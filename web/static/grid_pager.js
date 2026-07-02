// grid_pager.js — remote-native, in-place paging for the artist/movie/TV browse
// grids (the ones tagged `.band-albums-grid[data-grid-pager]`).
//
// Instead of a bottom Prev/Next that reloads the page, the grid refills in place
// with the next screenful when you move a remote past its right edge (and back
// past its left edge) — the couch interaction Dan asked for. The ‹ › chevrons
// fixed to the screen edges are the mouse path + a discovery hint; both call the
// same advance/retreat. The adjacent page is prefetched and cached (a Map keyed
// by page) so a flip is instant.
//
// Couch seam: couch.js binds keydown on `document`; a listener on the grid
// element fires first (bubble phase), so at a horizontal edge we advance and
// stopPropagation so couch.js never runs its spatial fallback. Off the edge, we
// leave the event alone and couch.js moves the ring normally.
(function () {
  'use strict';

  const cache = new Map(); // key `base|page|q` -> cards HTML fragment

  function initGrid(grid) {
    if (grid.__pagerInit) return; // one controller per grid element (survives in-place swaps)
    grid.__pagerInit = true;

    const base = location.pathname; // /music, /movies, /tv — each has one paged grid
    const total = parseInt(grid.dataset.totalPages, 10) || 1;
    const q = grid.dataset.q || '';
    let page = parseInt(grid.dataset.page, 10) || 1;
    if (total <= 1) return; // single page — nothing to wire

    const panel = grid.closest('.subtab-panel') || grid.parentElement || document;
    const nav = panel.querySelector('.grid-pager');
    const key = (p) => base + '|' + p + '|' + q;
    const url = (p) => base + '?grid=1&page=' + p + (q ? '&q=' + encodeURIComponent(q) : '');
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
      const prev = nav.querySelector('.grid-pager-prev');
      const next = nav.querySelector('.grid-pager-next');
      if (prev) prev.classList.toggle('is-hidden', page <= 1);
      if (next) next.classList.toggle('is-hidden', page >= total);
    }

    // Swap to page p. `focusEnd` lands the couch ring on the last card (when
    // retreating leftward) instead of the first, so left/right feels continuous.
    async function goTo(p, focusEnd) {
      if (navigating || p < 1 || p > total || p === page) return;
      navigating = true;
      try {
        const html = await fetchPage(p);
        if (html == null) return;
        grid.innerHTML = html;
        page = p;
        grid.dataset.page = String(p);
        updateNav();
        if (document.documentElement.getAttribute('data-couch') === '1') {
          const cards = grid.querySelectorAll('.band-album-card');
          const target = focusEnd ? cards[cards.length - 1] : cards[0];
          if (target) target.focus();
        }
        prefetch(p + 1);
        prefetch(p - 1);
      } finally {
        navigating = false;
      }
    }
    const advance = () => goTo(page + 1, false);
    const retreat = () => goTo(page - 1, true);

    // True when `el` has no card further in `dir` within its own row — i.e. it
    // sits on the grid's left/right edge. Mirrors couch.js's row-overlap test.
    function atEdge(el, dir) {
      const r = el.getBoundingClientRect();
      for (const c of grid.children) {
        if (c === el) continue;
        const cr = c.getBoundingClientRect();
        if (cr.bottom <= r.top || cr.top >= r.bottom) continue; // not same row
        if (dir === 'right' && cr.left > r.left + 1) return false;
        if (dir === 'left' && cr.left < r.left - 1) return false;
      }
      return true;
    }

    grid.addEventListener('keydown', (e) => {
      if (e.key !== 'ArrowRight' && e.key !== 'ArrowLeft') return;
      const active = document.activeElement;
      if (!active || !grid.contains(active)) return;
      const dir = e.key === 'ArrowRight' ? 'right' : 'left';
      if (!atEdge(active, dir)) return; // not at the edge — let couch.js move normally
      e.preventDefault();
      e.stopPropagation(); // suppress couch.js's spatial fallback for this edge press
      if (dir === 'right') advance();
      else retreat();
    });

    if (nav) {
      nav.addEventListener('click', (e) => {
        const a = e.target.closest && e.target.closest('a');
        if (!a) return;
        e.preventDefault();
        if (a.classList.contains('grid-pager-next')) advance();
        else if (a.classList.contains('grid-pager-prev')) retreat();
      });
    }

    updateNav();
    prefetch(page + 1); // warm the next page so the first flip is instant
  }

  const init = () => document.querySelectorAll('.band-albums-grid[data-grid-pager]').forEach(initGrid);
  document.addEventListener('turbo:load', init);
})();
