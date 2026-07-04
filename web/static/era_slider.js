// era_slider.js — the shuffle-era range control (music home + Home Quick-Play,
// rendered from partials_era_picker.html).
//
// A draggable, resizable window over a year timeline: drag the body to slide the
// range, drag an edge to resize it (a wider window spans a decade+, a narrow one
// a few years). Remote/keyboard: the track is a [data-couch-capture] control on
// couch.js's shared engage protocol — merely focused it is TRANSPARENT to arrows
// (couch moves focus straight past; a slider that eats all four arrows is a
// one-way door); Enter engages it (couch sets data-couch-engaged + the accent
// glow), then ◀▶ slide the window and ▲▼ grow/shrink its span from the centre;
// Enter/Back/blur release (all owned by couch.js). This file only acts on the
// arrows while engaged; playing is the Play/Shuffle buttons beside the track.
//
// Pure client-side: it keeps the Play and Shuffle `[data-play]` links' hrefs in
// sync with the chosen range (/music/player?source=era&from=&to=, Shuffle adding
// &shuffle=1). The backend era query (buildPlayerQueue) is unchanged.
(function () {
  'use strict';

  const clamp = (v, lo, hi) => Math.max(lo, Math.min(hi, v));
  const inited = new WeakSet(); // idempotent across turbo:load without persisting into cached HTML

  function setup(picker) {
    if (inited.has(picker)) return;
    inited.add(picker);

    const min = parseInt(picker.dataset.min, 10);
    const max = parseInt(picker.dataset.max, 10);
    const lib = picker.dataset.lib || '';
    if (!Number.isFinite(min) || !Number.isFinite(max) || max < min) return;
    // The tape below builds one tick per year, so an absurd span (a garbage
    // year tag leaking through) would allocate DOM until the browser dies.
    // The server clamps years to a plausible window; this is the belt.
    if (max - min > 300) return;
    const span = max - min;
    // Years are bands, not points: the track is divided into (span + 1) equal
    // year-bands, so a single-year selection (from==to) is one band wide — a
    // visible gap — rather than a zero-width collapse.
    const denom = span + 1;

    const track = picker.querySelector('.era-track');
    const win = picker.querySelector('.era-window');
    const fromEl = picker.querySelector('.era-from');
    const toEl = picker.querySelector('.era-to');
    const play = picker.querySelector('.era-play');
    const shuffle = picker.querySelector('.era-shuffle');
    const tape = picker.querySelector('.era-tape');
    if (!track || !win || !shuffle) return;

    // Default to the most recent decade.
    let to = max;
    let from = clamp(max - 9, min, max);

    // pct(y) is the LEFT edge of year y's band. A from..to range covers whole
    // bands, so its right edge is pct(to + 1) — that trailing band is what makes
    // a single year visible.
    const pct = (year) => ((year - min) / denom) * 100;

    function render() {
      const l = pct(from);
      win.style.left = l + '%';
      win.style.width = pct(to + 1) - l + '%';
      if (fromEl) fromEl.textContent = String(from);
      if (toEl) toEl.textContent = String(to);
      track.setAttribute('aria-valuetext', from + ' to ' + to);
      // Play plays the era in order; Shuffle is the same queue with the
      // client-side shuffle param. Both keep the chosen from/to range.
      const base = '/music/player?source=era&from=' + from + '&to=' + to +
        (lib ? '&library=' + encodeURIComponent(lib) : '');
      if (play) play.setAttribute('href', base);
      shuffle.setAttribute('href', base + '&shuffle=1');
    }

    // Measuring-tape ticks behind the (transparent) window: a mark per year,
    // taller every 5, tallest + a decade number every 10 — always visible so the
    // range is targetable even when the window covers only a few years.
    if (tape) {
      tape.textContent = '';
      for (let y = min; y <= max; y += 1) {
        const tick = document.createElement('span');
        const kind = y % 10 === 0 ? 'major' : y % 5 === 0 ? 'mid' : 'minor';
        tick.className = 'era-tick era-tick--' + kind;
        tick.style.left = pct(y) + '%';
        tape.appendChild(tick);
        if (kind === 'major') {
          const label = document.createElement('span');
          label.className = 'era-tick-label';
          const px = pct(y);
          label.style.left = px + '%';
          // Keep the first/last decade numbers from overflowing the track edges.
          if (px <= 6) label.style.transform = 'translateX(0)';
          else if (px >= 94) label.style.transform = 'translateX(-100%)';
          label.textContent = String(y); // full year, e.g. 1970 (clearer than '70)
          tape.appendChild(label);
        }
      }
    }

    // Move both edges together, clamped so the window stays inside [min,max].
    function slide(delta) {
      const width = to - from;
      from = clamp(from + delta, min, max - width);
      to = from + width;
      render();
    }
    // Grow (delta>0) or shrink (delta<0) the span symmetrically about the centre;
    // never narrower than a single year, never past the ends.
    function resize(delta) {
      let nf = from - delta;
      let nt = to + delta;
      if (nt < nf) {
        const mid = Math.round((from + to) / 2);
        nf = nt = mid;
      }
      from = clamp(nf, min, max);
      to = clamp(nt, min, max);
      if (from > to) from = to;
      render();
    }

    // Engage/release (Enter, Back, blur) is couch.js's shared protocol — this
    // handler only acts on the arrows while the track carries the engaged
    // attribute, and stays transparent otherwise so couch can move focus past.
    track.addEventListener('keydown', (e) => {
      if (!track.hasAttribute('data-couch-engaged')) return;
      switch (e.key) {
        case 'ArrowLeft': slide(-1); break;
        case 'ArrowRight': slide(1); break;
        case 'ArrowUp': resize(1); break; // widen
        case 'ArrowDown': resize(-1); break; // narrow
        default: return; // Enter/Back release in couch.js; everything else bubbles
      }
      e.preventDefault();
      e.stopPropagation(); // keep the adjust arrows out of couch.js's focus mover
    });

    // Pointer: drag an edge handle to resize, the window body to slide, or click
    // bare track to jump the nearest edge. Geometry needs layout, so this path is
    // covered by the Playwright smoke, not the jsdom test.
    let drag = null;
    const yearAt = (clientX) => {
      const r = track.getBoundingClientRect();
      if (r.width === 0) return from;
      // Which year-band is the pointer over? floor into [min, max].
      return clamp(min + Math.floor(clamp((clientX - r.left) / r.width, 0, 1) * denom), min, max);
    };
    const applyDrag = (clientX) => {
      if (!drag) return;
      const y = yearAt(clientX);
      if (drag.mode === 'edge') {
        // The handles flank the window (inner edge = boundary), so the grabbed
        // year is offset from the handle centre; carry that offset so the edge
        // tracks the finger without a jump, and a single year is reachable.
        const b = y + drag.offset;
        if (drag.edge === 'l') from = clamp(b, min, to);
        else to = clamp(b, from, max);
      } else {
        const width = drag.width;
        from = clamp(drag.from + (y - drag.grab), min, max - width);
        to = from + width;
      }
      render();
    };
    track.addEventListener('pointerdown', (e) => {
      const edge = e.target.getAttribute && e.target.getAttribute('data-edge');
      if (edge === 'l' || edge === 'r') {
        drag = { mode: 'edge', edge, offset: (edge === 'l' ? from : to) - yearAt(e.clientX) };
      } else if (e.target === win || win.contains(e.target)) {
        drag = { mode: 'slide', grab: yearAt(e.clientX), from, width: to - from };
      } else {
        // Bare-track click: jump the nearest edge straight to the click (no offset).
        const y = yearAt(e.clientX);
        drag = { mode: 'edge', edge: Math.abs(y - from) <= Math.abs(y - to) ? 'l' : 'r', offset: 0 };
        applyDrag(e.clientX);
      }
      if (track.setPointerCapture) track.setPointerCapture(e.pointerId);
      track.focus();
      track.setAttribute('data-couch-engaged', ''); // click-then-arrows keeps working
      e.preventDefault();
    });
    track.addEventListener('pointermove', (e) => { if (drag) applyDrag(e.clientX); });
    const endDrag = () => { drag = null; };
    track.addEventListener('pointerup', endDrag);
    track.addEventListener('pointercancel', endDrag);

    render();
  }

  const init = () => document.querySelectorAll('.era-picker').forEach(setup);
  document.addEventListener('turbo:load', init);
})();
