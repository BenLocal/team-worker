export function vikunjaSettings(el, fetchJSON, put, signal) {
  const path = "api/settings/vikunja";
  const editor = el("div", {}, el("p", { class: "chat-status", role: "status" }, "Loading teams…"));
  const section = el("section", { class: "settings-pane vikunja-settings", "aria-labelledby": "vikunja-settings-title" },
    el("h3", { id: "vikunja-settings-title" }, "Vikunja webhooks"), editor);
  void fetchJSON(path).then((settings) => {
    if (signal.aborted) return;
    if (settings.length) render(settings);
    else editor.replaceChildren(el("p", { class: "muted" }, "Create a team before adding a webhook."));
  }).catch((error) => {
    if (!signal.aborted) editor.replaceChildren(el("p", { class: "chat-feedback error", role: "status" }, error.message));
  });

  function render(settings) {
    const team = el("select", {}, settings.map(({ team }) => el("option", { value: team }, team)));
    const endpoint = el("input", { readonly: true });
    const secret = el("input", { type: "password", autocomplete: "new-password" });
    const mode = el("select", {}, el("option", { value: "all" }, "All events"),
      el("option", { value: "allowlist" }, "Only listed events"));
    const events = el("textarea", { rows: "4", spellcheck: "false", placeholder: "task.created\ntask.comment.*" });
    const eventFields = el("div", {}, el("label", {}, "Allowed events", events),
      el("p", { class: "muted" }, "One event or prefix per line, such as task.*. Leave the list empty to pause delivery."));
    const feedback = el("p", { class: "chat-feedback", role: "status" });
    const save = el("button", { class: "tool-btn", type: "submit" }, "Save webhook");
    const updateMode = () => { eventFields.hidden = mode.value === "all"; };
    mode.addEventListener("change", updateMode);

    function selectTeam() {
      const hook = settings.find((hook) => hook.team === team.value);
      endpoint.value = new URL("/api/hooks/vikunja/" + encodeURIComponent(hook.team), location.origin).href;
      secret.value = "";
      secret.required = !hook.hasSecret;
      secret.placeholder = hook.hasSecret ? "Leave blank to keep the saved secret" : "Enter the secret from Vikunja";
      mode.value = hook.events === null ? "all" : "allowlist";
      events.value = hook.events?.join("\n") ?? "";
      feedback.classList.remove("error");
      feedback.textContent = "";
      updateMode();
    }
    team.addEventListener("change", selectTeam);
    editor.replaceChildren(el("form", { class: "chat-form", onsubmit: async (event) => {
      event.preventDefault();
      save.disabled = team.disabled = true;
      feedback.classList.remove("error");
      feedback.textContent = "Saving…";
      try {
        const index = settings.findIndex((hook) => hook.team === team.value);
        settings[index] = await put(path + "/" + encodeURIComponent(team.value), {
          secret: secret.value,
          events: mode.value === "all" ? null : events.value.split("\n").map((line) => line.trim()).filter(Boolean),
        });
        selectTeam();
        feedback.textContent = "Webhook saved";
      } catch (error) {
        if (signal.aborted) return;
        feedback.classList.add("error");
        feedback.textContent = error.message;
      } finally {
        save.disabled = team.disabled = false;
      }
    } }, el("label", {}, "Team", team), el("label", {}, "Webhook URL", endpoint),
    el("label", {}, "Signing secret", secret), el("label", {}, "Events to deliver", mode), eventFields,
    el("div", { class: "chat-actions" }, save, feedback)));
    selectTeam();
  }
  return section;
}
