// keytrace.js — remote-input diagnostic tracer, off unless the keytrace_enabled
// app-setting is on (Settings → API Keys, or `hescli config set keytrace_enabled
// on` over SSH). While armed, every keydown — key/code, target, whether a handler
// consumed it, where focus moved — plus each Turbo navigation is beaconed to
// POST /debug/keytrace, which appends JSON lines to DataDir/keytrace.jsonl.
// That answers "what does this TV remote actually emit, and what did Hespera do
// with it": the app window on a TV has no address bar or devtools, and a
// .desktop launch swallows stdout, so the trace lives server-side. One flag
// probe per full document load (Turbo body swaps keep this window alive); when
// the setting is off the probe is the only cost.
(() => {
  if (window.__hesperaKeytrace) return;
  window.__hesperaKeytrace = true;

  const describe = (el) => {
    if (!el || !el.tagName) return '';
    let s = el.tagName.toLowerCase();
    if (el.id) s += '#' + el.id;
    else if (typeof el.className === 'string' && el.className.trim()) s += '.' + el.className.trim().split(/\s+/).slice(0, 2).join('.');
    return s;
  };
  const send = (ev) => {
    try {
      const body = JSON.stringify(ev);
      if (navigator.sendBeacon) navigator.sendBeacon('/debug/keytrace', new Blob([body], { type: 'application/json' }));
      else fetch('/debug/keytrace', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body, keepalive: true });
    } catch (e) { /* tracing must never break the app */ }
  };
  // After-dispatch snapshot: keydown dispatch is synchronous, so a 0-timeout
  // runs once every handler (couch.js focus moves, etc.) has had its turn.
  const after = (fn) => setTimeout(fn, 0);

  const arm = () => {
    // Capture phase: sees every key even when a handler stopPropagation()s
    // (era slider, grid pager), before anything can swallow it.
    window.addEventListener('keydown', (e) => {
      const focusBefore = describe(document.activeElement);
      const url = location.pathname + location.search;
      after(() => send({
        type: 'key', key: e.key, code: e.code, keyCode: e.keyCode, repeat: e.repeat,
        target: describe(e.target), handled: e.defaultPrevented,
        focusBefore, focusAfter: describe(document.activeElement),
        url,
      }));
    }, true);
    document.addEventListener('turbo:load', () => {
      send({ type: 'nav', url: location.pathname + location.search, focus: describe(document.activeElement) });
    });
    send({ type: 'trace-start', url: location.pathname + location.search, ua: navigator.userAgent, scale: document.documentElement.getAttribute('data-scale') || '' });
  };

  fetch('/debug/keytrace')
    .then((r) => (r.ok ? r.json() : null))
    .then((d) => { if (d && d.enabled) arm(); })
    .catch(() => {});
})();
