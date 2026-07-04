// Generic sub-tab switcher shared by the media pages (Music / TV / Movies).
// A `.subtabs` bar of `.subtab` buttons (each `data-tab="X"`) toggles the
// matching `.subtab-panel#tab-X`. The default tab is whichever button + panel
// carry the `active` class in the server-rendered HTML — there is no JS default,
// so each page controls its own landing tab.
//
// The last tab you clicked is remembered per path (localStorage
// `iso_subtab:<pathname>`) and restored on the next visit, so wandering off
// the Artists tab and coming back to /music doesn't reset you to Recent.
// A URL that carries ?page= is a deep link into a specific tab's pager state —
// the server already marks the right tab active there, so the stored tab must
// not override it.
//
// Re-bound on every Turbo render (turbo:load fires on the initial load too).
// The .subtab nodes are fresh after each page swap, so binding never doubles up.
(() => {
  const storeKey = () => 'iso_subtab:' + location.pathname;

  const bind = () => {
    const tabs = Array.from(document.querySelectorAll('.subtab'));
    if (!tabs.length) return;
    const panels = Array.from(document.querySelectorAll('.subtab-panel'));

    const activate = (tab) => {
      tabs.forEach((t) => t.classList.remove('active'));
      panels.forEach((p) => p.classList.remove('active'));
      tab.classList.add('active');
      const panel = document.getElementById('tab-' + tab.getAttribute('data-tab'));
      if (panel) panel.classList.add('active');
    };

    tabs.forEach((tab) => {
      tab.addEventListener('click', () => {
        activate(tab);
        // Server-tabbed pages (/photos) have link subtabs with no data-tab —
        // nothing client-side to remember there.
        const dt = tab.getAttribute('data-tab');
        if (!dt) return;
        try { localStorage.setItem(storeKey(), dt); } catch (_) { /* private mode */ }
      });
    });

    // Restore the remembered tab — unless the URL pins one (?page= deep-links
    // a tab's pager state; ?tab= is a server-tabbed page where the URL, not
    // localStorage, owns the active tab).
    const params = new URLSearchParams(location.search);
    if (params.has('page') || params.has('tab')) return;
    let saved = null;
    try { saved = localStorage.getItem(storeKey()); } catch (_) { /* private mode */ }
    if (!saved) return;
    const tab = tabs.find((t) => t.getAttribute('data-tab') === saved);
    if (tab && !tab.classList.contains('active')) activate(tab);
  };
  document.addEventListener('turbo:load', bind);
})();
