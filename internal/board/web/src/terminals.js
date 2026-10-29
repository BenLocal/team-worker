// Live agent terminals rendered with ghostty-web, ported from ClawTeam's board
// (a68f67b6 plus the wheel-selection and text-selection fixes after it).
// ghostty-web inlines its WebAssembly (~650 KB), so it loads only when the
// Terminals view opens.
import { openMemberPrompt } from "./prompt-dialog.js";

let Terminal, FitAddon;

const THEME = { background: "#0b0f15", foreground: "#e8e0cc", cursor: "#e8c47e" };
const FONT = '"JetBrains Mono Variable", "JetBrains Mono", ui-monospace, Menlo, monospace';
let ready = null;
export const initTerminals = () => (ready ??= import("ghostty-web").then(async (mod) => {
  ({ Terminal, FitAddon } = mod);
  // The canvas measures glyphs when a terminal opens, so the web font must be loaded first.
  await Promise.all([mod.init(), document.fonts.load('13px "JetBrains Mono Variable"').catch(() => {})]);
}));

async function request(path, payload) {
  const response = await fetch(new URL(path, location.href), payload
    ? { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(payload) }
    : { cache: "no-store" });
  const data = await response.json();
  if (!response.ok) throw new Error(data.error || `Request failed (${response.status})`);
  return data;
}

// Only the selected card takes the mouse wheel; over any other card the wheel
// scrolls the page and never reaches a TUI as arrow keys.
let selected = null;
export function selectCard(card) {
  if (selected === card) return;
  selected?.element.classList.remove("selected");
  selected = card;
  card?.element.classList.add("selected");
}
document.addEventListener("pointerdown", (event) => {
  if (!event.target.closest?.(".term-card")) selectCard(null);
}, { capture: true });

// describe tells whether a member has a live terminal and, if not, why.
export function describe(member) {
  const a = member.agent;
  if (!a) return { live: false, log: false, reason: "Not started yet" };
  if (a.backend !== "tmux") {
    return { live: false, log: a.backend === "subprocess", reason: "Runs as a background process; showing its log" };
  }
  if (a.status === "exited" || a.alive === false) return { live: false, log: false, reason: "Agent terminal is offline" };
  return { live: true, log: false, reason: "" };
}

export class AgentTerminal {
  constructor(team, member, tasks) {
    this.team = team;
    this.member = member;
    this.state = describe(member);
    this.generation = 0;
    this.control = false;
    this.disposed = false;
    this.retryDelay = 1000;
    this.element = document.createElement("section");
    this.element.className = "term-card";
    this.element.dataset.agent = member.name;
    this.element.setAttribute("aria-label", `${member.name} terminal`);
    this.element.innerHTML = `
      <div class="term-head"><span class="dot" aria-hidden="true"></span><strong class="term-name"></strong>
        <span class="term-role"></span><span class="term-status" role="status"></span></div>
      <p class="term-task"></p>
      <div class="term-actions">
        <button type="button" class="prompt">Prompt</button>
        <button type="button" class="history">History</button>
        <button type="button" class="reconnect">Reconnect</button>
        <button type="button" class="expand">Full screen</button>
        <button type="button" class="control">Take control</button>
      </div>
      <p class="term-note" role="status"></p>
      <div class="term-host"></div>
      <p class="term-error" role="status"></p>`;
    this.host = this.element.querySelector(".term-host");
    this.statusEl = this.element.querySelector(".term-status");
    this.error = this.element.querySelector(".term-error");
    this.note = this.element.querySelector(".term-note");
    this.controlButton = this.element.querySelector(".control");
    this.historyButton = this.element.querySelector(".history");
    this.promptButton = this.element.querySelector(".prompt");
    this.element.querySelector(".term-name").textContent = member.name;
    this.promptButton.addEventListener("click", () => openMemberPrompt(this.team, this.member.name, this.member.user, this.promptButton));
    this.element.addEventListener("pointerdown", () => selectCard(this));
    this.element.addEventListener("focusin", () => selectCard(this));
    this.historyButton.addEventListener("click", () => {
      if (this.historyTerminal || this.historyLoading) this.closeHistory();
      else this.openHistory(-Math.max(5, this.terminal?.rows || 24));
    });
    this.controlButton.addEventListener("click", () => this.connect(!this.control));
    this.element.querySelector(".reconnect").addEventListener("click", () => this.connect(false));
    this.element.querySelector(".expand").addEventListener("click", async () => {
      try {
        if (document.fullscreenElement === this.element) await document.exitFullscreen();
        else { selectCard(this); await this.element.requestFullscreen(); }
      } catch (error) {
        this.error.textContent = error.message;
      }
    });
    this.host.addEventListener("wheel", (event) => this.onWheel(event), { capture: true, passive: false });
    this.host.addEventListener("keydown", (event) => this.onKey(event), { capture: true });
    this.observer = new ResizeObserver(() => this.fit());
    this.observer.observe(this.host);
    this.update(member, tasks, true);
  }

  update(member, tasks, first = false) {
    const before = this.state;
    this.member = member;
    this.state = describe(member);
    const role = member.agentType && member.agentType !== "leader" ? member.agentType : "";
    this.element.querySelector(".term-role").textContent = [member.leader ? "Leader" : "", role].filter(Boolean).join(", ");
    // Offline cards show the reason in the terminal area, so it is not repeated here.
    this.element.querySelector(".term-task").textContent = tasks.length ? "Working on " + tasks.join("; ") : "";
    this.element.querySelector(".dot").className = "dot" + (this.state.live ? " alive" : member.agent ? " dead" : "");
    // The prompt is stored server-side, so it stays viewable without a terminal.
    this.promptButton.hidden = false;
    this.promptButton.setAttribute("aria-label", `Show ${member.name}'s prompt`);
    for (const b of this.element.querySelectorAll(".term-actions button:not(.prompt)")) b.hidden = !this.state.live;
    this.element.classList.toggle("offline", !this.state.live);
    if (!this.state.live) {
      if (first || before.live || before.log !== this.state.log) {
        this.disconnect();
        this.setStatus(this.state.log ? "Log" : "Offline", "offline");
        if (this.state.log) this.startLog(); else this.host.replaceChildren(Object.assign(document.createElement("p"), { className: "term-reason", textContent: this.state.reason }));
      }
    } else if (first || !before.live) {
      this.stopLog();
      this.connect(false);
    }
  }

  setStatus(text, state) {
    this.statusEl.textContent = text;
    this.statusEl.dataset.state = state;
  }

  // Subprocess agents have no tmux pane; poll their log through the screen API.
  startLog() {
    this.stopLog();
    const pre = document.createElement("pre");
    pre.className = "term-log";
    this.host.replaceChildren(pre);
    const poll = async () => {
      try {
        const res = await request(`api/teams/${encodeURIComponent(this.team)}/agents/${encodeURIComponent(this.member.name)}/screen?history=500`);
        const atBottom = pre.scrollTop + pre.clientHeight >= pre.scrollHeight - 4;
        pre.textContent = res.screen;
        if (atBottom) pre.scrollTop = pre.scrollHeight;
      } catch (error) {
        pre.textContent = "Cannot read the log: " + error.message;
      }
    };
    poll();
    this.logTimer = setInterval(poll, 2000);
  }
  stopLog() { clearInterval(this.logTimer); this.logTimer = null; }

  onWheel(event) {
    if (!this.state.live || event.ctrlKey || !event.deltaY) return;
    if (selected !== this && document.fullscreenElement !== this.element) {
      // Keep ghostty from consuming it, but let the browser scroll the page.
      event.stopImmediatePropagation();
      return;
    }
    event.preventDefault();
    event.stopImmediatePropagation();
    const lines = Math.sign(event.deltaY) * Math.max(1, Math.ceil(Math.abs(event.deltaY) / 20));
    if (this.historyTerminal) {
      if (lines > 0 && this.historyTerminal.viewportY === 0) this.closeHistory();
      else this.historyTerminal.scrollLines(lines);
    } else if (this.historyLoading) this.historyOffset += lines;
    else if (lines < 0) this.openHistory(lines);
  }

  onKey(event) {
    if (this.historyTerminal) {
      const t = this.historyTerminal;
      const actions = {
        ArrowUp: () => t.scrollLines(-1), ArrowDown: () => t.scrollLines(1),
        PageUp: () => t.scrollPages(-1), PageDown: () => t.scrollPages(1),
        Home: () => t.scrollToTop(), End: () => t.scrollToBottom(), Escape: () => this.closeHistory(),
      };
      if (actions[event.key]) {
        event.preventDefault();
        event.stopImmediatePropagation();
        actions[event.key]();
      }
    } else if (event.shiftKey && event.key === "PageUp") {
      event.preventDefault();
      event.stopImmediatePropagation();
      this.openHistory(-Math.max(5, this.terminal?.rows || 24));
    }
  }

  newTerminal(blink) {
    return new Terminal({ fontSize: 13, fontFamily: FONT, cursorBlink: blink, scrollback: 5000, theme: THEME });
  }

  configureInput() {
    const input = this.liveHost.querySelector("textarea");
    this.imeInput = input;
    this.liveHost.contentEditable = "false";
    this.liveHost.style.position = "relative";
    Object.assign(input.style, {
      clipPath: "none", pointerEvents: "none", zIndex: "1",
      color: THEME.foreground, background: THEME.background,
    });
    // Ghostty focuses its contenteditable container; IME needs a real caret
    // anchored at the canvas cursor instead of the container's top-left corner.
    this.liveHost.addEventListener("focusin", (event) => {
      this.positionInput();
      if (event.target === this.liveHost) input.focus({ preventScroll: true });
    });
    input.addEventListener("compositionstart", () => {
      this.positionInput();
      input.style.opacity = "1";
    });
    input.addEventListener("compositionend", () => {
      queueMicrotask(() => { input.value = ""; input.style.opacity = "0"; });
    });
    this.positionInput();
    if (document.activeElement === this.liveHost) input.focus({ preventScroll: true });
  }

  positionInput() {
    const input = this.imeInput;
    const canvas = this.liveHost?.querySelector("canvas");
    if (!input || !canvas || !this.terminal?.renderer) return;
    const { width, height } = this.terminal.renderer.getMetrics();
    const cursor = this.terminal.buffer.active;
    const x = Math.min(cursor.cursorX, this.terminal.cols - 1) * width;
    Object.assign(input.style, {
      position: "absolute", left: canvas.offsetLeft + x + "px",
      top: canvas.offsetTop + cursor.cursorY * height + "px",
      width: Math.max(width, canvas.offsetWidth - x) + "px", height: height + "px",
      fontFamily: this.terminal.options.fontFamily,
      fontSize: this.terminal.options.fontSize + "px", lineHeight: height + "px",
    });
  }

  fit() {
    if (!this.terminal || !this.host.clientWidth) return;
    if (this.historyTerminal) { this.historyFit.fit(); return; }
    if (this.paneSize) { this.layout(); return; }
    this.fitAddon.fit();
    const cols = Math.max(20, Math.min(400, this.terminal.cols));
    const rows = Math.max(5, Math.min(150, this.terminal.rows));
    if (cols !== this.terminal.cols || rows !== this.terminal.rows) this.terminal.resize(cols, rows);
    this.send({ type: "resize", cols, rows });
  }

  // setPaneSize adopts the agent window's size, so the whole window shows
  // even when a larger tmux client attached elsewhere decides that size.
  setPaneSize(cols, rows) {
    this.paneSize = { cols, rows };
    if (this.terminal.cols !== cols || this.terminal.rows !== rows) this.terminal.resize(cols, rows);
    // Keep the viewer's client the window's size so tmux draws all of it.
    this.send({ type: "resize", cols: Math.max(20, Math.min(400, cols)), rows: Math.max(5, Math.min(150, rows)) });
    this.layout();
  }

  // layout picks the largest font that fits the window's columns into the
  // card, and lets the card grow to the window's rows; full screen fits both.
  layout() {
    const canvas = this.liveHost?.querySelector("canvas");
    if (!canvas || !this.paneSize || this.historyTerminal) return;
    const full = document.fullscreenElement === this.element;
    // A window too wide to read in the card at 9px takes the whole grid row;
    // it returns to one column once the window fits again.
    const readable = this.paneSize.cols * 0.6 * 9;
    const wide = this.element.classList.contains("wide");
    if (!full && !wide && this.host.clientWidth - 16 < readable) {
      this.narrowWidth = this.host.clientWidth;
      this.element.classList.add("wide");
    } else if (wide && this.narrowWidth && this.narrowWidth - 16 >= readable) {
      this.element.classList.remove("wide");
    }
    const width = this.host.clientWidth - 16;
    const height = full ? this.host.clientHeight - 12 : Infinity;
    let size = Math.max(7, Math.min(full ? 20 : 14, Math.floor(width / (this.paneSize.cols * 0.6))));
    for (;;) {
      if (this.terminal.options.fontSize !== size) this.terminal.options.fontSize = size;
      if ((canvas.offsetWidth <= width && canvas.offsetHeight <= height) || size <= 7) break;
      size--;
    }
    // ghostty redraws the canvas for a new font on the next frame, so size the
    // card again once the canvas has its final height.
    const grow = () => {
      this.host.style.height = full ? "" : canvas.offsetHeight + 12 + "px";
      this.positionInput();
    };
    grow();
    requestAnimationFrame(() => requestAnimationFrame(grow));
  }

  send(message) {
    if (this.socket?.readyState === WebSocket.OPEN) this.socket.send(JSON.stringify(message));
  }

  openHistory(offset) {
    if (this.socket?.readyState !== WebSocket.OPEN) return;
    this.historyLoading = true;
    this.historyOffset = offset;
    this.historyButton.textContent = "Back to live";
    this.note.textContent = "Loading history…";
    this.send({ type: "history" });
  }

  showHistory(message) {
    if (!this.historyLoading) return;
    if (message.error) {
      this.closeHistory();
      this.error.textContent = message.error;
      return;
    }
    this.historyLoading = false;
    this.liveHost.hidden = true;
    this.historyHost.hidden = false;
    this.historyHost.replaceChildren();
    const terminal = this.newTerminal(false);
    this.historyTerminal = terminal;
    this.historyFit = new FitAddon();
    terminal.loadAddon(this.historyFit);
    terminal.attachCustomKeyEventHandler(() => false);
    terminal.open(this.historyHost);
    this.historyFit.fit();
    const generation = this.generation;
    terminal.write(message.text.replace(/\n/g, "\r\n") + "\x1b[0m", () => {
      if (generation !== this.generation || this.historyTerminal !== terminal) return;
      terminal.scrollToBottom();
      terminal.scrollLines(this.historyOffset);
    });
    terminal.focus();
    this.note.textContent = "History snapshot, input paused. Scroll or use Page Up and Page Down; Esc returns to live."
      + (message.truncated ? " Older output was trimmed." : "");
  }

  closeHistory(refocus = true) {
    this.historyLoading = false;
    this.historyTerminal?.dispose();
    this.historyTerminal = null;
    if (this.liveHost) this.liveHost.hidden = false;
    if (this.historyHost) { this.historyHost.hidden = true; this.historyHost.replaceChildren(); }
    this.historyButton.textContent = "History";
    this.note.textContent = "";
    if (refocus) {
      this.fit();
      if (this.control) this.terminal?.focus();
    }
  }

  disconnect() {
    this.generation++;
    this.closeHistory(false);
    clearTimeout(this.retryTimer);
    if (this.socket) { this.socket.onclose = null; this.socket.close(); this.socket = null; }
    this.inputSubscription?.dispose();
    this.imeInput = null;
    this.terminal?.dispose();
    this.terminal = null;
    this.control = false;
    this.element.classList.remove("controlling");
    this.controlButton.textContent = "Take control";
  }

  retry(message) {
    this.error.textContent = message;
    this.retryTimer = setTimeout(() => this.connect(false), this.retryDelay);
    this.retryDelay = Math.min(10000, this.retryDelay * 2);
  }

  async connect(control) {
    this.disconnect();
    this.paneSize = null;
    this.host.style.height = "";
    this.element.classList.remove("wide");
    if (this.disposed || !this.state.live) return;
    const generation = this.generation;
    this.error.textContent = "";
    this.liveHost = Object.assign(document.createElement("div"), { className: "term-surface" });
    this.historyHost = Object.assign(document.createElement("div"), { className: "term-surface", hidden: true });
    this.host.replaceChildren(this.liveHost, this.historyHost);
    this.setStatus("Connecting…", "connecting");
    this.controlButton.disabled = true;
    try {
      await initTerminals();
      if (generation !== this.generation || this.disposed) return;
      this.terminal = this.newTerminal(control);
      this.fitAddon = new FitAddon();
      this.terminal.loadAddon(this.fitAddon);
      this.terminal.open(this.liveHost);
      if (control) this.configureInput();
      this.fit();
      const data = await request("api/terminal/ticket", {
        team: this.team, agent: this.member.name, control, cols: this.terminal.cols, rows: this.terminal.rows,
      });
      if (generation !== this.generation || this.disposed) return;
      const url = new URL("api/terminal/ws", location.href);
      url.protocol = location.protocol === "https:" ? "wss:" : "ws:";
      url.searchParams.set("ticket", data.ticket);
      const socket = new WebSocket(url);
      socket.binaryType = "arraybuffer";
      this.socket = socket;
      socket.onopen = () => {
        if (generation !== this.generation) return socket.close();
        this.retryDelay = 1000;
        this.control = control;
        this.element.classList.toggle("controlling", control);
        this.controlButton.disabled = false;
        this.controlButton.textContent = control ? "Release control" : "Take control";
        this.setStatus(control ? "Controlling" : "Watching", "connected");
        this.fit();
        if (control) { selectCard(this); this.terminal.focus(); }
      };
      socket.onmessage = (event) => {
        if (generation !== this.generation) return;
        if (typeof event.data === "string") {
          const message = JSON.parse(event.data);
          if (message.type === "history") this.showHistory(message);
          else if (message.type === "size") this.setPaneSize(message.cols, message.rows);
        } else this.terminal.write(new Uint8Array(event.data), () => this.positionInput());
      };
      socket.onclose = () => {
        if (generation !== this.generation || this.disposed) return;
        this.control = false;
        this.controlButton.textContent = "Take control";
        this.controlButton.disabled = false;
        this.element.classList.remove("controlling");
        this.setStatus("Disconnected", "disconnected");
        this.retry(control
          ? "Control ended. Reconnecting to watch; someone else may have control now."
          : "Connection lost. Reconnecting…");
      };
      this.inputSubscription = this.terminal.onData((input) => {
        if (this.control && !this.historyTerminal && !this.historyLoading && generation === this.generation) {
          this.send({ type: "input", data: input });
        }
      });
    } catch (error) {
      if (generation !== this.generation || this.disposed) return;
      this.controlButton.disabled = false;
      this.setStatus("Unavailable", "disconnected");
      this.retry(error.message);
    }
  }

  dispose() {
    this.disposed = true;
    this.disconnect();
    this.stopLog();
    this.observer.disconnect();
    if (selected === this) selectCard(null);
    this.element.remove();
  }
}
