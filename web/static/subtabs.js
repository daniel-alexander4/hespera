// Generic sub-tab switcher shared by the media pages (Music / TV / Movies).
// A `.subtabs` bar of `.subtab` buttons (each `data-tab="X"`) toggles the
// matching `.subtab-panel#tab-X`. The default tab is whichever button + panel
// carry the `active` class in the server-rendered HTML — there is no JS default,
// so each page controls its own landing tab.
(() => {
  const tabs = Array.from(document.querySelectorAll('.subtab'));
  if (!tabs.length) return;
  const panels = Array.from(document.querySelectorAll('.subtab-panel'));
  tabs.forEach((tab) => {
    tab.addEventListener('click', () => {
      const target = tab.getAttribute('data-tab');
      tabs.forEach((t) => t.classList.remove('active'));
      panels.forEach((p) => p.classList.remove('active'));
      tab.classList.add('active');
      const panel = document.getElementById('tab-' + target);
      if (panel) panel.classList.add('active');
    });
  });
})();
