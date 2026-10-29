// Shared prompt viewer: a native <dialog> that fetches one stored prompt only
// when a viewer asks for it, then renders it as plain text. Keeping the fetch
// out of the board snapshot and its polling loops keeps both small.

// openTeamPrompt shows the shared instructions of one team. trigger is the
// control to focus again on close; callers pass it because menus that opened
// from a click are removed before the dialog closes.
export function openTeamPrompt(team, trigger) {
  return openPrompt({
    title: `${team} prompt`,
    context: "Shared team instructions",
    url: `api/teams/${encodeURIComponent(team)}/prompt`,
    trigger,
  });
}

// openMemberPrompt shows one member's instructions; the user query selects
// between members that share a name.
export function openMemberPrompt(team, name, user, trigger) {
  const query = new URLSearchParams({ user: user || "" });
  return openPrompt({
    title: `${name} prompt`,
    context: `Agent instructions in ${team}`,
    url: `api/teams/${encodeURIComponent(team)}/members/${encodeURIComponent(name)}/prompt?${query}`,
    trigger,
  });
}

async function openPrompt({ title, context, url, trigger: openedFrom }) {
  // showModal focuses the first focusable control, so remember where focus was
  // to return it when the dialog closes.
  const trigger = openedFrom || document.activeElement;
  // Sidebar action buttons are hidden unless their row is hovered. Mark the
  // real origin so it stays visible while the dialog is open and until focus
  // lands on it again; a hidden trigger silently refuses focus on close.
  const origin = openedFrom instanceof HTMLElement ? openedFrom : null;
  origin?.classList.add("prompt-origin");
  const dialog = document.createElement("dialog");
  dialog.className = "prompt-dialog";
  dialog.setAttribute("aria-labelledby", "prompt-dialog-title");

  const card = document.createElement("div");
  card.className = "prompt-card";
  const head = document.createElement("header");
  head.className = "prompt-head";
  const heading = document.createElement("div");
  const name = document.createElement("h2");
  name.id = "prompt-dialog-title";
  name.textContent = title;
  const note = document.createElement("p");
  note.className = "prompt-context";
  note.textContent = context;
  heading.append(name, note);
  const close = document.createElement("button");
  close.type = "button";
  close.className = "quiet-btn prompt-close";
  close.textContent = "Close";
  close.setAttribute("aria-label", "Close prompt");
  head.append(heading, close);

  const body = document.createElement("div");
  body.className = "prompt-body loading";
  body.tabIndex = 0;
  body.setAttribute("aria-busy", "true");
  body.textContent = "Loading prompt…";

  card.append(head, body);
  dialog.append(card);
  document.body.append(dialog);

  const controller = new AbortController();
  const restore = () => {
    controller.abort();
    dialog.remove();
    if (origin && origin.isConnected) {
      origin.focus({ preventScroll: true });
      if (document.activeElement === origin) {
        // Drop the forced visibility only once focus leaves the origin again.
        origin.addEventListener("blur", () => origin.classList.remove("prompt-origin"), { once: true });
        return;
      }
      origin.classList.remove("prompt-origin");
    }
    if (trigger instanceof HTMLElement && trigger.isConnected) trigger.focus({ preventScroll: true });
  };
  dialog.addEventListener("close", restore, { once: true });
  close.addEventListener("click", () => dialog.close());
  // A click on the backdrop, outside the card, dismisses the dialog too.
  dialog.addEventListener("click", (event) => { if (event.target === dialog) dialog.close(); });

  dialog.showModal();
  close.focus();

  try {
    const response = await fetch(url, { signal: controller.signal, cache: "no-store" });
    const data = await response.json().catch(() => ({}));
    if (!response.ok) throw new Error(data.error || `Request failed (${response.status})`);
    if (!dialog.open) return;
    const prompt = typeof data.prompt === "string" ? data.prompt : "";
    body.classList.remove("loading");
    body.setAttribute("aria-busy", "false");
    if (prompt.trim() === "") {
      body.classList.add("empty");
      body.textContent = "No prompt is stored.";
      return;
    }
    // textContent keeps the prompt inert while white-space: pre-wrap preserves it.
    body.textContent = prompt;
  } catch (error) {
    if (error.name === "AbortError" || !dialog.open) return;
    body.classList.remove("loading");
    body.classList.add("error");
    body.setAttribute("aria-busy", "false");
    body.textContent = "Cannot load the prompt: " + error.message;
  }
}
