import "./style.css";
import { AgentTerminal, selectCard } from "./terminals.js";

const $ = (id) => document.getElementById(id);
const SVG = "http://www.w3.org/2000/svg";
const COLUMNS = [["pending", "Pending"], ["in_progress", "In progress"], ["blocked", "Blocked"], ["completed", "Done"]];
const PRIORITY = { urgent: 0, high: 1, medium: 2, low: 3 };
const DONE_PAGE = 15, MSG_PAGE = 60, GAP_MS = 10 * 60 * 1000, LANE_H = 14;

const state = {
  teams: [], boards: {}, team: new URLSearchParams(location.search).get("team") || "", expanded: new Set(),
  view: new URLSearchParams(location.search).get("view") === "terminals" ? "terminals" : "board",
  query: "", owner: "", doneShown: DONE_PAGE, open: new Set(),
  msgShown: MSG_PAGE, range: null, selected: null, follow: true,
  keys: {}, links: [],
};
const terms = new Map();

function el(tag, attrs = {}, ...children) {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (v == null || v === false) continue;
    if (k === "class") node.className = v;
    else if (k.startsWith("on")) node.addEventListener(k.slice(2), v);
    else node.setAttribute(k, v === true ? "" : v);
  }
  for (const c of children.flat()) if (c != null && c !== false) node.append(c);
  return node;
}
function svg(tag, attrs = {}, text) {
  const node = document.createElementNS(SVG, tag);
  for (const [k, v] of Object.entries(attrs)) node.setAttribute(k, v);
  if (text != null) node.textContent = text;
  return node;
}
async function api(path) {
  const res = await fetch(path);
  const body = await res.json();
  if (!res.ok) throw new Error(body.error || res.statusText);
  return body;
}
const pad = (n) => String(n).padStart(2, "0");
const clock = (ms) => { const d = new Date(ms); return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`; };
const shortClock = (ms) => { const d = new Date(ms); return `${pad(d.getHours())}:${pad(d.getMinutes())}`; };
function duration(secs) {
  if (!secs) return "";
  if (secs < 90) return Math.round(secs) + "s";
  if (secs < 5400) return Math.round(secs / 60) + "m";
  return (secs / 3600).toFixed(1) + "h";
}
function agentState(m) {
  const a = m.agent;
  if (!a) return ["", "not started"];
  if (a.status === "exited") return ["dead", "exited"];
  if (a.alive === true) return ["alive", "running"];
  if (a.alive === false) return ["dead", "stopped"];
  return ["", "unknown"];
}
// changed reports whether a region's inputs differ from its last render.
function changed(region, value) {
  const key = JSON.stringify(value);
  if (state.keys[region] === key) return false;
  state.keys[region] = key;
  return true;
}

/* ---------------- rail ---------------- */
const chevron = () => { const s = svg("svg", { width: 10, height: 10, viewBox: "0 0 10 10", "aria-hidden": "true" });
  s.append(svg("path", { d: "M3 1.5 6.5 5 3 8.5", fill: "none", stroke: "currentColor", "stroke-width": 1.6 })); return s; };

function renderRail() {
  const view = state.teams.map((t) => [t, state.boards[t.name]]);
  if (!changed("rail", [view, state.team, [...state.expanded]])) return;
  if (!state.teams.length) {
    $("teams").replaceChildren(el("li", { class: "empty muted" }, "No teams yet"));
    return;
  }
  $("teams").replaceChildren(...view.map(([t, b]) => {
    const s = b && b.taskSummary;
    const current = t.name === state.team;
    const open = current || state.expanded.has(t.name);
    const members = b ? b.members : [];
    const running = members.filter((m) => agentState(m)[0] === "alive").length;
    const strip = el("div", { class: "strip", "aria-hidden": "true" });
    if (s && s.total) {
      for (const [cls, n] of [["done", s.completed], ["progress", s.in_progress], ["blocked", s.blocked], ["pending", s.pending]]) {
        if (n) strip.append(el("span", { class: cls, style: `width:${(n / s.total) * 100}%` }));
      }
    }
    const people = open && members.length ? el("ul", { class: "people", id: "people-" + t.name, "aria-label": t.name + " members" },
      members.map((m) => {
        const [cls, label] = agentState(m);
        const leader = b && m.agentId === b.team.leadAgentId;
        return el("li", {}, el("button", { type: "button", class: "person",
          title: m.agent ? `Show ${m.name}'s terminal` : `${m.name} has not been started`,
          onclick: () => showTerminal(t.name, m.name) },
          el("span", { class: "dot " + cls, "aria-hidden": "true" }),
          el("span", { class: "who" }, el("span", { class: "pname" }, m.name),
            el("span", { class: "role" }, (leader ? "Leader, " : "") + label)),
          m.inboxCount ? el("span", { class: "unread", title: `${m.inboxCount} unread messages` }, String(m.inboxCount)) : null));
      })) : null;
    return el("li", { class: "team" + (current ? " current" : "") },
      el("div", { class: "team-row" },
        el("button", { type: "button", class: "team-tab", "aria-current": current ? "page" : null, onclick: () => selectTeam(t.name) },
          el("div", { class: "name" }, t.name),
          t.description ? el("div", { class: "desc", title: t.description }, t.description) : null,
          strip,
          el("div", { class: "facts" },
            el("span", {}, s ? (s.total ? `${s.completed} of ${s.total} done` : "No tasks") : ""),
            el("span", {}, running ? `${running} running` : `${t.memberCount} members`))),
        current ? null : el("button", { type: "button", class: "toggle", "aria-expanded": String(open),
          "aria-controls": "people-" + t.name, "aria-label": (open ? "Hide " : "Show ") + t.name + " members",
          onclick: () => { open ? state.expanded.delete(t.name) : state.expanded.add(t.name); renderRail(); } }, chevron())),
      people);
  }));
}

/* ---------------- skeleton ---------------- */
function buildMain() {
  state.keys = {};
  const search = el("input", { type: "search", id: "filter", placeholder: "Filter by subject, ID or owner", "aria-label": "Filter tasks",
    oninput: (e) => { state.query = e.target.value; update(); } });
  const owner = el("select", { id: "owner", "aria-label": "Owner", onchange: (e) => { state.owner = e.target.value; update(); } });
  const clear = el("button", { type: "button", id: "clear-filter", class: "quiet-btn", hidden: true,
    onclick: () => { state.query = ""; state.owner = ""; search.value = ""; update(); } }, "Clear filter");
  const tab = (view, label) => el("button", { type: "button", role: "tab", id: "tab-" + view, "aria-controls": "view-" + view,
    "aria-selected": String(state.view === view), onclick: () => setView(view) }, label);
  $("main").replaceChildren(
    el("div", { class: "head-row" }, el("header", { class: "head", id: "head" }),
      el("div", { class: "views", role: "tablist", "aria-label": "View" }, tab("board", "Board"), tab("terminals", "Terminals")),
      el("nav", { class: "links", id: "links", "aria-label": "Other services" })),
    el("div", { class: "view", id: "view-board", role: "tabpanel", "aria-labelledby": "tab-board", hidden: state.view !== "board" },
    el("section", { class: "tasks", "aria-label": "Tasks" },
      el("div", { class: "toolbar" }, search, owner, clear),
      el("div", { class: "board", id: "board" })),
    el("section", { class: "traj", "aria-label": "Messages" },
      el("div", { class: "traj-head" }, el("h2", {}, "Messages"),
        el("button", { type: "button", class: "quiet-btn", id: "clear-range", hidden: true, onclick: () => setRange(null) }),
        el("span", { class: "count muted", id: "msg-count" })),
      el("div", { class: "overview", id: "overview" }),
      el("div", { class: "traj-body" },
        el("div", { class: "ledger", id: "ledger", tabindex: "0", role: "listbox", "aria-label": "Message ledger",
          onscroll: (e) => { const n = e.target; state.follow = n.scrollTop + n.clientHeight >= n.scrollHeight - 8; },
          onkeydown: ledgerKeys }),
        el("aside", { class: "inspector", id: "inspector", "aria-live": "polite" })))),
    el("div", { class: "view", id: "view-terminals", role: "tabpanel", "aria-labelledby": "tab-terminals", hidden: state.view !== "terminals" },
      el("div", { class: "terms", id: "terms" },
        el("p", { class: "intro" }, "Click a terminal to select it: the mouse wheel then scrolls that terminal, otherwise it scrolls this list. "
          + "Scroll up or choose History to read earlier output, and Back to live to return. Take control to type, paste or press Ctrl+C. "
          + "Closing this page leaves the agents running."))));
  bindOverview();
  renderLinks();
}

/* ---------------- links to other services (board serve --link) ---------------- */
function renderLinks() {
  const nav = $("links");
  if (!nav) return;
  nav.replaceChildren(...state.links.map((l) => el("a", { href: l.url, target: "_blank", rel: "noopener" }, l.name)));
}

/* ---------------- header + tasks ---------------- */
function renderHead(board) {
  const s = board.taskSummary;
  if (!changed("head", [board.team, s])) return;
  $("head").replaceChildren(
    el("div", {}, el("h1", {}, board.team.name), board.team.description ? el("p", { class: "desc" }, board.team.description) : null),
    el("div", { class: "progress-text" },
      el("div", {}, el("strong", {}, `${s.completed} of ${s.total}`), el("span", { class: "muted" }, " done")),
      s.avg_duration_seconds ? el("div", { class: "muted" }, `about ${duration(s.avg_duration_seconds)} per task`) : null));
}
function matches(t) {
  if (state.owner && t.owner !== state.owner) return false;
  if (!state.query) return true;
  const q = state.query.toLowerCase();
  return [t.subject, t.id, t.owner, t.description].some((v) => v && v.toLowerCase().includes(q));
}
function sortTasks(status, tasks) {
  const newest = (f) => (a, b) => (b[f] || "").localeCompare(a[f] || "");
  if (status === "completed") return tasks.sort(newest("updatedAt"));
  if (status === "in_progress") return tasks.sort(newest("startedAt"));
  return tasks.sort((a, b) => (PRIORITY[a.priority] ?? 2) - (PRIORITY[b.priority] ?? 2) || (a.createdAt || "").localeCompare(b.createdAt || ""));
}
function taskCard(t, byId) {
  const line = el("div", { class: "line" }, el("span", {}, t.id),
    t.priority && t.priority !== "medium" ? el("span", { class: "prio-" + t.priority }, t.priority) : null,
    el("span", {}, t.owner ? "@" + t.owner : "unassigned"),
    t.status === "completed" && t.metadata && t.metadata.duration_seconds ? el("span", {}, duration(t.metadata.duration_seconds)) : null);
  const waits = t.blockedBy && t.blockedBy.length
    ? el("div", { class: "waits" }, "Waits for " + t.blockedBy.map((id) => byId[id] ? byId[id].subject : id).join(", ")) : null;
  const head = [el("div", { class: "subject" }, t.subject), line, waits];
  if (!t.description) return el("div", { class: "task" }, head);
  const card = el("details", { class: "task", open: state.open.has(t.id) }, el("summary", {}, head), el("div", { class: "body" }, t.description));
  card.addEventListener("toggle", () => card.open ? state.open.add(t.id) : state.open.delete(t.id));
  return card;
}
function renderTasks(board) {
  const owners = [...new Set(board.tasks.map((t) => t.owner).filter(Boolean))].sort();
  if (state.owner && !owners.includes(state.owner)) owners.push(state.owner);
  if (changed("owners", [owners, state.owner])) {
    $("owner").replaceChildren(el("option", { value: "" }, "Everyone"),
      owners.map((o) => el("option", { value: o, selected: o === state.owner }, o)));
  }
  const filtering = Boolean(state.query || state.owner);
  $("clear-filter").hidden = !filtering;
  if (!changed("tasks", [board.tasks, state.query, state.owner, state.doneShown])) return;
  const scroll = {};
  $("board").querySelectorAll(".list").forEach((n) => { scroll[n.dataset.col] = n.scrollTop; });
  const byId = Object.fromEntries(board.tasks.map((t) => [t.id, t]));
  $("board").replaceChildren(...COLUMNS.map(([status, title]) => {
    const all = board.tasks.filter((t) => t.status === status);
    let tasks = sortTasks(status, all.filter(matches));
    let hidden = 0;
    if (status === "completed" && !filtering && tasks.length > state.doneShown) {
      hidden = tasks.length - state.doneShown;
      tasks = tasks.slice(0, state.doneShown);
    }
    const items = tasks.map((t) => taskCard(t, byId));
    if (hidden) items.push(el("button", { type: "button", class: "quiet-btn",
      onclick: () => { state.doneShown += hidden; update(); } }, `Show ${hidden} earlier`));
    if (!items.length) items.push(el("div", { class: "none" }, filtering && all.length ? "No matches" : "Nothing here"));
    return el("section", { class: "col col-" + status, "aria-label": title },
      el("h2", {}, el("span", {}, title), el("span", { class: "n" }, filtering ? `${tasks.length} of ${all.length}` : String(all.length))),
      el("div", { class: "list", "data-col": status }, items));
  }));
  $("board").querySelectorAll(".list").forEach((n) => { n.scrollTop = scroll[n.dataset.col] || 0; });
}

/* ---------------- trajectory ---------------- */
function messageRecords(board) {
  return board.messages.map((m, i) => {
    const at = Date.parse(m.timestamp) || 0;
    const lifecycle = Boolean(m.type && m.type !== "message");
    return { ...m, at, lifecycle, channel: lifecycle ? "lifecycle" : (m.to ? "message" : "broadcast"),
      key: `${m.timestamp}|${m.from}|${m.to || ""}|${m.requestId || i}` };
  }).sort((a, b) => a.at - b.at);
}
function lanesFor(board, records) {
  const names = board.members.map((m) => m.name);
  for (const r of records) if (r.from && !names.includes(r.from)) names.push(r.from);
  return names;
}
function domain(records) {
  if (!records.length) return null;
  let lo = records[0].at, hi = records[records.length - 1].at;
  if (hi - lo < 60000) { lo -= 30000; hi += 30000; }
  return [lo, hi];
}
let overviewGeom = null;
function renderOverview(board, records) {
  const box = $("overview");
  const width = Math.max(240, box.clientWidth - 24);
  if (!changed("overview", [records.map((r) => [r.key, r.from, r.channel]), board.members.map((m) => m.name), state.range, state.selected, width])) return;
  const dom = domain(records);
  if (!dom) { box.replaceChildren(el("div", { class: "none" }, "No messages yet. Agents report here when they finish a task or get stuck.")); overviewGeom = null; return; }
  const lanes = lanesFor(board, records);
  const labelW = 72, top = 2, height = top + lanes.length * LANE_H + 16;
  const x = (t) => labelW + ((t - dom[0]) / (dom[1] - dom[0])) * (width - labelW - 6);
  overviewGeom = { dom, labelW, width, x, t: (px) => dom[0] + ((px - labelW) / (width - labelW - 6)) * (dom[1] - dom[0]) };
  const s = svg("svg", { viewBox: `0 0 ${width} ${height}`, height, role: "img",
    "aria-label": `Timeline of ${records.length} messages from ${shortClock(dom[0])} to ${shortClock(dom[1])}. Drag to show a time range.` });
  lanes.forEach((name, i) => {
    const y = top + i * LANE_H + LANE_H / 2;
    s.append(svg("line", { class: "lane-line", x1: labelW, x2: width, y1: y, y2: y }));
    s.append(svg("text", { class: "lane-label", x: 0, y: y + 4 }, name.length > 10 ? name.slice(0, 9) + "…" : name));
  });
  if (state.range) {
    const [a, b] = state.range.map(x);
    s.append(svg("rect", { class: "range", x: Math.min(a, b), y: 0, width: Math.max(2, Math.abs(b - a)), height: height - 14, rx: 2 }));
  }
  for (const r of records) {
    const lane = Math.max(0, lanes.indexOf(r.from));
    const y = top + lane * LANE_H + 3;
    s.append(svg("rect", { class: r.key === state.selected ? "t-selected" : "t-" + r.channel,
      x: x(r.at) - 1.5, y, width: 3, height: LANE_H - 6, rx: 1 }));
  }
  const ticks = 4;
  for (let i = 0; i <= ticks; i++) {
    const t = dom[0] + ((dom[1] - dom[0]) * i) / ticks;
    s.append(svg("text", { class: "axis", x: x(t), y: height - 2, "text-anchor": i === 0 ? "start" : i === ticks ? "end" : "middle" }, shortClock(t)));
  }
  box.replaceChildren(s);
}
function bindOverview() {
  const box = $("overview");
  let start = null;
  const px = (e) => { const r = box.querySelector("svg").getBoundingClientRect(); return ((e.clientX - r.left) / r.width) * overviewGeom.width; };
  box.addEventListener("pointerdown", (e) => {
    if (e.button !== 0 || !overviewGeom || !box.querySelector("svg")) return;
    start = Math.max(overviewGeom.labelW, px(e));
    box.setPointerCapture(e.pointerId);
  });
  box.addEventListener("pointermove", (e) => {
    if (start == null) return;
    const end = Math.max(overviewGeom.labelW, px(e));
    if (Math.abs(end - start) > 3) { state.range = [overviewGeom.t(start), overviewGeom.t(end)].sort((a, b) => a - b); update(); }
  });
  box.addEventListener("pointerup", (e) => {
    if (start == null) return;
    const end = Math.max(overviewGeom.labelW, px(e));
    if (Math.abs(end - start) <= 3) selectNearest(overviewGeom.t(end));
    start = null;
  });
  box.addEventListener("contextmenu", (e) => { if (state.range) { e.preventDefault(); setRange(null); } });
  new ResizeObserver(() => update()).observe(box);
}
function setRange(range) {
  state.range = range;
  state.follow = !range;
  update();
}
function selectNearest(t) {
  const records = messageRecords(state.boards[state.team]);
  let best = null;
  for (const r of records) if (!best || Math.abs(r.at - t) < Math.abs(best.at - t)) best = r;
  if (best) select(best.key, true);
}
function visibleRecords(records) {
  if (state.range) return records.filter((r) => r.at >= state.range[0] && r.at <= state.range[1]);
  return records.slice(-state.msgShown);
}
function renderLedger(records) {
  const shown = visibleRecords(records);
  const hiddenEarlier = state.range ? 0 : records.length - shown.length;
  $("msg-count").textContent = state.range ? `${shown.length} of ${records.length} in range` : `${records.length} messages`;
  const clearRange = $("clear-range");
  clearRange.hidden = !state.range;
  if (state.range) clearRange.textContent = `Clear range ${shortClock(state.range[0])}–${shortClock(state.range[1])}`;
  if (!changed("ledger", [shown.map((r) => r.key), hiddenEarlier, state.selected])) return;
  const ledger = $("ledger");
  const before = { top: ledger.scrollTop, height: ledger.scrollHeight };
  const rows = [];
  if (hiddenEarlier) rows.push(el("button", { type: "button", class: "quiet-btn older", onclick: () => {
    state.follow = false; state.msgShown += MSG_PAGE; update(); } }, `Load ${Math.min(MSG_PAGE, hiddenEarlier)} earlier (${hiddenEarlier} more)`));
  let prev = null;
  for (const r of shown) {
    if (!prev || r.at - prev.at > GAP_MS) {
      rows.push(el("div", { class: "gap", role: "presentation" },
        prev ? `${shortClock(r.at)}, ${Math.round((r.at - prev.at) / 60000)} min later` : new Date(r.at).toLocaleDateString() + " " + shortClock(r.at)));
    }
    prev = r;
    const first = (r.content || r.summary || r.reason || "").split("\n")[0];
    rows.push(el("button", { type: "button", role: "option", class: "row" + (r.lifecycle ? " lifecycle" : ""), "data-key": r.key,
      "aria-selected": String(r.key === state.selected), onclick: () => select(r.key, false) },
      el("span", { class: "t" }, clock(r.at)),
      el("span", { class: "route" }, el("b", {}, r.from || "unknown"), " → ", r.to || "everyone"),
      el("span", { class: "gist" }, r.lifecycle ? el("span", { class: "kind" }, r.type.replaceAll("_", " ")) : null, first)));
  }
  if (!shown.length) rows.push(el("div", { class: "none" }, state.range ? "No messages in this range." : "No messages yet."));
  ledger.replaceChildren(...rows);
  if (state.follow) ledger.scrollTop = ledger.scrollHeight;
  else if (hiddenEarlier || before.height) ledger.scrollTop = before.top + (ledger.scrollHeight - before.height) * (before.top < 40 ? 1 : 0);
}
function renderInspector(records) {
  const r = records.find((x) => x.key === state.selected);
  if (!changed("inspector", r || null)) return;
  if (!r) {
    $("inspector").replaceChildren(el("p", { class: "muted" }, "Select a message to read it in full. Drag across the timeline to look at a time range."));
    return;
  }
  const fields = [["From", r.from], ["To", r.to || "everyone"], ["Type", r.type ? r.type.replaceAll("_", " ") : "message"],
    ["Sent", new Date(r.at).toLocaleString()], ["Request", r.requestId], ["Status", r.status], ["Summary", r.summary],
    ["Reason", r.reason], ["Last task", r.lastTask], ["Plan file", r.planFile], ["Feedback", r.feedback]].filter(([, v]) => v);
  $("inspector").replaceChildren(
    el("dl", {}, fields.flatMap(([k, v]) => [el("dt", {}, k), el("dd", {}, v)])),
    r.content ? el("div", { class: "content" }, r.content) : null,
    r.plan ? el("div", { class: "content" }, r.plan) : null);
}
function select(key, reveal) {
  state.selected = key;
  update();
  if (reveal) {
    const row = $("ledger").querySelector(`[data-key="${CSS.escape(key)}"]`);
    if (row) row.scrollIntoView({ block: "nearest" });
  }
}
function ledgerKeys(e) {
  if (e.key === "Escape" && state.range) { setRange(null); return; }
  if (e.key !== "ArrowDown" && e.key !== "ArrowUp") return;
  e.preventDefault();
  const keys = [...$("ledger").querySelectorAll(".row")].map((n) => n.dataset.key);
  if (!keys.length) return;
  const i = keys.indexOf(state.selected);
  const next = i < 0 ? keys.length - 1 : Math.min(keys.length - 1, Math.max(0, i + (e.key === "ArrowDown" ? 1 : -1)));
  select(keys[next], true);
}

/* ---------------- update loop ---------------- */
function update() {
  const board = state.boards[state.team];
  if (!board || !$("board")) return;
  renderHead(board);
  if (state.view === "terminals") { renderTerminals(board); return; }
  renderTasks(board);
  const records = messageRecords(board);
  renderOverview(board, records);
  renderLedger(records);
  renderInspector(records);
}
function showNotice(text, error = false) {
  $("main").replaceChildren(el("div", { class: "notice" + (error ? " error" : " muted") }, text));
}
async function loadTeams() {
  state.teams = await api("api/teams");
  if (!state.teams.some((t) => t.name === state.team)) state.team = state.teams.length ? state.teams[0].name : "";
  renderRail();
  if (!state.team) showNotice("No teams yet. Create one with team-worker team spawn-team <name>.");
}
async function loadBoard(name) { state.boards[name] = await api("api/teams/" + encodeURIComponent(name)); }
async function refreshSelected() {
  if (!state.team) return;
  try {
    await loadBoard(state.team);
    if (!$("board")) buildMain();
    update();
    renderRail();
  } catch (e) {
    showNotice(`Cannot load ${state.team}: ${e.message}`, true);
  }
}
async function refreshOthers() {
  await Promise.allSettled(state.teams.filter((t) => t.name !== state.team).map((t) => loadBoard(t.name)));
  renderRail();
}
function selectTeam(name) {
  if (name === state.team) return;
  state.team = name;
  Object.assign(state, { query: "", owner: "", doneShown: DONE_PAGE, msgShown: MSG_PAGE, range: null, selected: null, follow: true });
  state.open.clear();
  disposeTerminals();
  writeURL();
  renderRail();
  if (state.boards[name]) { buildMain(); update(); } else showNotice("Loading " + name + "…");
  refreshSelected();
}

/* ---------------- views and live terminals ---------------- */
function writeURL() {
  const q = new URLSearchParams({ team: state.team });
  if (state.view === "terminals") q.set("view", "terminals");
  history.replaceState(null, "", "?" + q);
}
function setView(view) {
  if (state.view === view) return;
  state.view = view;
  for (const v of ["board", "terminals"]) {
    const panel = $("view-" + v);
    if (panel) panel.hidden = v !== view;
    $("tab-" + v)?.setAttribute("aria-selected", String(v === view));
  }
  // Hidden terminals would keep their WebSockets and tmux viewers open.
  if (view !== "terminals") disposeTerminals();
  state.keys = {};
  writeURL();
  update();
}
function renderTerminals(board) {
  const names = new Set(board.members.map((m) => m.name));
  for (const [name, card] of terms) if (!names.has(name)) { card.dispose(); terms.delete(name); }
  for (const m of board.members) {
    const member = { ...m, leader: m.agentId === board.team.leadAgentId };
    const tasks = board.tasks.filter((t) => t.status === "in_progress" && t.owner === m.name).map((t) => t.subject);
    if (terms.has(m.name)) terms.get(m.name).update(member, tasks);
    else {
      const card = new AgentTerminal(state.team, member, tasks);
      terms.set(m.name, card);
      $("terms").append(card.element);
    }
  }
}
function disposeTerminals() {
  for (const card of terms.values()) card.dispose();
  terms.clear();
}
function showTerminal(team, name) {
  selectTeam(team);
  setView("terminals");
  const card = terms.get(name);
  if (card) { selectCard(card); card.element.scrollIntoView({ block: "nearest" }); }
}
window.addEventListener("pagehide", disposeTerminals);

// Older servers have no /api/links; the header then simply shows none.
api("api/links").then((links) => { state.links = links; renderLinks(); }).catch(() => {});
loadTeams().then(() => Promise.all([refreshSelected(), refreshOthers()]))
  .catch((e) => showNotice("Cannot reach team-workerd: " + e.message, true));
setInterval(refreshSelected, 2000);
setInterval(refreshOthers, 10000);
setInterval(() => loadTeams().catch(() => {}), 15000);
