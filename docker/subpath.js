// Upstream UIs use root-relative API URLs. Scope browser requests to this app.
(() => {
  const prefix = document.currentScript.dataset.prefix;
  function scoped(value) {
    const url = new URL(value, location.href);
    const local =
      url.origin === location.origin ||
      ((url.protocol === "ws:" || url.protocol === "wss:") &&
        url.host === location.host);
    if (local && !url.pathname.startsWith(prefix + "/")) {
      url.pathname = prefix + url.pathname;
      return url.href;
    }
    return value;
  }
  const fetch = window.fetch;
  window.fetch = function (input, options) {
    if (input instanceof Request) {
      const url = scoped(input.url);
      if (url !== input.url) input = new Request(url, input);
    } else {
      input = scoped(input);
    }
    return fetch.call(this, input, options);
  };
  for (const name of ["EventSource", "WebSocket"]) {
    if (!window[name]) continue;
    window[name] = new Proxy(window[name], {
      construct(target, args) {
        args[0] = scoped(args[0]);
        return Reflect.construct(target, args);
      },
    });
  }
  // Some Magpie plugin/icon URLs come from API responses rather than HTML.
  for (const [type, property] of [
    [HTMLImageElement, "src"],
    [HTMLIFrameElement, "src"],
  ]) {
    const descriptor = Object.getOwnPropertyDescriptor(
      type.prototype,
      property,
    );
    Object.defineProperty(type.prototype, property, {
      ...descriptor,
      set(value) {
        descriptor.set.call(this, scoped(value));
      },
    });
  }
})();
