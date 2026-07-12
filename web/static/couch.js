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
// Home (the remote's own Home button) skips the ladder entirely: one press goes
// to / from anywhere and leaves the ring on the Music card.
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

  const focusMoved = (el) => {
    if (!el) return;
    el.focus();
    el.scrollIntoView({ block: 'nearest', inline: 'nearest', behavior: 'smooth' });
  };

  // A dense band-album card grid (the paged browse grids AND the cast/similar/
  // compilation card strips) keeps the Netflix-style spatial behavior below —
  // up/down track the column, left/right the row, with a nearest fallback so
  // every card stays reachable. Everything else (the home nav cards, the
  // stacked home carousels, vertical lists, the player transport, the settings
  // accordion) uses the row-locked model.
  const inGrid = (el) => !!(el && el.closest && el.closest('.band-albums-grid'));

  // Move the focus ring one step in `dir`. Two regimes:
  //   • Inside a card grid — the original 2D spatial nav: prefer a candidate
  //     whose cross-axis extent overlaps the active element's (same row for
  //     left/right, same column for up/down) ranked by distance along the press
  //     direction; fall back to the nearest item overall so nothing is stranded.
  //   • Everywhere else (the ROW-LOCKED model Dan asked for) — left/right stay
  //     within the current row (no cross-row fallback; a row end is a no-op,
  //     grid_pager owns any real grid edge), and up/down jump to the LEFTMOST
  //     item of the nearest row in that direction.
  const move = (dir) => {
    const all = candidates();
    if (!all.length) return;
    const active = document.activeElement;
    if (!active || !all.includes(active)) { all[0].focus(); return; }
    const a = active.getBoundingClientRect();
    const ac = center(a);
    const horizontal = dir === 'left' || dir === 'right';

    if (inGrid(active)) {
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
      focusMoved(aligned || nearest);
      return;
    }

    if (horizontal) {
      // Nearest same-row (vertically overlapping) candidate in the press
      // direction; no cross-row fallback so the ring stays on this row.
      let best = null, bestGap = Infinity;
      for (const el of all) {
        if (el === active) continue;
        const r = el.getBoundingClientRect();
        if (!(r.bottom > a.top && r.top < a.bottom)) continue; // not the same row
        const dx = center(r).x - ac.x;
        const gap = dir === 'left' ? -dx : dx;
        if (gap <= 1) continue; // not in the press direction
        if (gap < bestGap) { bestGap = gap; best = el; }
      }
      focusMoved(best);
      return;
    }

    // Vertical: find the nearest DIFFERENT row in the press direction (the
    // anchor), then focus the leftmost candidate sharing that row's band.
    let anchor = null, anchorGap = Infinity;
    for (const el of all) {
      if (el === active) continue;
      const r = el.getBoundingClientRect();
      if (r.bottom > a.top && r.top < a.bottom) continue; // same row as active — that's left/right's job
      const dy = center(r).y - ac.y;
      const gap = dir === 'up' ? -dy : dy;
      if (gap <= 0) continue; // not in the press direction
      if (gap < anchorGap) { anchorGap = gap; anchor = el; }
    }
    if (!anchor) return; // nothing that way — no-op (grid_pager pages a grid edge)
    const band = anchor.getBoundingClientRect();
    let leftmost = anchor, leftX = band.left;
    for (const el of all) {
      if (el === active || el === anchor) continue;
      const r = el.getBoundingClientRect();
      if (!(r.bottom > band.top && r.top < band.bottom)) continue; // not in the anchor's row
      if (r.left < leftX) { leftX = r.left; leftmost = el; }
    }
    focusMoved(leftmost);
  };

  const DIRS = { ArrowLeft: 'left', ArrowRight: 'right', ArrowUp: 'up', ArrowDown: 'down' };
  // A remote's "Back"/parent button can arrive as any of these key values
  // depending on the dongle/OS (Flirc, BT, CEC, webOS/Android TV), so catch them
  // all — otherwise the behaviour depends on which code the remote happens to send.
  const BACK_KEYS = new Set(['Backspace', 'Escape', 'BrowserBack', 'GoBack']);
  // Same for a remote's Home button: the Flirc emits Alt+Home (key 'Home'),
  // other dongles send BrowserHome/GoHome. Modifiers are ignored for the same
  // reason — the dongle picks them, not the user.
  const HOME_KEYS = new Set(['Home', 'BrowserHome', 'GoHome']);

  // Home's nav cards are its primary controls, and Music is the first of them.
  // The ring belongs there rather than on the utility cluster (clock, version
  // pill, search) that precedes the cards in the DOM.
  const homeCard = () => document.querySelector('.card-grid a.card');

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
    // Home goes straight to the landing page and lands the ring on the Music
    // card, from anywhere and in one press — no ladder, unlike Back. An open
    // overlay is dismissed on the way out rather than left floating over home.
    // Skipped while typing (Home moves the caret) and on an engaged control
    // (it owns its keys until released).
    if (HOME_KEYS.has(e.key) && !typing && !isEngaged(target)) {
      html.classList.remove('using-mouse');
      e.preventDefault();
      const overlay = openOverlay();
      if (overlay) {
        const dismiss = overlay.querySelector('[data-couch-dismiss]');
        if (dismiss) dismiss.click();
      }
      // Already home: no navigation (it would stack a duplicate history entry) —
      // just move the ring, which is the whole point of the press. Otherwise the
      // Turbo visit lands there and focusFirst anchors the card.
      if (window.location.pathname === '/') {
        const card = homeCard();
        if (card) card.focus();
        return;
      }
      if (window.Turbo && typeof window.Turbo.visit === 'function') window.Turbo.visit('/');
      else window.location.href = '/';
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
      // Terminal stage: Back is a Home shortcut — go to the landing page in one
      // press (the breadcrumb is the way UP one level). EVERY back key reaches
      // this, Escape included, so the remote behaves the same regardless of
      // which keycode the dongle emits. Turbo.visit keeps the persistent audio
      // player alive, matching every other nav; a full load is the fallback if
      // Turbo isn't present. Already on Home → no-op, so repeated presses don't
      // stack duplicate Home history entries.
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
    // Never fight an anchor another controller already set (the media players
    // focus their play/pause button at boot). Today couch.js's listener runs
    // first so this is a no-op, but the guard makes the outcome independent of
    // turbo:load listener order. A fresh Turbo body swap always starts with
    // focus on <body>, so ordinary pages are unaffected.
    const ae = document.activeElement;
    if (ae && ae !== document.body && ae !== document.documentElement) return;
    // A page with a subtab bar (Music / TV / Movies) lands the ring on its
    // active tab — arriving from a home card starts you on "Recent", not
    // whatever control happens to be first in the content. Gated on input
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
    // Home anchors on the Music card explicitly: the generic first-in-<main>
    // rule below would land on the utility cluster (version pill), which sits
    // above the cards in the DOM.
    if (isHome) {
      const card = homeCard();
      if (card) { card.focus(); return; }
    }
    const all = candidates();
    if (!all.length) return;
    // Prefer the first control INSIDE the content (<main>) over the shell chrome
    // that precedes it in the DOM (the floating now-playing / resume chips) and
    // the breadcrumb — otherwise the ring lands on a chip or a crumb rather than
    // the page's own controls, and the remote user arrow-hunts into content. The
    // chips and breadcrumb stay reachable by arrowing to them. Fall back to the
    // old first-non-breadcrumb, then anything.
    const main = document.querySelector('main');
    const inContent = (el) => !!main && main.contains(el) && !el.closest('.breadcrumb');
    const first = all.find(inContent) || all.find((el) => !el.closest('.breadcrumb')) || all[0];
    first.focus();
  };
  document.addEventListener('turbo:load', focusFirst);
})();
