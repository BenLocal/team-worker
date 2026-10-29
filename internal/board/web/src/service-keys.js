export function openServiceKeys(trigger) {
  const dialog = document.createElement("dialog");
  dialog.className = "prompt-dialog service-keys";
  dialog.setAttribute("aria-labelledby", "service-keys-title");
  const card = document.createElement("div");
  card.className = "prompt-card";
  const head = document.createElement("header");
  head.className = "prompt-head";
  const title = document.createElement("h2");
  title.id = "service-keys-title";
  title.textContent = "Service keys";
  const close = document.createElement("button");
  close.className = "quiet-btn";
  close.textContent = "Close";
  close.type = "button";
  head.append(title, close);
  const body = document.createElement("div");
  body.className = "service-keys-body";
  body.textContent = "Loading service keys…";
  card.append(head, body);
  dialog.append(card);
  document.body.append(dialog);
  const controller = new AbortController();
  dialog.addEventListener("close", () => {
    controller.abort();
    dialog.remove();
    trigger?.focus({ preventScroll: true });
  }, { once: true });
  close.addEventListener("click", () => dialog.close());
  dialog.showModal();
  fetch("api/service-keys", { cache: "no-store", signal: controller.signal })
    .then(async (response) => {
      const result = await response.json();
      if (!response.ok) throw new Error(result.error || "Unable to load service keys");
      if (controller.signal.aborted) return;
      body.replaceChildren();
      for (const item of result) {
        const section = document.createElement("section");
        const label = document.createElement("label");
        label.textContent = `${item.name} · ${item.label}`;
        if (!item.value) {
          const note = document.createElement("p");
          note.className = "muted";
          note.textContent = "Not configured yet";
          section.append(label, note);
        } else {
          const input = document.createElement("input");
          input.type = "password";
          input.readOnly = true;
          input.autocomplete = "off";
          input.setAttribute("aria-label", `${item.name} ${item.label}`);
          input.value = item.value;
          const actions = document.createElement("div");
          actions.className = "service-key-actions";
          const show = document.createElement("button");
          show.className = "quiet-btn";
          show.textContent = "Show";
          show.type = "button";
          show.addEventListener("click", () => {
            input.type = input.type === "password" ? "text" : "password";
            show.textContent = input.type === "password" ? "Show" : "Hide";
          });
          const copy = document.createElement("button");
          copy.type = "button";
          copy.className = "quiet-btn";
          copy.textContent = "Copy";
          copy.addEventListener("click", async () => {
            try {
              await navigator.clipboard.writeText(input.value);
              copy.textContent = "Copied";
            } catch {
              input.type = "text";
              show.textContent = "Hide";
              input.focus();
              input.select();
              copy.textContent = "Select and copy";
            }
          });
          actions.append(show, copy);
          section.append(label, input, actions);
        }
        body.append(section);
      }
    })
    .catch((error) => {
      if (error.name !== "AbortError") body.textContent = error.message;
    });
}
