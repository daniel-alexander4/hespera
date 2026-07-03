// photo_view.js — the full-screen photo viewer's remote/keyboard navigation.
//
// ←/→ move to the previous/next photo under the grid's filter + order context
// (server-computed hrefs on #photoView). The listener sits on the focused
// container element, so it fires in the bubble phase BEFORE couch.js's
// document-level listener (the grid_pager.js seam) and stops propagation —
// otherwise couch would spend the arrows on spatial focus moves. Back
// (Escape) stays couch's: history.back() to the grid.
(() => {
  const boot = () => {
    const view = document.getElementById('photoView');
    if (!view) return;
    view.focus(); // arrows work immediately; also gives couch a focus anchor
    view.addEventListener('keydown', (e) => {
      const href = e.key === 'ArrowLeft' ? view.dataset.prev
        : e.key === 'ArrowRight' ? view.dataset.next : null;
      if (href === null) return;
      e.preventDefault();
      e.stopPropagation();
      if (!href) return; // at the first/last photo: consume, don't wander focus
      if (window.Turbo && typeof window.Turbo.visit === 'function') window.Turbo.visit(href);
      else window.location.href = href;
    });
  };
  document.addEventListener('turbo:load', boot);
})();
