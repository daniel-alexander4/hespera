// couch.js — remote/keyboard navigation layer, always on.
// The input scheme (arrows move a focus ring, Enter/OK activates natively,
// Backspace/Escape walks back up the UI) works identically on the desktop and
// from a TV remote that emits standard key events (e.g. a Flirc dongle or BT
// remote). There is ONE mode: only the display scale varies (html[data-scale],
// set by the layout bootstrap from the display's physical size). The one input
// nicety keyed to the tv scale class is auto-focusing a starting element after
// each render (focusFirst) — on a desk it would steal focus for no benefit.
//
// Back walks up the UI in stages, then goes Home:
// dismiss an open overlay/menu → pull focus out of a subtab panel onto its
// menu bar → navigate to the home page (/). The Back button is a Home shortcut,
// not a history-retrace — going "up" one level is the breadcrumb's job.
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

  // summary is natively focusable (the settings accordion's headers are the
  // page's primary controls) but doesn't match the button/input/etc. list, so
  // without it the remote can't reach or open any accordion card.
  const FOCUSABLE = 'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), summary, [tabindex]:not([tabindex="-1"])';

  const visible = (el) => {
    if (el.offsetParent === null && getComputedStyle(el).position !== 'fixed') return false;
    const r = el.getBoundingClientRect();
    return r.width > 0 && r.height > 0;
  };

  // The currently-open overlay, if any: the first visible [data-couch-overlay].
  const openOverlay = () => Array.from(document.querySelectorAll('[data-couch-overlay]')).find(visible) || null;

  // A collapsed <details> keeps its body laid out (modern browsers hide it via
  // content-visibility on ::details-content, not display:none), so its controls
  // still pass the geometric visible() test — but the browser refuses to focus
  // them, which snags the ring. Treat content inside a closed <details> as
  // unreachable (the summary itself stays reachable so the card can be opened).
  const reachable = (el) => {
    if (el.tagName === 'SUMMARY') return true;
    const d = el.closest('details');
    return !d || d.open;
  };

  // Focus candidates are scoped to the open overlay when one is present, so
  // arrows can't drift to the dimmed page behind it; otherwise the whole page.
  const candidates = () => Array.from((openOverlay() || document).querySelectorAll(FOCUSABLE)).filter(visible).filter(reachable);

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

  // ---- Engage protocol (capture/uncapture) ----------------------------------
  // Any control that wants the arrow keys for itself — a <select> cycling
  // options, an <input type=range> (volume, music seek), or a custom slider
  // tagged [data-couch-capture] (era picker, video scrubber) — would otherwise
  // be a one-way door: arrows adjust it forever and focus can never leave. So
  // capture is EXPLICIT: a merely-focused control is transparent (arrows move
  // focus straight past it); Enter engages it (data-couch-engaged, styled with
  // an accent glow), arrows then act on the control, and Enter/Back or leaving
  // it releases. Custom widgets read the same attribute to gate their own
  // keydown handlers, so couch.js owns the whole protocol.
  const isEngageable = (el) => !!el && !!el.tagName &&
    (el.tagName === 'SELECT' || (el.tagName === 'INPUT' && el.type === 'range') ||
      (el.matches && el.matches('[data-couch-capture]')));
  const isEngaged = (el) => !!el && !!el.hasAttribute && el.hasAttribute('data-couch-engaged');
  const release = (el) => el.removeAttribute('data-couch-engaged');
  // Leaving an engaged control by any means (click elsewhere, Turbo swap,
  // widget blur-after-pick) releases it.
  document.addEventListener('focusout', (e) => {
    if (isEngaged(e.target)) release(e.target);
  });

  document.addEventListener('keydown', (e) => {
    const target = e.target;
    // Arrows must keep editing text; back keys are exempted from text fields
    // only. Text-ENTRY inputs only — a range/checkbox is an INPUT too, but has
    // no caret and takes the engage protocol / normal focus moves instead.
    const TEXT_TYPES = new Set(['text', 'search', 'password', 'email', 'url', 'tel', 'number']);
    const typing = target && (target.tagName === 'TEXTAREA' || target.isContentEditable ||
      (target.tagName === 'INPUT' && TEXT_TYPES.has(target.type)));

    if (e.key in DIRS) {
      if (typing) return; // native arrows (caret / spinner)
      if (isEngageable(target) && isEngaged(target)) return; // engaged: the control owns the arrows
      html.classList.remove('using-mouse');
      e.preventDefault(); // also stops an unengaged select/range from adjusting
      move(DIRS[e.key]);
      return;
    }
    if (e.key === 'Enter' && isEngageable(target)) {
      html.classList.remove('using-mouse');
      e.preventDefault(); // keep Enter from submitting an enclosing form
      if (isEngaged(target)) release(target);
      else target.setAttribute('data-couch-engaged', '');
      return;
    }
    // Enter toggles a checkbox like OK on a remote would — natively it triggers
    // the enclosing form's implicit submission instead (Space toggles, but a
    // remote has no Space).
    if (e.key === 'Enter' && target && target.tagName === 'INPUT' && (target.type === 'checkbox' || target.type === 'radio')) {
      e.preventDefault();
      target.click();
      return;
    }
    // A focused text input is otherwise a one-way door for a remote (arrows move
    // the caret, Back keys are suppressed while typing) — so Escape-family keys
    // exit the field instead of doing nothing. Backspace stays native: it must
    // keep deleting characters.
    if (typing && (e.key === 'Escape' || e.key === 'BrowserBack' || e.key === 'GoBack')) {
      html.classList.remove('using-mouse');
      e.preventDefault();
      target.blur();
      return;
    }
    // Back on an engaged control releases it; the NEXT press moves/navigates.
    if (BACK_KEYS.has(e.key) && isEngaged(target)) {
      html.classList.remove('using-mouse');
      e.preventDefault();
      release(target);
      return;
    }
    if (BACK_KEYS.has(e.key) && !typing) {
      // Native fullscreen owns Escape: exiting fullscreen must not also
      // navigate. Let the browser handle it untouched.
      if (document.fullscreenElement) return;
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
      // leaving (grid_pager.js). Second press then goes back.
      const active = document.activeElement;
      if (active && active.closest && active.closest('.subtab-panel')) {
        const menu = document.querySelector('.subtab.active') || document.querySelector('.subtab');
        if (menu) { menu.focus(); return; }
      }
      // Terminal stage: the Back button is a Home shortcut — go to the landing
      // page in one press (the breadcrumb is the way UP one level). Turbo.visit
      // keeps the persistent audio player alive, matching every other nav; a
      // full load is the fallback if Turbo isn't present. Already on Home →
      // no-op, so repeated presses don't stack duplicate Home history entries.
      // (This uniformly replaces history.back for every back-keycode, so the
      // remote's Back button behaves the same regardless of dongle.)
      if (window.location.pathname === '/') return;
      if (window.Turbo && typeof window.Turbo.visit === 'function') window.Turbo.visit('/');
      else window.location.href = '/';
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
    // A page with a subtab bar (Music / TV / Movies) lands the ring on its
    // active tab — selecting a main tab from the topbar starts you on "Recent",
    // not whatever control happens to be first in the content. Gated on input
    // MODALITY, not display scale: "using a remote" is true on a 32″ TV at the
    // `large` scale too, and using-mouse persists on <html> across Turbo body
    // swaps, so a mouse-driven visit is never focus-stolen while any
    // keyboard/remote-driven one gets its anchor. This runs before subtabs.js
    // restores a remembered tab (couch.js loads first), so the server-rendered
    // default tab is what gets the ring.
    const tab = document.querySelector('.subtab.active') || document.querySelector('.subtab');
    if (tab && !html.classList.contains('using-mouse')) { tab.focus(); return; }
    // The home dashboard has no subtab bar, but its nav cards ARE the primary
    // controls — anchor the ring on the first (Music) for a keyboard/remote start
    // at ANY scale (elsewhere only tv scale auto-focuses content, so a desk mouse
    // user isn't stolen from every load). Modality-gated like the subtab branch, so
    // a mouse user returning home mid-session is never focus-stolen.
    const isHome = location.pathname === '/';
    if (isHome ? html.classList.contains('using-mouse') : !isTVScale()) return;
    const all = candidates();
    if (!all.length) return;
    // Prefer the first control INSIDE the content (<main>) over the topbar that
    // precedes it in the DOM and the breadcrumb — otherwise the ring lands on
    // the top-left logo (a self-link) every non-subtab page and the remote user
    // arrow-hunts down into content. The topbar and breadcrumb stay reachable by
    // pressing Up. Fall back to the old first-non-breadcrumb, then anything.
    const main = document.querySelector('main');
    const inContent = (el) => !!main && main.contains(el) && !el.closest('.breadcrumb');
    const first = all.find(inContent) || all.find((el) => !el.closest('.breadcrumb')) || all[0];
    first.focus();
  };
  document.addEventListener('turbo:load', focusFirst);
})();
