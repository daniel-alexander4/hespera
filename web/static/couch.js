// couch.js — 10-foot "couch mode" remote/keyboard navigation layer.
// Active only when <html data-couch="1"> (set by the layout bootstrap from
// ?couch=1 / localStorage). It makes the existing pages navigable with a TV
// remote that emits standard key events (e.g. a Flirc dongle or BT remote):
// arrow keys move a visible focus ring between focusable elements, Enter/OK
// activates natively, and Backspace/Escape goes back. No server involvement.
(() => {
  if (document.documentElement.getAttribute('data-couch') !== '1') return;

  const FOCUSABLE = 'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), [tabindex]:not([tabindex="-1"])';

  const visible = (el) => {
    if (el.offsetParent === null && getComputedStyle(el).position !== 'fixed') return false;
    const r = el.getBoundingClientRect();
    return r.width > 0 && r.height > 0;
  };

  const candidates = () => Array.from(document.querySelectorAll(FOCUSABLE)).filter(visible);

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

  document.addEventListener('keydown', (e) => {
    const target = e.target;
    const typing = target && (target.tagName === 'INPUT' || target.tagName === 'TEXTAREA' || target.isContentEditable);

    if (e.key in DIRS) {
      if (typing) return; // let arrows edit text
      e.preventDefault();
      move(DIRS[e.key]);
      return;
    }
    if ((e.key === 'Backspace' || e.key === 'Escape' || e.key === 'BrowserBack') && !typing) {
      e.preventDefault();
      history.back();
    }
    // Enter / OK is left to native behavior (activates links and buttons).
  });

  // Land focus somewhere sensible on first paint so the remote has a starting point.
  const first = candidates()[0];
  if (first) first.focus();
})();
