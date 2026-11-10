import "@fontsource-variable/jetbrains-mono";
import "./style.css";
import { AgentTerminal, describe, selectCard, setBoardHost } from "./terminals.js";
import { openMemberPrompt, openTeamPrompt } from "./prompt-dialog.js";
import { openServiceKeys } from "./service-keys.js";

const $ = (id) => document.getElementById(id);
const SVG = "http://www.w3.org/2000/svg";
const COLUMNS = [["pending", "Pending"], ["in_progress", "In progress"], ["blocked", "Blocked"], ["completed", "Done"]];
const PRIORITY = { urgent: 0, high: 1, medium: 2, low: 3 };
const DONE_PAGE = 15, MSG_PAGE = 60, GAP_MS = 10 * 60 * 1000, LANE_H = 14;
const VIEWS = [["board", "Board"], ["results", "Results"], ["messages", "Messages"], ["approvals", "Approvals"], ["terminals", "Terminals"]];
const RAIL_KEY = "team-worker.rail";
const TERM_LAYOUT_KEY = "team-worker.terminals";

// Storage can be unavailable (private windows, blocked site data); the rail then just starts open.
function stored(key) { try { return localStorage.getItem(key); } catch { return null; } }
function store(key, value) { try { localStorage.setItem(key, value); } catch { /* keep the in-page state */ } }
const initialView = new URLSearchParams(location.search).get("view");

const state = {
  teams: [], boards: {}, team: new URLSearchParams(location.search).get("team") || "", 
  view: VIEWS.some(([v]) => v === initialView) ? initialView : "board", railOpen: stored(RAIL_KEY) !== "closed",
  doneShown: DONE_PAGE, open: new Set(), resultOpen: new Set(), oversight: {}, openPlans: new Set(), approvalsTab: "requests",
  // Decision request cards: open bodies, the action being written per
  // request, requests being sent, their errors and AI decisions being overridden.
  req: { open: new Set(), form: {}, busy: new Set(), errors: {}, overriding: new Set(), autoBusy: false, autoError: "", autoWindow: null },
  msgShown: MSG_PAGE, range: null, selected: null, follow: true,
  keys: {}, links: [],
  // Terminals show one member at a time unless the grid was chosen; the
  // member shown is remembered per team.
  termLayout: stored(TERM_LAYOUT_KEY) === "grid" ? "grid" : "single", termAgent: {},
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
  // Hooks report what a running agent does; without them only liveness is known.
  if (a.alive === true && a.activity === "blocked") return ["attention", "asking the user"];
  if (a.alive === true && a.activity === "waiting") return ["alive", "waiting for messages"];
  if (a.alive === true && a.activity === "working") return ["alive", "working"];
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

/* ---------------- rail: teams and members, operated like dsh's Workspaces ---------------- */
const MEMBER_PAGE = 12;
const RAIL_PREFS = "team-worker.rail.prefs";
const rail = (() => {
  let saved = {};
  try { saved = JSON.parse(stored(RAIL_PREFS) || "{}"); } catch { saved = {}; }
  return {
    sort: saved.sort === "manual" ? "manual" : "updated",
    order: Array.isArray(saved.order) ? saved.order : [],
    pins: new Set(Array.isArray(saved.pins) ? saved.pins : []),
    folded: new Set(Array.isArray(saved.folded) ? saved.folded : []),
    shown: {}, query: "", searching: false, menu: null, drag: null,
  };
})();
function saveRail() {
  store(RAIL_PREFS, JSON.stringify({ sort: rail.sort, order: rail.order, pins: [...rail.pins], folded: [...rail.folded] }));
}
function icon(paths) {
  const s = svg("svg", { width: 14, height: 14, viewBox: "0 0 16 16", "aria-hidden": "true", fill: "none", stroke: "currentColor", "stroke-width": 1.4 });
  for (const d of paths) s.append(svg("path", { d }));
  return s;
}
const ICONS = {
  search: ["M7 12.5a5.5 5.5 0 1 0 0-11 5.5 5.5 0 0 0 0 11Z", "m11 11 3.5 3.5"],
  team: ["M6 7.5a2.5 2.5 0 1 0 0-5 2.5 2.5 0 0 0 0 5Z", "M1.5 13.5c0-2.5 2-4 4.5-4s4.5 1.5 4.5 4", "M10.5 2.7a2.5 2.5 0 0 1 0 4.6", "M12 9.8c1.5.5 2.5 1.8 2.5 3.7"],
  view: ["M2 4h12", "M4.5 8h7", "M6.5 12h3"],
  more: ["M3.5 8h.01", "M8 8h.01", "M12.5 8h.01"],
  copy: ["M5.5 5.5V3.5a1 1 0 0 1 1-1h6a1 1 0 0 1 1 1v6a1 1 0 0 1-1 1h-2", "M2.5 6.5a1 1 0 0 1 1-1h6a1 1 0 0 1 1 1v6a1 1 0 0 1-1 1h-6a1 1 0 0 1-1-1Z"],
  check: ["m3 8.5 3 3 7-7"],
  doc: ["M9 2H4.5a1 1 0 0 0-1 1v10a1 1 0 0 0 1 1h7a1 1 0 0 0 1-1V5.5Z", "M9 2v3.5h3.5", "M5.5 8.5h5", "M5.5 11h3"],
  pin: ["M9.5 2.5 13.5 6.5", "M11.5 4.5 8 8l-3 .5 2.5 2.5L8 14l3.5-3.5", "m5.5 10.5-3 3"],
  external: ["M9 2.5h4.5V7", "M13.5 2.5 7.5 8.5", "M12 9.5V13a1 1 0 0 1-1 1H3.5a1 1 0 0 1-1-1V5.5a1 1 0 0 1 1-1H7"],
  key: ["M5 13.5a2.5 2.5 0 1 0 0-5 2.5 2.5 0 0 0 0 5Z", "M6.8 9.2 13.5 2.5", "m11.5 4.5 1.5 1.5", "m9.5 6.5 1.5 1.5"],
  signOut: ["M6 13.5H3.5a1 1 0 0 1-1-1v-9a1 1 0 0 1 1-1H6", "M10.5 11 13.5 8l-3-3", "M13.5 8H6"],
};
function ago(ms) {
  if (!ms) return "";
  const s = Math.max(0, (Date.now() - ms) / 1000);
  if (s < 60) return "now";
  if (s < 3600) return Math.floor(s / 60) + "m";
  if (s < 86400) return Math.floor(s / 3600) + "h";
  return Math.floor(s / 86400) + "d";
}
// teamActivity is the latest task update or message, the team's "last updated".
function teamActivity(t, b) {
  let at = Date.parse(t.createdAt || "") || 0;
  if (!b) return at;
  for (const task of b.tasks) at = Math.max(at, Date.parse(task.updatedAt || "") || 0);
  for (const m of b.messages) at = Math.max(at, Date.parse(m.timestamp || "") || 0);
  return at;
}
function memberActivity(b, name) {
  let at = 0;
  for (const m of b.messages) if (m.from === name) at = Math.max(at, Date.parse(m.timestamp || "") || 0);
  return at;
}
function teamTone(s) {
  if (!s || !s.total) return "";
  if (s.blocked) return "blocked";
  if (s.completed === s.total) return "done";
  return s.in_progress ? "progress" : "";
}
// orderedTeams puts pinned teams first, then sorts by activity or by the saved manual order.
function orderedTeams() {
  const rows = state.teams.map((t) => ({ t, b: state.boards[t.name], at: teamActivity(t, state.boards[t.name]) }));
  const pos = (name) => { const i = rail.order.indexOf(name); return i < 0 ? Infinity : i; };
  return rows.sort((x, y) => (rail.pins.has(y.t.name) - rail.pins.has(x.t.name))
    || (rail.sort === "manual" ? pos(x.t.name) - pos(y.t.name) || y.at - x.at : y.at - x.at));
}
function copyText(text) {
  navigator.clipboard?.writeText(text).catch(() => {});
  closeMenu();
}
function closeMenu() {
  if (!rail.menu) return;
  rail.menu = null;
  $("rail-menu")?.remove();
}
// openMenu shows a "..." menu below its button; items are [label, action] pairs.
function openMenu(button, key, items) {
  const again = rail.menu === key;
  closeMenu();
  if (again) return;
  rail.menu = key;
  const r = button.getBoundingClientRect();
  const menu = el("div", { class: "rail-menu", id: "rail-menu", role: "menu", style: `top:${r.bottom + 4}px;left:${Math.max(8, r.right - 190)}px` },
    items.map(([label, action]) => el("button", { type: "button", role: "menuitem", onclick: () => { closeMenu(); action(); } }, label)));
  document.body.append(menu);
  menu.querySelector("button")?.focus();
}
document.addEventListener("pointerdown", (e) => {
  if (rail.menu && !e.target.closest?.(".rail-menu, .menu-trigger")) closeMenu();
});

function renderRailTools() {
  const tools = $("rail-tools");
  if (!changed("rail-tools", [rail.searching, rail.sort, state.railOpen])) return;
  if (!state.railOpen) { tools.replaceChildren(); return; }
  if (rail.searching) {
    const input = el("input", { type: "search", id: "rail-search", value: rail.query, placeholder: "Search teams and members",
      "aria-label": "Search teams and members", autocomplete: "off",
      oninput: (e) => { rail.query = e.target.value; renderRail(); },
      onkeydown: (e) => {
        if (e.key === "Escape") { endSearch(); }
        else if (e.key === "Enter") { $("teams").querySelector(".result")?.click(); }
      } });
    tools.replaceChildren(el("div", { class: "rail-search" }, input,
      el("button", { type: "button", class: "icon-btn", "aria-label": "Close search", title: "Close search", onclick: endSearch }, "✕")));
    input.focus();
    return;
  }
  tools.replaceChildren(el("div", { class: "rail-section" },
    el("span", { class: "rail-title" }, "Teams"),
    el("button", { type: "button", class: "icon-btn", title: "Search teams and members (Ctrl+K)", "aria-label": "Search teams and members",
      "aria-keyshortcuts": "Control+K Meta+K", onclick: startSearch }, icon(ICONS.search)),
    el("button", { type: "button", class: "icon-btn menu-trigger", title: "View options", "aria-label": "View options", "aria-haspopup": "menu",
      onclick: (e) => openMenu(e.currentTarget, "view", [
        [(rail.sort === "updated" ? "✓ " : "  ") + "Sort by last updated", () => { rail.sort = "updated"; saveRail(); renderRail(); }],
        [(rail.sort === "manual" ? "✓ " : "  ") + "Sort manually (drag teams)", () => { freezeOrder(); rail.sort = "manual"; saveRail(); renderRail(); }],
      ]) }, icon(ICONS.view))));
}
function startSearch() {
  if (!state.railOpen) setRail(true);
  rail.searching = true;
  renderRail();
}
function endSearch() {
  rail.searching = false;
  rail.query = "";
  renderRail();
}
// freezeOrder records the order on screen, so manual sorting starts from it.
function freezeOrder() { rail.order = orderedTeams().map((r) => r.t.name); }

function renderSearchResults() {
  const q = rail.query.trim().toLowerCase();
  const results = [];
  for (const { t, b } of orderedTeams()) {
    if ([t.name, t.description].some((v) => v && v.toLowerCase().includes(q))) {
      results.push(el("li", {}, el("button", { type: "button", class: "result", onclick: () => { endSearch(); selectTeam(t.name); } },
        el("span", { class: "result-name" }, t.name), el("span", { class: "result-meta" }, "team"))));
    }
    for (const m of b ? b.members : []) {
      if (m.name.toLowerCase().includes(q)) {
        results.push(el("li", {}, el("button", { type: "button", class: "result", onclick: () => { endSearch(); showTerminal(t.name, m.name); } },
          el("span", { class: "dot " + agentState(m)[0], "aria-hidden": "true" }),
          el("span", { class: "result-name" }, m.name), el("span", { class: "result-meta" }, "in " + t.name))));
      }
    }
  }
  $("teams").replaceChildren(...(results.length ? results.slice(0, 20)
    : [el("li", { class: "empty muted" }, `No team or member matches "${rail.query.trim()}"`)]));
}

function renderMiniRail(rows) {
  $("teams").replaceChildren(el("li", {}, el("ul", { class: "mini" }, rows.map(({ t, b }) => {
    const s = b && b.taskSummary;
    const facts = s && s.total ? `, ${s.completed} of ${s.total} done${s.blocked ? `, ${s.blocked} blocked` : ""}` : "";
    return el("li", {}, el("button", { type: "button", title: t.name + facts, "aria-label": t.name + facts,
      "aria-current": t.name === state.team ? "page" : null, onclick: () => selectTeam(t.name) },
      [...t.name][0].toUpperCase(), teamTone(s) ? el("span", { class: "dot " + teamTone(s), "aria-hidden": "true" }) : null));
  }))));
}
function setRail(open) {
  state.railOpen = open;
  store(RAIL_KEY, open ? "open" : "closed");
  document.body.classList.toggle("rail-collapsed", !open);
  const button = $("rail-toggle");
  button.setAttribute("aria-expanded", String(open));
  button.setAttribute("aria-label", open ? "Collapse sidebar" : "Expand sidebar");
  button.title = button.getAttribute("aria-label");
  renderRail();
}

function memberRows(t, b) {
  const members = b.members;
  const limit = rail.shown[t.name] || MEMBER_PAGE;
  const rows = members.slice(0, limit).map((m) => {
    const [cls, label] = agentState(m);
    const leader = m.agentId === b.team.leadAgentId;
    const at = memberActivity(b, m.name);
    const when = at ? "Last message " + new Date(at).toLocaleString() : "";
    // One line per member, as the sub-rows under Magpie's open page: the
    // state as a dot, unread messages or the last activity at the end.
    return el("li", { class: "person-row" },
      el("button", { type: "button", class: "person", onclick: () => showTerminal(t.name, m.name),
        title: [`${m.name}: ${leader ? "leader, " : ""}${label}`, m.agent ? "Show the terminal" : "", when].filter(Boolean).join("\n") },
        el("span", { class: "dot " + cls, "aria-hidden": "true" }),
        el("span", { class: "pname" }, m.name),
        leader && m.name !== "leader" ? el("span", { class: "lead" }, "leader") : null,
        m.inboxCount ? el("em", { class: "up", title: `${m.inboxCount} unread messages` }, String(m.inboxCount))
          : el("em", {}, ago(at))),
      el("button", { type: "button", class: "row-more menu-trigger", "aria-label": `${m.name} actions`, "aria-haspopup": "menu",
        onclick: (e) => {
          // The menu is removed before its action runs, so keep the trigger
          // button to restore focus to after the prompt dialog closes.
          const trigger = e.currentTarget;
          openMenu(trigger, "m:" + t.name + "/" + m.name, [
            ["Show terminal", () => showTerminal(t.name, m.name)],
            ["Show prompt", () => openMemberPrompt(t.name, m.name, m.user, trigger)],
            ["Copy name", () => copyText(m.name)],
            ["Copy agent ID", () => copyText(m.agentId)],
          ]);
        } }, icon(ICONS.more)));
  });
  const rest = members.length - limit;
  if (rest > 0) {
    rows.push(el("li", {}, el("button", { type: "button", class: "show-more",
      onclick: () => { rail.shown[t.name] = limit + MEMBER_PAGE; renderRail(); } }, rest <= MEMBER_PAGE ? `Show ${rest} more` : "Show more")));
  } else if (limit > MEMBER_PAGE) {
    rows.push(el("li", {}, el("button", { type: "button", class: "show-more",
      onclick: () => { delete rail.shown[t.name]; renderRail(); } }, "Show less")));
  }
  return rows;
}

function teamRow({ t, b, at }) {
  const s = b && b.taskSummary;
  const current = t.name === state.team;
  const members = b ? b.members : [];
  // As in Magpie's sidebar, only the open team shows its members, under it;
  // clicking the open team folds or unfolds them.
  const open = current && !rail.folded.has(t.name);
  const running = members.filter((m) => ["alive", "attention", "remote"].includes(agentState(m)[0])).length;
  const pinned = rail.pins.has(t.name);
  const tone = teamTone(s);
  const fold = () => { rail.folded.has(t.name) ? rail.folded.delete(t.name) : rail.folded.add(t.name); delete rail.shown[t.name]; saveRail(); renderRail(); };
  const li = el("li", { class: "team" + (current ? " current" : "") + (rail.drag === t.name ? " dragging" : ""), draggable: "true",
    "data-team": t.name },
    el("div", { class: "team-row" },
      el("button", { type: "button", class: "team-tab", "aria-current": current ? "page" : null,
        "aria-expanded": current ? String(open) : null, "aria-controls": open ? "people-" + t.name : null,
        onclick: () => current ? fold() : selectTeam(t.name),
        title: [t.description, s && s.total ? `${s.completed} of ${s.total} tasks done${s.blocked ? `, ${s.blocked} blocked` : ""}` : "No tasks",
          running ? `${running} of ${members.length} members running` : `${t.memberCount} members`,
          at ? "Updated " + new Date(at).toLocaleString() : ""].filter(Boolean).join("\n") },
        icon(ICONS.team),
        el("span", { class: "name" }, t.name),
        pinned ? el("span", { class: "pin", title: "Pinned" }, icon(ICONS.pin)) : null,
        tone ? el("span", { class: "dot " + tone, "aria-hidden": "true" }) : null,
        el("em", {}, s && s.total ? `${s.completed}/${s.total}` : "")),
      el("button", { type: "button", class: "row-more menu-trigger", "aria-label": `${t.name} actions`, "aria-haspopup": "menu",
        onclick: (e) => openMenu(e.currentTarget, "t:" + t.name, [
          [pinned ? "Unpin" : "Pin to top", () => { pinned ? rail.pins.delete(t.name) : rail.pins.add(t.name); saveRail(); renderRail(); }],
          ["Open board", () => { selectTeam(t.name); setView("board"); }],
          ["Open messages", () => { selectTeam(t.name); setView("messages"); }],
          ["Open terminals", () => { selectTeam(t.name); setView("terminals"); }],
          ...(current ? [[open ? "Fold members" : "Unfold members", fold]] : []),
          ["Copy team name", () => copyText(t.name)],
        ]) }, icon(ICONS.more))),
    open && b && members.length ? el("ul", { class: "people", id: "people-" + t.name, "aria-label": t.name + " members" }, memberRows(t, b)) : null);
  // Dragging a team reorders it and switches to manual sorting, as in dsh.
  li.addEventListener("dragstart", (e) => { rail.drag = t.name; e.dataTransfer.effectAllowed = "move"; e.dataTransfer.setData("text/plain", t.name); });
  li.addEventListener("dragend", () => { rail.drag = null; state.keys.rail = ""; renderRail(); });
  li.addEventListener("dragover", (e) => { if (rail.drag && rail.drag !== t.name) { e.preventDefault(); li.classList.add("drop-target"); } });
  li.addEventListener("dragleave", () => li.classList.remove("drop-target"));
  li.addEventListener("drop", (e) => {
    e.preventDefault();
    const from = rail.drag;
    if (!from || from === t.name) return;
    freezeOrder();
    rail.order = rail.order.filter((n) => n !== from);
    rail.order.splice(rail.order.indexOf(t.name), 0, from);
    rail.sort = "manual";
    rail.drag = null;
    saveRail();
    renderRail();
  });
  return li;
}

function renderRail() {
  renderRailTools();
  const rows = orderedTeams();
  const minute = Math.floor(Date.now() / 60000);
  if (!changed("rail", [rows.map((r) => [r.t, r.b && [r.b.taskSummary, r.b.members], r.at]), state.team, state.railOpen,
    rail.sort, [...rail.pins], [...rail.folded], rail.shown, rail.query, rail.searching, minute])) return;
  closeMenu();
  if (rows.length && !state.railOpen) { renderMiniRail(rows); return; }
  if (!rows.length) { $("teams").replaceChildren(el("li", { class: "empty muted" }, "No teams yet")); return; }
  if (rail.searching && rail.query.trim()) { renderSearchResults(); return; }
  $("teams").replaceChildren(...rows.map(teamRow));
}
document.addEventListener("keydown", (e) => {
  if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "k" && !e.target.closest?.(".term-host")) {
    e.preventDefault();
    startSearch();
  } else if (e.key === "Escape") closeMenu();
});

/* ---------------- skeleton ---------------- */
function buildMain() {
  state.keys = {};
  const tab = (view, label) => el("button", { type: "button", role: "tab", id: "tab-" + view, "aria-controls": "view-" + view,
    "aria-selected": String(state.view === view), onclick: () => setView(view) }, label);
  $("main").replaceChildren(
    el("div", { class: "topbar" }, el("nav", { class: "links", id: "links", "aria-label": "Other services" })),
    el("div", { class: "head-row" }, el("header", { class: "head", id: "head" }),
      el("div", { class: "views", role: "tablist", "aria-label": "View" }, VIEWS.map(([v, label]) => tab(v, label)))),
    el("div", { class: "view", id: "view-board", role: "tabpanel", "aria-labelledby": "tab-board", hidden: state.view !== "board" },
    el("section", { class: "tasks", "aria-label": "Tasks" },
      el("div", { class: "harness", id: "harness", hidden: true }),
      el("div", { class: "stats", id: "stats", role: "list", "aria-label": "Team summary" }),
      el("div", { class: "board", id: "board" }))),
    el("div", { class: "view", id: "view-results", role: "tabpanel", "aria-labelledby": "tab-results", hidden: state.view !== "results" },
      el("section", { class: "results", id: "results", "aria-label": "Agent results" })),
    el("div", { class: "view", id: "view-messages", role: "tabpanel", "aria-labelledby": "tab-messages", hidden: state.view !== "messages" },
    el("section", { class: "traj", "aria-label": "Messages" },
      el("div", { class: "traj-head" }, el("h2", {}, "Messages"),
        el("button", { type: "button", class: "quiet-btn", id: "clear-range", hidden: true, onclick: () => setRange(null) }),
        el("span", { class: "count muted", id: "msg-count" })),
      el("div", { class: "overview", id: "overview" }),
      el("div", { class: "traj-body" },
        el("div", { class: "ledger", id: "ledger", tabindex: "0", role: "listbox", "aria-label": "Message ledger",
          onscroll: (e) => { state.follow = e.target.scrollTop <= 8; },
          onkeydown: ledgerKeys }),
        el("aside", { class: "inspector", id: "inspector", "aria-live": "polite" })))),
    el("div", { class: "view", id: "view-approvals", role: "tabpanel", "aria-labelledby": "tab-approvals", hidden: state.view !== "approvals" },
      el("div", { class: "approvals", id: "approvals" })),
    el("div", { class: "view", id: "view-terminals", role: "tabpanel", "aria-labelledby": "tab-terminals", hidden: state.view !== "terminals" },
      el("div", { class: "term-bar", id: "term-bar" }),
      el("div", { class: "terms", id: "terms" })));
  bindOverview();
  renderLinks();
}

/* ---------------- links to other services (board serve --link) ---------------- */
function renderLinks() {
  const nav = $("links");
  if (!nav) return;
  const label = (name, text) => [icon(ICONS[name]), el("span", {}, text)];
  nav.replaceChildren(...state.links.map((l) => el("a", { class: "tool-btn", href: l.url, target: "_blank", rel: "noopener",
    title: "Open " + l.name + " in a new tab" }, ...label("external", l.name))));
  nav.append(el("button", { type: "button", class: "tool-btn", onclick: (event) => openServiceKeys(event.currentTarget) },
    ...label("key", "Service keys")),
    el("form", { method: "post", action: "logout" }, el("button", { type: "submit", class: "tool-btn" }, ...label("signOut", "Sign out"))));
}

/* ---------------- header + tasks ---------------- */
function renderHead(board) {
  if (!changed("head", board.team)) return;
  const name = board.team.name;
  // The description is the name's tooltip; icons after the name copy it and
  // open the team prompt.
  const copy = el("button", { type: "button", class: "icon-btn copy-name", title: "Copy team name", "aria-label": `Copy ${name}`,
    onclick: (e) => {
      const button = e.currentTarget;
      copyText(name);
      button.replaceChildren(icon(ICONS.check));
      setTimeout(() => button.replaceChildren(icon(ICONS.copy)), 1200);
    } }, icon(ICONS.copy));
  const prompt = el("button", { type: "button", class: "icon-btn copy-name", title: "Show the team prompt (shared instructions)",
    "aria-label": `Show ${name}'s team prompt`, onclick: (e) => openTeamPrompt(name, e.currentTarget) }, icon(ICONS.doc));
  $("head").replaceChildren(el("div", { class: "title-line" }, el("h1", { title: board.team.description || "" }, name), copy, prompt));
}
function renderStats(board) {
  const s = board.taskSummary;
  const members = board.members;
  const running = members.filter((m) => ["alive", "attention", "remote"].includes(agentState(m)[0])).length;
  const unread = members.reduce((n, m) => n + (m.inboxCount || 0), 0);
  const ov = state.oversight[state.team];
  if (!changed("stats", [s, running, members.length, unread, ov && [ov.budget, ov.cost]])) return;
  const pct = s.total ? Math.round((s.completed / s.total) * 100) : 0;
  const stat = (tone, label, value, note, extra) => el("div", { class: "stat" + (tone ? " " + tone : ""), role: "listitem" },
    el("div", { class: "stat-label" }, label), el("div", { class: "stat-value" }, value),
    note ? el("div", { class: "stat-note" }, note) : null, extra || null);
  $("stats").replaceChildren(
    stat("done", "Done", s.total ? `${pct}%` : "–", s.total ? `${s.completed} of ${s.total} tasks` : "No tasks yet",
      el("div", { class: "stat-bar", "aria-hidden": "true" }, el("span", { style: `width:${pct}%` }))),
    stat("progress", "In progress", String(s.in_progress), s.in_progress ? "being worked on" : "nobody is working"),
    stat(s.blocked ? "blocked" : "", "Blocked", String(s.blocked), s.blocked ? "waiting on other tasks" : "nothing is stuck"),
    stat("", "Pending", String(s.pending), s.pending ? "ready to pick up" : "queue is empty"),
    stat("", "Time per task", s.avg_duration_seconds ? duration(s.avg_duration_seconds) : "–",
      s.timed_completed ? `average of ${s.timed_completed} timed` : "no finished timings"),
    stat(running ? "done" : "", "Agents online", `${running}/${members.length}`, running === members.length ? "all running" : `${members.length - running} not running`),
    stat(unread ? "blocked" : "", "Unread", String(unread), unread ? "messages waiting in inboxes" : "inboxes are clear"),
    // replaceChildren would print a null as text; spend waits for oversight.
    ...(ov ? [spendStat(stat, ov)] : []));
}
// spendStat shows cost when agents reported it, else the tokens captured from
// their sessions, against the budget.
function spendStat(stat, ov) {
  const { budget, cost } = ov;
  const dollars = (cents) => "$" + (cents / 100).toFixed(cents >= 10000 ? 0 : 2);
  const tokens = cost.totalInputTokens + cost.totalOutputTokens;
  const value = cost.totalCostCents ? dollars(cost.totalCostCents) : tokens ? compact(tokens) + " tok" : "–";
  let note = budget.budgetCents ? `of ${dollars(budget.budgetCents)} budget${budget.enforce ? ", enforced" : ""}` : "no budget set";
  if (budget.exhausted) note = budget.enforce ? "budget spent: new work is blocked" : "budget spent";
  const pct = budget.budgetCents ? Math.min(100, Math.round((budget.spentCents / budget.budgetCents) * 100)) : 0;
  return stat(budget.exhausted ? "blocked" : "", "Spend", value, note,
    budget.budgetCents ? el("div", { class: "stat-bar" + (budget.exhausted ? " over" : ""), "aria-hidden": "true" }, el("span", { style: `width:${pct}%` })) : null);
}
function compact(n) {
  if (n >= 1e6) return (n / 1e6).toFixed(1) + "M";
  if (n >= 1e3) return Math.round(n / 1e3) + "k";
  return String(n);
}

/* ---------------- harness: the team's phased run ---------------- */
const PHASES = ["discuss", "plan", "execute", "verify", "ship"];
function renderHarness() {
  const box = $("harness");
  const h = state.oversight[state.team]?.harness;
  if (!changed("harness", h || null)) return;
  box.hidden = !h;
  if (!h) return;
  const at = PHASES.indexOf(h.phase);
  const finished = h.status === "completed";
  const steps = PHASES.map((p, i) => el("li", {
    class: "phase " + (finished || i < at ? "done" : i === at ? "current " + h.status : "todo"),
    "aria-current": i === at && !finished ? "step" : null }, p));
  const waiting = h.status === "running" && !h.can_advance && h.gate_reason;
  box.replaceChildren(
    el("div", { class: "harness-head" },
      el("span", { class: "harness-label" }, "Harness"),
      el("span", { class: "harness-goal", title: h.goal }, h.goal),
      el("span", { class: "chip " + (h.status === "running" ? "pending" : h.status === "completed" ? "approved" : "rejected") }, h.status),
      h.attempt > 1 ? el("span", { class: "harness-meta" }, `attempt ${h.attempt} of ${h.max_attempts}`) : null),
    el("ol", { class: "phases", "aria-label": "Phases" }, steps),
    waiting ? el("p", { class: "harness-gate" }, "Waiting for: " + h.gate_reason)
      : h.status === "running" && h.can_advance ? el("p", { class: "harness-gate ready" }, "Ready to move on; the conductor advances it on its next check.") : null);
}

/* ---------------- approvals: decision requests, plans and join requests ---------------- */
const STATUS_ORDER = { pending: 0, approved: 1, rejected: 2, answered: 3 };
const APPROVAL_TABS = [["requests", "Requests"], ["plans", "Plans"], ["joins", "Join requests"]];
const pendingOf = (list) => (list || []).filter((x) => x.status === "pending").length;
function pendingCount(ov) {
  return ov ? pendingOf(ov.requests) + pendingOf(ov.plans) + pendingOf(ov.joinRequests) : 0;
}
function renderApprovalsTab() {
  const n = pendingCount(state.oversight[state.team]);
  const tab = $("tab-approvals");
  if (tab) tab.textContent = n ? `Approvals ${n}` : "Approvals";
}
async function send(method, path, body) {
  const res = await fetch(path, { method, headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
  const out = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(out.error || res.statusText);
  return out;
}
// rerenderApprovals redraws the view now, e.g. after a click changed its state.
function rerenderApprovals() {
  delete state.keys.approvals;
  if (state.view === "approvals") renderApprovals();
}
function renderApprovals() {
  const ov = state.oversight[state.team];
  const team = state.team;
  const box = $("approvals");
  // Redrawing would take the focus and caret from a note being typed; the
  // next refresh draws it once the field is left.
  if (box.contains(document.activeElement) && document.activeElement.matches("textarea, input[type=text], input[type=time]")) return;
  const rq = state.req;
  if (!changed("approvals", [team, ov && [ov.requests, ov.autoReview, ov.plans, ov.joinRequests], [...state.openPlans],
    state.approvalsTab, [...rq.open], Object.entries(rq.form).map(([id, f]) => [id, f.mode, f.choice]), [...rq.busy],
    rq.errors, [...rq.overriding], rq.autoBusy, rq.autoError])) return;
  if (!ov) { box.replaceChildren(el("p", { class: "muted" }, "Loading approvals…")); return; }
  const requests = ov.requests || [];
  const counts = { requests: pendingOf(requests), plans: pendingOf(ov.plans), joins: pendingOf(ov.joinRequests) };
  const tabs = el("div", { class: "views approval-tabs", role: "tablist", "aria-label": "Approvals" },
    APPROVAL_TABS.map(([v, label]) => el("button", { type: "button", role: "tab", "aria-selected": String(state.approvalsTab === v),
      onclick: () => { state.approvalsTab = v; rerenderApprovals(); } }, label, counts[v] ? el("em", {}, String(counts[v])) : null)));
  let panel;
  if (state.approvalsTab === "requests") panel = renderRequests(team, ov, requests);
  else {
    const byStatus = (a, b) => (STATUS_ORDER[a.status] - STATUS_ORDER[b.status]) || (b.createdAt || "").localeCompare(a.createdAt || "");
    const when = (ts) => ts ? new Date(ts).toLocaleString() : "";
    const plans = [...ov.plans].sort(byStatus).map((p) => {
      const card = el("details", { class: "approval " + p.status, open: state.openPlans.has(p.id) },
        el("summary", {},
          el("span", { class: "chip " + p.status }, p.status),
          el("span", { class: "approval-title" }, p.summary || p.plan.split("\n")[0]),
          el("span", { class: "approval-meta" }, `${p.agent}, ${when(p.createdAt)}`)),
        el("div", { class: "approval-body" },
          el("pre", { class: "plan-text" }, p.plan),
          p.feedback ? el("p", { class: "feedback" }, `${p.status === "rejected" ? "Rejected" : "Approved"} by ${p.decidedBy || "the leader"}: ${p.feedback}`)
            : p.decidedBy ? el("p", { class: "feedback" }, `${p.status === "rejected" ? "Rejected" : "Approved"} by ${p.decidedBy} ${when(p.decidedAt)}`) : null,
          p.status === "pending" ? el("p", { class: "hint" }, "Decide from the leader's terminal: ",
            el("code", {}, `team-worker plan approve ${team} ${p.id} ${p.agent}`), " or ",
            el("code", {}, `team-worker plan reject ${team} ${p.id} ${p.agent} -f "<why>"`)) : null));
      card.addEventListener("toggle", () => card.open ? state.openPlans.add(p.id) : state.openPlans.delete(p.id));
      return card;
    });
    const joins = [...ov.joinRequests].sort(byStatus).map((j) => el("div", { class: "approval " + j.status },
      el("div", { class: "join-line" },
        el("span", { class: "chip " + j.status }, j.status),
        el("span", { class: "approval-title" }, j.assignedName && j.assignedName !== j.proposedName ? `${j.proposedName} → ${j.assignedName}` : j.proposedName),
        el("span", { class: "approval-meta" }, when(j.createdAt))),
      j.capabilities ? el("p", { class: "join-caps" }, j.capabilities) : null,
      j.reason ? el("p", { class: "feedback" }, "Reason: " + j.reason) : null,
      j.status === "pending" ? el("p", { class: "hint" }, el("code", {}, `team-worker team approve-join ${team} ${j.requestId}`), " or ",
        el("code", {}, `team-worker team reject-join ${team} ${j.requestId} -r "<why>"`)) : null));
    panel = state.approvalsTab === "plans"
      ? el("section", { class: "approval-group", "aria-label": "Plans" },
        el("h2", {}, "Plans", el("span", { class: "n" }, String(ov.plans.length))),
        plans.length ? plans : el("p", { class: "muted" }, "No plans yet. Members started with --plan-required submit one before they start work."))
      : el("section", { class: "approval-group", "aria-label": "Join requests" },
        el("h2", {}, "Join requests", el("span", { class: "n" }, String(ov.joinRequests.length))),
        joins.length ? joins : el("p", { class: "muted" }, "No join requests. Agents ask to join with team-worker team request-join."));
  }
  box.replaceChildren(tabs, panel);
}

/* decision requests: the leader asks the operator; the board decides as the operator */
const when = (ts) => ts ? new Date(ts).toLocaleString() : "";
const isAuto = (by) => by === "auto" || (by || "").startsWith("auto:");
function renderRequests(team, ov, requests) {
  const sorted = [...requests].sort((a, b) => ((a.status === "pending" ? 0 : 1) - (b.status === "pending" ? 0 : 1))
    || (b.createdAt || "").localeCompare(a.createdAt || ""));
  return el("section", { class: "approval-group requests", "aria-label": "Decision requests" },
    autoReviewBox(team, ov.autoReview || { enabled: false }),
    el("h2", {}, "Decision requests", el("span", { class: "n" }, String(requests.length))),
    sorted.length ? sorted.map((r) => requestCard(team, r))
      : el("p", { class: "muted" }, "No decision requests. The leader asks you to decide with team-worker request create."));
}
function autoReviewBox(team, auto) {
  const rq = state.req;
  const saved = { from: auto.from || "", to: auto.to || "", timeZone: auto.timeZone || "" };
  // rq.autoWindow is the window being edited, until it is saved.
  const win = rq.autoWindow || saved;
  const sameWindow = (a, b) => a.from === b.from && a.to === b.to && a.timeZone === b.timeZone;
  const save = async (next, savesWindow) => {
    rq.autoBusy = true; rq.autoError = "";
    rerenderApprovals();
    try {
      state.oversight[team].autoReview = await send("PUT", "api/teams/" + encodeURIComponent(team) + "/auto-review", next);
      if (savesWindow) rq.autoWindow = null;
    } catch (e) { rq.autoError = "Cannot change auto review: " + e.message; }
    rq.autoBusy = false;
    rerenderApprovals();
    refreshOversight();
  };
  // Every save sends the whole setting: the saved window unless it is the
  // window being saved.
  const setting = (over) => ({ enabled: auto.enabled, model: auto.model || "", includeHuman: !!auto.includeHuman, ...saved, ...over });
  const model = el("input", { type: "text", class: "auto-model", placeholder: "default model", value: auto.model || "",
    "aria-label": "Model for auto review", disabled: rq.autoBusy, spellcheck: "false",
    onkeydown: (e) => { if (e.key === "Enter") e.target.blur(); },
    onchange: (e) => save(setting({ model: e.target.value.trim() })) });
  const saveWindow = () => save(setting({ model: model.value.trim(), ...(rq.autoWindow || saved) }), true);
  const saveBtn = el("button", { type: "button", class: "quiet-btn", disabled: rq.autoBusy || sameWindow(win, saved),
    onclick: saveWindow }, "Save window");
  const edit = (key) => (e) => {
    rq.autoWindow = { ...(rq.autoWindow || saved), [key]: e.target.value.trim() };
    saveBtn.disabled = rq.autoBusy || sameWindow(rq.autoWindow, saved);
  };
  const time = (key, label) => el("label", { class: "auto-time" }, el("span", {}, label),
    el("input", { type: "time", value: win[key], disabled: rq.autoBusy, "aria-label": label + " of the auto review window", oninput: edit(key) }));
  const zone = el("input", { type: "text", class: "auto-model auto-zone", value: win.timeZone, disabled: rq.autoBusy, spellcheck: "false",
    placeholder: auto.daemonZone ? `daemon time (${auto.daemonZone})` : "daemon local time", "aria-label": "Time zone of the auto review window",
    oninput: edit("timeZone"), onkeydown: (e) => { if (e.key === "Enter" && !saveBtn.disabled) saveWindow(); } });
  const status = auto.active ? el("span", { class: "auto-state on" }, "Active now")
    : auto.enabled && auto.from ? el("span", { class: "auto-state" }, `Outside the window (next starts ${auto.from})`) : null;
  return el("div", { class: "auto-review" + (auto.enabled ? " on" : "") },
    el("label", { class: "auto-toggle" },
      el("input", { type: "checkbox", checked: auto.enabled, disabled: rq.autoBusy,
        onchange: (e) => save(setting({ enabled: e.target.checked, model: model.value.trim() })) }),
      el("span", {}, "Auto review (AI)")),
    model,
    status,
    el("div", { class: "auto-window", role: "group", "aria-label": "Daily auto review window" },
      time("from", "From"), time("to", "To"), zone, saveBtn),
    el("p", { class: "hint" }, "Leave both empty to review all day; an end before the start crosses midnight. "
      + "Requests made outside the window wait until it opens."),
    el("p", { class: "hint" }, "When on, an AI model decides pending requests for you and escalates risky ones (destructive, "
      + "outside the team instructions, cost, not enough information). Each request is reviewed once; you can override its decision. "
      + "Requests marked human only are left for a person unless the option below is on."),
    el("label", { class: "auto-toggle auto-human" },
      el("input", { type: "checkbox", checked: !!auto.includeHuman, disabled: rq.autoBusy,
        onchange: (e) => save(setting({ includeHuman: e.target.checked, model: model.value.trim() })) }),
      el("span", {}, "Also review human-only requests")),
    el("p", { class: "hint" }, "The AI may only approve them or leave them to you; destructive or unauthorized actions are still escalated."),
    rq.autoError ? el("p", { class: "notice error" }, rq.autoError) : null);
}
function decisionText(r) {
  const label = (r.options || []).find((o) => o.key === r.choice)?.label;
  const what = r.status === "approved" ? (r.choice ? `Approved ${r.choice}${label ? ` (${label})` : ""}` : "Approved")
    : r.status === "rejected" ? "Rejected" : "Answered";
  return `${what} by ${isAuto(r.decidedBy) ? r.decidedBy + " (AI)" : r.decidedBy}, ${when(r.decidedAt)}`;
}
function requestCard(team, r) {
  const rq = state.req;
  const pending = r.status === "pending";
  const acting = pending || rq.overriding.has(r.id);
  const replyLabel = r.status === "rejected" ? "Reason" : r.status === "answered" ? "Answer" : "Note";
  const details = r.body ? el("details", { class: "request-body", open: rq.open.has(r.id) },
    el("summary", {}, "Details"), el("pre", { class: "plan-text" }, r.body)) : null;
  details?.addEventListener("toggle", () => details.open ? rq.open.add(r.id) : rq.open.delete(r.id));
  return el("div", { class: "approval request " + r.status },
    el("div", { class: "join-line" },
      el("span", { class: "chip " + r.status }, r.status),
      r.humanOnly ? el("span", { class: "chip human", title: "The requester marked this request for a person" }, "human only") : null,
      el("span", { class: "approval-title", title: r.summary }, r.summary),
      el("span", { class: "approval-meta" }, `${r.from}, ${when(r.createdAt)}`)),
    details,
    !pending ? el("p", { class: "feedback" }, decisionText(r)) : null,
    !pending && r.reply ? el("p", { class: "feedback" }, `${replyLabel}: ${r.reply}`) : null,
    !pending && r.autoReason ? el("p", { class: "feedback ai" }, (isAuto(r.decidedBy) ? "AI reason: " : "Earlier AI review: ") + r.autoReason) : null,
    pending && r.autoNote ? el("p", { class: "feedback ai" }, "AI did not decide: " + r.autoNote) : null,
    !pending && isAuto(r.decidedBy) ? el("div", { class: "request-actions" },
      el("button", { type: "button", class: "quiet-btn", "aria-expanded": String(rq.overriding.has(r.id)), disabled: rq.busy.has(r.id),
        onclick: () => { rq.overriding.has(r.id) ? rq.overriding.delete(r.id) : rq.overriding.add(r.id); delete rq.form[r.id]; rerenderApprovals(); } },
      rq.overriding.has(r.id) ? "Cancel override" : "Override")) : null,
    acting ? requestActions(team, r) : null,
    rq.errors[r.id] ? el("p", { class: "notice error" }, rq.errors[r.id]) : null);
}
function requestActions(team, r) {
  const rq = state.req;
  const form = rq.form[r.id] || {};
  const busy = rq.busy.has(r.id);
  const open = (mode, choice = "") => { rq.form[r.id] = { mode, choice, text: "" }; delete rq.errors[r.id]; rerenderApprovals(); };
  const options = r.options || [];
  const choose = options.length
    ? options.map((o) => el("button", { type: "button", class: "opt-btn" + (o.key === r.recommended ? " recommended" : ""),
      "aria-pressed": String(form.mode === "approve" && form.choice === o.key), disabled: busy, onclick: () => open("approve", o.key),
      title: o.key === r.recommended ? "Recommended by " + r.from : null },
      el("b", {}, o.key), el("span", {}, o.label), o.key === r.recommended ? el("em", {}, "recommended") : null))
    : [el("button", { type: "button", class: "opt-btn", "aria-pressed": String(form.mode === "approve"), disabled: busy,
      onclick: () => open("approve") }, "Approve")];
  const row = el("div", { class: "request-actions" }, choose,
    el("button", { type: "button", class: "quiet-btn", "aria-pressed": String(form.mode === "reject"), disabled: busy, onclick: () => open("reject") }, "Reject"),
    el("button", { type: "button", class: "quiet-btn", "aria-pressed": String(form.mode === "answer"), disabled: busy, onclick: () => open("answer") }, "Reply"));
  if (!form.mode) return row;
  const required = form.mode !== "approve";
  const placeholder = { approve: "Optional note for " + r.from, reject: "Why it is rejected (required)", answer: "Your answer (required)" }[form.mode];
  const verb = { approve: form.choice ? `Approve ${form.choice}` : "Approve", reject: "Reject", answer: "Send reply" }[form.mode];
  const text = el("textarea", { class: "request-text", rows: 3, placeholder, disabled: busy, "aria-label": placeholder,
    oninput: (e) => { form.text = e.target.value; } });
  text.value = form.text || "";
  const submit = async () => {
    if (required && !text.value.trim()) { rq.errors[r.id] = form.mode === "reject" ? "Give a reason for the rejection." : "Write the answer first."; rerenderApprovals(); return; }
    rq.busy.add(r.id); delete rq.errors[r.id];
    rerenderApprovals();
    try {
      await send("POST", `api/teams/${encodeURIComponent(team)}/requests/${encodeURIComponent(r.id)}/decide`,
        { action: form.mode, choice: form.choice || "", reply: text.value.trim() });
      delete rq.form[r.id];
      rq.overriding.delete(r.id);
    } catch (e) { rq.errors[r.id] = "Cannot decide: " + e.message; }
    rq.busy.delete(r.id);
    rerenderApprovals();
    refreshOversight();
  };
  return el("div", { class: "request-form" }, row, text,
    el("div", { class: "request-actions" },
      el("button", { type: "button", class: "primary-btn" + (form.mode === "reject" ? " danger" : ""), disabled: busy, onclick: submit },
        busy ? "Sending…" : verb),
      el("button", { type: "button", class: "quiet-btn", disabled: busy, onclick: () => { delete rq.form[r.id]; delete rq.errors[r.id]; rerenderApprovals(); } }, "Cancel")));
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
function resultDetails(task) {
  const r = task.result;
  const artifact = (path) => /^https?:\/\//i.test(path)
    ? el("a", { href: path, target: "_blank", rel: "noopener noreferrer" }, path)
    : el("span", {}, el("code", {}, path), " ", el("button", { type: "button", class: "quiet-btn", onclick: () => copyText(path), "aria-label": `Copy ${path}` }, "Copy"));
  return el("div", { class: "result-report" },
    el("p", { class: "muted" }, task.id),
    r ? [el("p", { class: "result-text" }, r.summary),
      el("h4", {}, "Output files"),
      r.artifacts?.length ? el("ul", {}, r.artifacts.map((path) => el("li", {}, artifact(path)))) : el("p", { class: "muted" }, "No files reported"),
      el("h4", {}, "Verification (agent reported)"), el("p", { class: "result-text" }, r.verification || "Not reported")]
      : [el("p", { class: "muted" }, "No separate result report. Task notes:"),
        el("p", { class: "result-text" }, task.description || "No task notes")]);
}
function resultDisclosure(key, attrs, summary, ...body) {
  const node = el("details", { ...attrs, open: state.resultOpen.has(key) }, summary, ...body);
  node.addEventListener("toggle", () => {
    if (node.isConnected) node.open ? state.resultOpen.add(key) : state.resultOpen.delete(key);
  });
  return node;
}
function renderResults(board) {
  if (!changed("results", [board.members, board.tasks])) return;
  const groups = new Map(board.members.map((m) => [m.name, []]));
  for (const task of board.tasks) {
    const owner = task.owner || "Unassigned";
    if (!groups.has(owner)) groups.set(owner, []);
    groups.get(owner).push(task);
  }
  const rows = [...groups].map(([name, tasks]) => {
    const completed = tasks.filter((t) => t.status === "completed")
      .sort((a, b) => (b.result?.reportedAt || b.updatedAt).localeCompare(a.result?.reportedAt || a.updatedAt));
    const latest = completed[0];
    const count = tasks.filter((t) => t.status === "in_progress").length;
    const agent = el("div", { class: "result-agent" }, el("h3", {}, name),
      el("span", { class: "muted" }, count ? `${count} in progress` : `${completed.length} completed`));
    if (!latest) return el("div", { class: "agent-results result-empty" }, agent,
      el("span", { class: "muted" }, "No completed tasks yet"));
    const at = latest.result?.reportedAt || latest.updatedAt;
    const key = JSON.stringify([board.team.name, name]);
    const history = completed.slice(1);
    const earlier = history.length ? resultDisclosure(key + ":history", { class: "result-history" },
      el("summary", {}, `Earlier completed tasks (${history.length})`),
      history.map((t) => resultDisclosure(key + ":" + t.id, {}, el("summary", {}, t.subject), resultDetails(t)))) : null;
    return resultDisclosure(key, { class: "agent-results" },
      el("summary", { class: "result-row" }, agent,
        el("div", { class: "result-latest" }, el("div", { class: "result-subject" }, latest.subject),
          latest.result?.summary ? el("p", { class: "result-preview" }, latest.result.summary) : null),
        el("time", { class: "muted result-time", datetime: at, title: (latest.result ? "Reported " : "Updated ") + new Date(at).toLocaleString() },
          new Date(at).toLocaleDateString()),
        el("span", { class: "result-chevron", "aria-hidden": "true" }, "›")),
      el("div", { class: "result-expanded" }, resultDetails(latest), earlier));
  });
  $("results").replaceChildren(el("h2", {}, "Latest completed work"),
    el("p", { class: "muted" }, "One result per agent. Expand for files, checks and earlier tasks."),
    el("div", { class: "result-groups" }, rows.length ? rows : el("p", { class: "muted" }, "No agents or tasks yet")));
}

function renderTasks(board) {
  if (!changed("tasks", [board.tasks, state.doneShown])) return;
  const scroll = {};
  $("board").querySelectorAll(".list").forEach((n) => { scroll[n.dataset.col] = n.scrollTop; });
  const byId = Object.fromEntries(board.tasks.map((t) => [t.id, t]));
  $("board").replaceChildren(...COLUMNS.map(([status, title]) => {
    const all = board.tasks.filter((t) => t.status === status);
    let tasks = sortTasks(status, all);
    let hidden = 0;
    if (status === "completed" && tasks.length > state.doneShown) {
      hidden = tasks.length - state.doneShown;
      tasks = tasks.slice(0, state.doneShown);
    }
    const items = tasks.map((t) => taskCard(t, byId));
    if (hidden) items.push(el("button", { type: "button", class: "quiet-btn",
      onclick: () => { state.doneShown += hidden; update(); } }, `Show ${hidden} earlier`));
    if (!items.length) items.push(el("div", { class: "none" }, "Nothing here"));
    return el("section", { class: "col col-" + status, "aria-label": title },
      el("h2", {}, el("span", {}, title), el("span", { class: "n" }, String(all.length))),
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
  // Newest first. While reading further down, keep the row at the top of the
  // view in place so messages arriving above it do not push it away.
  let anchor = null;
  if (!state.follow) {
    const top = ledger.getBoundingClientRect().top;
    const row = [...ledger.querySelectorAll(".row")].find((n) => n.getBoundingClientRect().bottom > top);
    if (row) anchor = { key: row.dataset.key, offset: row.getBoundingClientRect().top - top };
  }
  const rows = [];
  let prev = null;
  for (const r of [...shown].reverse()) {
    if (!prev || prev.at - r.at > GAP_MS) {
      rows.push(el("div", { class: "gap", role: "presentation" },
        prev ? `${shortClock(r.at)}, ${Math.round((prev.at - r.at) / 60000)} min earlier` : new Date(r.at).toLocaleDateString() + " " + shortClock(r.at)));
    }
    prev = r;
    const first = (r.content || r.summary || r.reason || "").split("\n")[0];
    rows.push(el("button", { type: "button", role: "option", class: "row" + (r.lifecycle ? " lifecycle" : ""), "data-key": r.key,
      "aria-selected": String(r.key === state.selected), onclick: () => select(r.key, false) },
      el("span", { class: "t" }, clock(r.at)),
      el("span", { class: "route" }, el("b", {}, r.from || "unknown"), " → ", r.to || "everyone"),
      el("span", { class: "gist" }, r.lifecycle ? el("span", { class: "kind" }, r.type.replaceAll("_", " ")) : null, first)));
  }
  if (hiddenEarlier) rows.push(el("button", { type: "button", class: "quiet-btn older", onclick: () => {
    state.msgShown += MSG_PAGE; update(); } }, `Load ${Math.min(MSG_PAGE, hiddenEarlier)} earlier (${hiddenEarlier} more)`));
  if (!shown.length) rows.push(el("div", { class: "none" }, state.range ? "No messages in this range." : "No messages yet."));
  ledger.replaceChildren(...rows);
  if (state.follow) { ledger.scrollTop = 0; return; }
  const row = anchor && ledger.querySelector(`[data-key="${CSS.escape(anchor.key)}"]`);
  if (row) ledger.scrollTop += row.getBoundingClientRect().top - ledger.getBoundingClientRect().top - anchor.offset;
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
  // replaceChildren would print a null as text, so absent parts are dropped.
  $("inspector").replaceChildren(...[
    el("dl", {}, fields.flatMap(([k, v]) => [el("dt", {}, k), el("dd", {}, v)])),
    r.content ? el("div", { class: "content" }, r.content) : null,
    r.plan ? el("div", { class: "content" }, r.plan) : null].filter(Boolean));
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
  renderApprovalsTab();
  if (state.view === "board") { renderHarness(); renderStats(board); renderTasks(board); return; }
  if (state.view === "approvals") { renderApprovals(); return; }
  if (state.view === "results") { renderResults(board); return; }
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
  rail.folded.delete(name);
  Object.assign(state, { doneShown: DONE_PAGE, msgShown: MSG_PAGE, range: null, selected: null, follow: true });
  state.open.clear();
  disposeTerminals();
  writeURL();
  renderRail();
  if (state.boards[name]) { buildMain(); update(); } else showNotice("Loading " + name + "…");
  state.openPlans.clear();
  Object.assign(state.req, { open: new Set(), form: {}, busy: new Set(), errors: {}, overriding: new Set(), autoBusy: false, autoError: "", autoWindow: null });
  refreshSelected();
  refreshOversight();
}

/* ---------------- views and live terminals ---------------- */
function writeURL() {
  const q = new URLSearchParams({ team: state.team });
  if (state.view !== "board") q.set("view", state.view);
  history.replaceState(null, "", "?" + q);
}
function setView(view) {
  if (state.view === view) return;
  state.view = view;
  for (const [v] of VIEWS) {
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
// currentTermAgent is the member the single view shows: the one chosen for
// this team, else the first with a live terminal, else the first.
function currentTermAgent(board) {
  const chosen = state.termAgent[state.team];
  if (board.members.some((m) => m.name === chosen)) return chosen;
  return (board.members.find((m) => describe(m).live) || board.members[0])?.name || "";
}
function renderTerminals(board) {
  const single = state.termLayout === "single";
  const current = currentTermAgent(board);
  // The single view keeps only the shown member's terminal: the others
  // would hold WebSockets and tmux viewers nobody sees.
  const wanted = new Set(board.members.filter((m) => !single || m.name === current).map((m) => m.name));
  for (const [name, card] of terms) if (!wanted.has(name)) { card.dispose(); terms.delete(name); }
  for (const m of board.members) {
    if (!wanted.has(m.name)) continue;
    const member = { ...m, leader: m.agentId === board.team.leadAgentId };
    const tasks = board.tasks.filter((t) => t.status === "in_progress" && t.owner === m.name).map((t) => t.subject);
    if (terms.has(m.name)) terms.get(m.name).update(member, tasks);
    else {
      const card = new AgentTerminal(state.team, member, tasks);
      terms.set(m.name, card);
      $("terms").append(card.element);
    }
    const card = terms.get(m.name);
    card.element.classList.toggle("solo", single);
    if (single) selectCard(card);
  }
  // Cards keep the members' order when the grid fills in around the one shown.
  const order = board.members.filter((m) => terms.has(m.name)).map((m) => terms.get(m.name).element);
  if (order.some((node, i) => $("terms").children[i] !== node)) $("terms").append(...order);
  $("terms").classList.toggle("solo", single);
  renderTermBar(board, current);
}
function renderTermBar(board, current) {
  const single = state.termLayout === "single";
  if (!changed("term-bar", [single, current, board.members.map((m) => [m.name, agentState(m)[0], m.inboxCount])])) return;
  const layout = el("div", { class: "views term-layout", role: "tablist", "aria-label": "Terminal layout" },
    [["single", "Single"], ["grid", "All"]].map(([v, label]) => el("button", { type: "button", role: "tab",
      "aria-selected": String(state.termLayout === v), onclick: () => setTermLayout(v) }, label)));
  const lead = board.team.leadAgentId;
  $("term-bar").replaceChildren(single
    ? el("div", { class: "term-tabs", role: "tablist", "aria-label": "Members" }, board.members.map((m) => {
      const [cls, label] = agentState(m);
      return el("button", { type: "button", role: "tab", "aria-selected": String(m.name === current),
        title: `${m.name}: ${label}`, onclick: () => showTerminal(state.team, m.name) },
      el("span", { class: "dot " + cls, "aria-hidden": "true" }), el("span", {}, m.name),
      m.agentId === lead && m.name !== "leader" ? el("span", { class: "lead" }, "leader") : null,
      m.inboxCount ? el("em", { title: `${m.inboxCount} unread messages` }, String(m.inboxCount)) : null);
    }))
    : el("p", { class: "intro" }, "Click a terminal to select it: the mouse wheel then scrolls that terminal, otherwise it scrolls this list. "
      + "Scroll up or choose History to read earlier output, and Back to live to return. Take control to type, paste or press Ctrl+C. "
      + "Closing this page leaves the agents running."),
    layout);
}
function setTermLayout(layout) {
  if (state.termLayout === layout) return;
  state.termLayout = layout;
  store(TERM_LAYOUT_KEY, layout);
  const board = state.boards[state.team];
  if (!board) return;
  const kept = [...terms.values()];
  renderTerminals(board);
  $("terms").scrollTop = 0;
  // Terminals that stay connected are sized for the old layout; size them
  // for their new cards once the layout has applied.
  requestAnimationFrame(() => { for (const card of kept) if (terms.get(card.member.name) === card) card.refit(); });
}
function disposeTerminals() {
  for (const card of terms.values()) card.dispose();
  terms.clear();
}
function showTerminal(team, name) {
  selectTeam(team);
  state.termAgent[team] = name;
  setView("terminals");
  const board = state.boards[team];
  if (board) renderTerminals(board);
  const card = terms.get(name);
  if (card) { selectCard(card); card.element.scrollIntoView({ block: "nearest" }); }
}
window.addEventListener("pagehide", disposeTerminals);
$("rail-toggle").addEventListener("click", () => setRail(!state.railOpen));
setRail(state.railOpen);

api("api/host").then((h) => { setBoardHost(h); state.keys = {}; update(); renderRail(); }).catch(() => {});
// Older servers have no /api/links; the header then simply shows none.
api("api/links").then((links) => { state.links = links; renderLinks(); }).catch(() => {});
loadTeams().then(() => Promise.all([refreshSelected(), refreshOthers()]))
  .catch((e) => showNotice("Cannot reach team-workerd: " + e.message, true));
setInterval(refreshSelected, 2000);
// Approvals and spending change slowly and cost four daemon reads, so they poll less often.
async function refreshOversight() {
  const team = state.team;
  if (!team) return;
  try {
    state.oversight[team] = await api("api/teams/" + encodeURIComponent(team) + "/oversight");
    update();
  } catch { /* an older daemon without these routes simply shows no approvals or spend */ }
}
setInterval(refreshOversight, 5000);
refreshOversight();
setInterval(refreshOthers, 10000);
setInterval(() => loadTeams().catch(() => {}), 15000);
