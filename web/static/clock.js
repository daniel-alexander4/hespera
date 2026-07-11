// clock.js — the home screen's live clock (#app-clock), left of the version pill.
//
// One app-lifetime interval, window-guarded so it is created ONCE however many
// times Turbo re-renders the body. The element is queried per tick rather than
// cached (the home page re-renders with each body swap, and the clock does not
// exist on any other page), so there is nothing to bind, unbind, or leak — the
// tick is a no-op while the element is absent.
//
// Hour:minute, locale-formatted (12- or 24-hour per the viewer's locale). No
// seconds: this screen sits on a TV, and a second-by-second repaint buys nothing
// a home screen needs. The interval still runs at 1s so a minute boundary lands
// promptly, but the DOM is only touched when the rendered string actually
// changes.
(() => {
  if (window.__hesperaClock) return;
  window.__hesperaClock = true;

  const format = () =>
    new Date().toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' });

  let last = null;
  const tick = () => {
    const el = document.getElementById('app-clock');
    if (!el) return;
    const now = format();
    if (now === last && el.textContent) return;
    last = now;
    el.textContent = now;
  };

  setInterval(tick, 1000);
  document.addEventListener('turbo:load', tick); // paint immediately on arrival
  tick();
})();
