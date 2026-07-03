// couch.js — remote/keyboard navigation layer, always on.
// The input scheme (arrows move a focus ring, Enter/OK activates natively,
// Backspace/Escape walks back up the UI) works identically on the desktop and
// from a TV remote that emits standard key events (e.g. a Flirc dongle or BT
// remote). There is ONE mode: only the display scale varies (html[data-scale],
// set by the layout bootstrap from the display's physical size). The one input
// nicety keyed to the tv scale class is auto-focusing a starting element after
// each render (focusFirst) — on a desk it would steal focus for no benefit.
//
// Back walks up the hierarchy in stages, like a TV remote's back button:
// dismiss an open overlay/menu → pull focus out of a subtab panel onto its
// menu bar → climb to the breadcrumb's semantic parent → history.back().
//
// Overlay contract: an element tagged [data-couch-overlay] that is currently
// visible (any hide mechanism — display:none via class, attribute, or inline
// style all read as not-visible) traps focus inside itself, and Back dismisses
// it instead of navigating by clicking its [data-couch-dismiss] control (so
// "how to close" stays owned by the overlay's own template/handler).
//
// Input modality: the html element carries `using-mouse` while the pointer is
// the active input (cleared on any handled key). CSS uses it to reveal
// mouse-only affordances (the grid-pager chevrons) without showing them to
// remote/keyboard users.
(() => {
  const isTVScale = () => document.documentElement.getAttribute('data-scale') === 'tv';

  const FOCUSABLE = 'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), [tabindex]:not([tabindex="-1"])';

  const visible = (el) => {
    if (el.offsetParent === null && getComputedStyle(el).position !== 'fixed') return false;
    const r = el.getBoundingClientRect();
    return r.width > 0 && r.height > 0;
  };

  // The currently-open overlay, if any: the first visible [data-couch-overlay].
  const openOverlay = () => Array.from(document.querySelectorAll('[data-couch-overlay]')).find(visible) || null;

  // Focus candidates are scoped to the open overlay when one is present, so
  // arrows can't drift to the dimmed page behind it; otherwise the whole page.
  const candidates = () => Array.from((openOverlay() || document).querySelectorAll(FOCUSABLE)).filter(visible);

  const center = (r) => ({ x: r.left + r.width / 2, y: r.top + r.height / 2 });

  // Pick the next candidate in the given direction from the active element.
  // Prefer a candidate whose cross-axis extent overlaps the active element's —
  // i.e. one in the same row (for left/right) or column (for up/down) — ranked
  // by how close it is along the press direction. This keeps focus tracking the
  // row/column instead of drifting diagonally on dense grids. Only when nothing
  // aligns do we fall back to the nearest item overall, so every focusable stays
  // reachable.
  const move = (dir) => {
    const all = candidates();
    if (!all.length) return;
    const active = document.activeElement;
    if (!active || !all.includes(active)) { all[0].focus(); return; }
    const a = active.getBoundingClientRect();
    const ac = center(a);

    let aligned = null, alignedGap = Infinity;   // overlaps the cross axis
    let nearest = null, nearestScore = Infinity;  // fallback: closest overall
    for (const el of all) {
      if (el === active) continue;
      const r = el.getBoundingClientRect();
      const c = center(r);
      const dx = c.x - ac.x;
      const dy = c.y - ac.y;
      let primary, cross, overlap;
      if (dir === 'left') { if (dx >= -1) continue; primary = -dx; cross = Math.abs(dy); overlap = r.bottom > a.top && r.top < a.bottom; }
      else if (dir === 'right') { if (dx <= 1) continue; primary = dx; cross = Math.abs(dy); overlap = r.bottom > a.top && r.top < a.bottom; }
      else if (dir === 'up') { if (dy >= -1) continue; primary = -dy; cross = Math.abs(dx); overlap = r.right > a.left && r.left < a.right; }
      else { if (dy <= 1) continue; primary = dy; cross = Math.abs(dx); overlap = r.right > a.left && r.left < a.right; }

      const score = primary + cross * 2;
      if (score < nearestScore) { nearestScore = score; nearest = el; }
      if (overlap && primary < alignedGap) { alignedGap = primary; aligned = el; }
    }

    const best = aligned || nearest;
    if (best) {
      best.focus();
      best.scrollIntoView({ block: 'nearest', inline: 'nearest', behavior: 'smooth' });
    }
  };

  const DIRS = { ArrowLeft: 'left', ArrowRight: 'right', ArrowUp: 'up', ArrowDown: 'down' };
  // A remote's "Back"/parent button can arrive as any of these key values
  // depending on the dongle/OS (Flirc, BT, CEC, webOS/Android TV), so catch them
  // all — otherwise the behaviour depends on which code the remote happens to send.
  const BACK_KEYS = new Set(['Backspace', 'Escape', 'BrowserBack', 'GoBack']);

  // The current page's semantic parent, so Back climbs the hierarchy like a TV
  // remote's up button (Album → Artist → Music → Home) rather than retracing
  // browsing history. The breadcrumb already encodes the parent chain, so its
  // LAST crumb IS the immediate parent; the breadcrumb-less immersive players
  // carry the same target in [data-couch-parent]. Null → no known parent.
  const parentHref = () => {
    const crumbs = document.querySelectorAll('.breadcrumb a[href]');
    if (crumbs.length) return crumbs[crumbs.length - 1].getAttribute('href');
    const hint = document.querySelector('[data-couch-parent]');
    return hint ? hint.getAttribute('data-couch-parent') : null;
  };
  const navigateUp = (href) => {
    if (window.Turbo && typeof window.Turbo.visit === 'function') window.Turbo.visit(href);
    else window.location.href = href;
  };

  // Input-modality tracking: mouse presence reveals mouse-only affordances
  // (html.using-mouse in CSS); any handled key hands control back to the
  // keyboard/remote and hides them again.
  const html = document.documentElement;
  document.addEventListener('mousemove', () => {
    if (!html.classList.contains('using-mouse')) html.classList.add('using-mouse');
  }, { passive: true });
  document.addEventListener('mousedown', () => {
    if (!html.classList.contains('using-mouse')) html.classList.add('using-mouse');
  }, { passive: true });

  document.addEventListener('keydown', (e) => {
    const target = e.target;
    // Arrows must keep editing text and cycling <select> options; back keys are
    // exempted from text fields only (Escape on a focused select is still Back).
    const typing = target && (target.tagName === 'INPUT' || target.tagName === 'TEXTAREA' || target.isContentEditable);

    if (e.key in DIRS) {
      if (typing || (target && target.tagName === 'SELECT')) return; // native arrows
      html.classList.remove('using-mouse');
      e.preventDefault();
      move(DIRS[e.key]);
      return;
    }
    if (BACK_KEYS.has(e.key) && !typing) {
      // Native fullscreen owns Escape: exiting fullscreen must not also
      // navigate. Let the browser handle it untouched.
      if (document.fullscreenElement) return;
      // An open topbar dropdown owns Escape too — layout.html's own document
      // listener closes it; we just don't navigate on the same press.
      if (document.querySelector('[data-menu][data-open="1"]')) return;
      html.classList.remove('using-mouse');
      e.preventDefault();
      const overlay = openOverlay();
      if (overlay) {
        const dismiss = overlay.querySelector('[data-couch-dismiss]');
        if (dismiss) dismiss.click();
        return; // dismiss the overlay instead of leaving the page
      }
      // Stage 2 (subtab pages): pull focus out of the content panel onto its
      // menu bar — the remote-Back way OUT of a grid whose ↑ pages instead of
      // leaving (grid_pager.js). Second press then climbs to the parent.
      const active = document.activeElement;
      if (active && active.closest && active.closest('.subtab-panel')) {
        const menu = document.querySelector('.subtab.active') || document.querySelector('.subtab');
        if (menu) { menu.focus(); return; }
      }
      const parent = parentHref();
      if (parent) { navigateUp(parent); return; } // climb to the semantic parent
      history.back(); // root/unknown-parent pages: retrace history
    }
    // Enter / OK is left to native behavior (activates links and buttons).
  });

  // TV-scale only: land focus somewhere sensible after each render so the
  // remote always has a starting point. On the desktop this would steal focus
  // on every page load for no benefit — there, the first arrow press engages
  // navigation instead (move()'s no-active branch focuses it). turbo:load
  // fires on the initial load and after every Turbo visit; the keydown listener
  // above is added once and queries the live DOM, so it keeps working across
  // visits without re-binding.
  const focusFirst = () => {
    if (!isTVScale()) return;
    const all = candidates();
    if (!all.length) return;
    // Prefer the first in-content control over the breadcrumb, so a page doesn't
    // land the ring on its "up to parent" link every load (the breadcrumb is
    // still reachable by pressing Up). Fall back to it if it's the only thing.
    const first = all.find((el) => !el.closest('.breadcrumb')) || all[0];
    first.focus();
  };
  document.addEventListener('turbo:load', focusFirst);
})();
