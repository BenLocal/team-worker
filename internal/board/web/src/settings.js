export function openSettings(trigger, el) {
  const controller = new AbortController();
  const dialog = el("dialog", { class: "prompt-dialog chat-settings", "aria-labelledby": "settings-title" });
  const status = el("p", { class: "chat-status", role: "status" }, "Loading settings…");
  const editor = el("div");
  const blocked = el("section");
  const pane = el("section", { class: "chat-settings-pane", "aria-labelledby": "feishu-settings-title" },
    el("h3", { id: "feishu-settings-title" }, "Feishu integration"), status, editor, blocked);
  dialog.append(el("div", { class: "prompt-card" },
    el("header", { class: "prompt-head" }, el("h2", { id: "settings-title" }, "Settings"),
      el("button", { class: "quiet-btn", type: "button", onclick: () => dialog.close() }, "Close")),
    el("div", { class: "prompt-body" }, pane)));
  document.body.append(dialog);
  dialog.showModal();

  let timer;
  let settings;
  let connections = [];
  let appInfo;
  const fetchJSON = async (path, options) => {
    const response = await fetch(path, { cache: "no-store", signal: controller.signal, ...options });
    const data = await response.json();
    if (!response.ok) throw new Error(data.error || response.statusText);
    return data;
  };
  const put = (path, data) => fetchJSON(path, { method: "PUT", headers: { "Content-Type": "application/json" },
    body: JSON.stringify(data) });
  const sameApp = (a, b) => a?.provider === b?.provider && a?.tenant === b?.tenant && a?.application === b?.application;
  const currentApp = () => {
    if (appInfo?.appId) return { provider: "feishu", tenant: "app-scoped", application: appInfo.appId };
    return connections.find((item) => item.provider === "feishu" && item.connected)
      ?? connections.find((item) => item.provider === "feishu") ?? settings.feishuSender;
  };

  function renderStatus() {
    const app = currentApp();
    const connection = connections.find((candidate) => sameApp(app, candidate));
    if (!app) {
      status.textContent = "No Feishu application connected";
    } else if (!connection?.connected) {
      status.textContent = connection?.lastError ? `Feishu offline · ${connection.lastError}` : "Feishu offline · Waiting to connect";
    } else {
      const state = connection.appliedRevision !== settings.revision ? "Saved settings are pending"
        : settings.feishuSenderGrant ? "Settings applied" : "Ready to authorize a Feishu account";
      status.textContent = `Feishu online · ${state}`;
    }
  }

  function renderForm(editCredentials = false) {
    const app = currentApp();
    const editSender = app && !editCredentials;
    const feedback = el("p", { class: "chat-feedback", role: "status" });
    const save = el("button", { type: "submit", class: "tool-btn" }, editSender ? "Save settings" : "Save and connect");
    const navigation = editSender && !appInfo?.managedByEnvironment
      ? el("button", { type: "button", class: "quiet-btn", onclick: () => renderForm(true) }, "Application credentials")
      : editCredentials ? el("button", { type: "button", class: "quiet-btn", onclick: () => renderForm() }, "Cancel") : null;
    const fields = [];
    let submit;
    if (editSender) {
      const id = el("input", { value: sameApp(app, settings.feishuSender) ? settings.feishuSender.openId : "",
        placeholder: "ou_…", autocomplete: "off" });
      fields.push(el("label", {}, "Feishu account allowed to message all teams", id),
        el("p", { class: "muted" }, "Enter your open_id for this app. Clear the field to pause incoming messages."));
      submit = async () => {
        const { provider, tenant, application } = app;
        settings = await put("api/chat/settings", { revision: settings.revision, bindings: settings.bindings,
          updateFeishuSender: true, feishuSender: id.value.trim() ? { provider, tenant, application, openId: id.value.trim() } : null });
        feedback.textContent = `Saved version ${settings.revision}`;
      };
    } else {
      const id = el("input", { required: true, value: app?.application ?? "", autocomplete: "off" });
      const secret = el("input", { required: true, type: "password", autocomplete: "new-password" });
      fields.push(el("label", {}, "App ID", id), el("label", {}, "App Secret", secret));
      submit = async () => {
        try {
          appInfo = await put("api/chat/feishu/app", { appId: id.value.trim(), appSecret: secret.value });
          renderForm();
        } finally {
          secret.value = "";
        }
      };
    }
    editor.replaceChildren(el("form", { class: "chat-form", onsubmit: async (event) => {
      event.preventDefault();
      save.disabled = true;
      feedback.classList.remove("error");
      feedback.textContent = "Saving…";
      try {
        await submit();
        renderStatus();
      } catch (error) {
        if (controller.signal.aborted) return;
        feedback.classList.add("error");
        feedback.textContent = error.message;
      } finally {
        save.disabled = false;
      }
    } }, fields, el("div", { class: "chat-actions" }, save, navigation, feedback)));
  }

  dialog.addEventListener("close", () => {
    controller.abort();
    clearInterval(timer);
    dialog.remove();
    trigger.focus({ preventScroll: true });
  }, { once: true });
  const metadata = fetchJSON("api/chat/feishu/app").catch(() => undefined);
  void Promise.all([fetchJSON("api/chat/settings"), fetchJSON("api/chat/connections"), metadata])
    .then(([saved, registered, info]) => {
      settings = saved;
      connections = registered;
      appInfo = info;
      renderForm();
      renderStatus();
      void fetchJSON("api/chat/blocked").then((items) => {
        if (!items.length) return;
        blocked.replaceChildren(el("details", {}, el("summary", {}, "Blocked Feishu requests"), items.map((item) =>
          el("article", {}, el("p", {}, `${item.team || "Unassigned"} · ${item.requestId} · ${item.reason}`),
            el("p", {}, item.content)))));
      }).catch(() => {});
      timer = setInterval(() => {
        void fetchJSON("api/chat/connections").then((next) => {
          const previous = currentApp();
          connections = next;
          if (!sameApp(previous, currentApp())) renderForm();
          renderStatus();
        }).catch(() => {
          if (!controller.signal.aborted) status.textContent = "Unable to check the Feishu connection";
        });
      }, 5000);
    }).catch((error) => { if (!controller.signal.aborted) status.textContent = error.message; });
}
