// grid_pager.js — remote-native, in-place paging for the artist/movie/TV browse
// grids (the ones tagged `.band-albums-grid[data-grid-pager]`).
//
// Instead of a bottom Prev/Next that reloads the page, the grid refills in
// place when you move past its edge in any direction: rightmost card + → or
// bottom row + ↓ advances; leftmost card + ← or top row + ↑ retreats. Paging
// is continuous — it wraps at both ends (page 1 ← → last page). The ‹ ›
// chevrons fixed to the screen edges are the mouse path; they're revealed only
// while the mouse is the active input (html.using-mouse, tracked by couch.js)
// so remote/keyboard users never see them. The adjacent pages are prefetched
// and cached (a Map keyed by page) so a flip is instant.
//
// Escape (couch.js's staged Back) is the way OUT of the grid to the subtab
// menu bar above it — ↑ pages rather than leaving, by design.
//
// Couch seam: couch.js binds keydown on `document`; a listener on the grid
// element fires first (bubble phase), so at an edge we page and
// stopPropagation so couch.js never runs its spatial fallback. Off the edge,
// we leave the event alone and couch.js moves the ring normally.
(function () {
  'use strict';

  const cache = new Map(); // key `base|page|q` -> cards HTML fragment

  function initGrid(grid) {
    if (grid.__pagerInit) return; // one controller per grid element (survives in-place swaps)
    grid.__pagerInit = true;

    const base = location.pathname; // /music, /movies, /tv — each has one paged grid
    const total = parseInt(grid.dataset.totalPages, 10) || 1;
    let page = parseInt(grid.dataset.page, 10) || 1;
    if (total <= 1) return; // single page — nothing to wire

    const panel = grid.closest('.subtab-panel') || grid.parentElement || document;
    const nav = panel.querySelector('.grid-pager');
    const key = (p) => base + '|' + p;
    const url = (p) => base + '?grid=1&page=' + p;
    const wrapNext = () => (page >= total ? 1 : page + 1);
    const wrapPrev = () => (page <= 1 ? total : page - 1);
    let navigating = false;

    async function fetchPage(p) {
      if (p < 1 || p > total) return null;
      if (cache.has(key(p))) return cache.get(key(p));
      try {
        const res = await fetch(url(p), { headers: { 'X-Requested-With': 'XMLHttpRequest' } });
        if (!res.ok) return null;
        const html = await res.text();
        cache.set(key(p), html);
        return html;
      } catch (_) {
        return null;
      }
    }
    const prefetch = (p) => { if (p >= 1 && p <= total && !cache.has(key(p))) fetchPage(p); };

    function updateNav() {
      if (!nav) return;
      const info = nav.querySelector('.grid-pager-info');
      if (info) info.textContent = 'Page ' + page + ' of ' + total;
    }

    // Swap to page p. `focus` (keyboard-driven flips only — a chevron click
    // must not steal the mouse user's focus) lands on the last card when
    // retreating and the first when advancing, so paging feels continuous.
    async function goTo(p, focus, focusEnd) {
      if (navigating || p < 1 || p > total || p === page) return;
      navigating = true;
      try {
        const html = await fetchPage(p);
        if (html == null) return;
        grid.innerHTML = html;
        page = p;
        grid.dataset.page = String(p);
        updateNav();
        if (focus) {
          const cards = grid.querySelectorAll('.band-album-card');
          const target = focusEnd ? cards[cards.length - 1] : cards[0];
          if (target) target.focus();
        }
        prefetch(wrapNext());
        prefetch(wrapPrev());
      } finally {
        navigating = false;
      }
    }
    const advance = (focus) => goTo(wrapNext(), focus, false);
    const retreat = (focus) => goTo(wrapPrev(), focus, true);

    // True when `el` has no card further in `dir` within its own row (for
    // left/right) or column (for up/down) — i.e. it sits on that edge of the
    // grid. Mirrors couch.js's cross-axis overlap test.
    function atEdge(el, dir) {
      const r = el.getBoundingClientRect();
      const horizontal = dir === 'left' || dir === 'right';
      for (const c of grid.children) {
        if (c === el) continue;
        const cr = c.getBoundingClientRect();
        if (horizontal) {
          if (cr.bottom <= r.top || cr.top >= r.bottom) continue; // not same row
          if (dir === 'right' && cr.left > r.left + 1) return false;
          if (dir === 'left' && cr.left < r.left - 1) return false;
        } else {
          if (cr.right <= r.left || cr.left >= r.right) continue; // not same column
          if (dir === 'down' && cr.top > r.top + 1) return false;
          if (dir === 'up' && cr.top < r.top - 1) return false;
        }
      }
      return true;
    }

    const KEY_DIRS = { ArrowRight: 'right', ArrowLeft: 'left', ArrowDown: 'down', ArrowUp: 'up' };
    grid.addEventListener('keydown', (e) => {
      const dir = KEY_DIRS[e.key];
      if (!dir) return;
      const active = document.activeElement;
      if (!active || !grid.contains(active)) return;
      if (!atEdge(active, dir)) return; // not at the edge — let couch.js move normally
      e.preventDefault();
      e.stopPropagation(); // suppress couch.js's spatial fallback for this edge press
      // stopPropagation means couch.js never sees this key, so hand the
      // modality back to the keyboard here (hides the mouse chevrons).
      document.documentElement.classList.remove('using-mouse');
      if (dir === 'right' || dir === 'down') advance(true);
      else retreat(true);
    });

    if (nav) {
      nav.addEventListener('click', (e) => {
        const a = e.target.closest && e.target.closest('a');
        if (!a) return;
        e.preventDefault();
        if (a.classList.contains('grid-pager-next')) advance(false);
        else if (a.classList.contains('grid-pager-prev')) retreat(false);
      });
    }

    updateNav();
    prefetch(wrapNext()); // warm the next page so the first flip is instant
  }

  const init = () => document.querySelectorAll('.band-albums-grid[data-grid-pager]').forEach(initGrid);
  document.addEventListener('turbo:load', init);
})();
