// book_reader.js — the EPUB/CBZ reading surface (#bookReader). PDF pages carry
// no JS at all: Chromium's own viewer owns them. Boots on turbo:load like the
// other controllers and tears its listeners down on turbo:before-cache.
//
// EPUB: one spine document at a time in a sandboxed same-origin iframe (its
// relative hrefs resolve against /book/asset/{id}/... natively); Prev/Next
// step chapters, and the scroll fraction inside the current one is tracked.
// CBZ: an image sequence; Prev/Next (or ←/→ on the focused stage) step pages.
//
// Progress beacons mirror media_player.js's reportProgress: 15s throttle,
// flush on step/unload/before-cache, completed reported ONLY when this session
// reached the end (the server upsert is earn-only, MAX(completed)).
(() => {
  'use strict';
  if (window.__hesperaBookReaderBound) return;
  window.__hesperaBookReaderBound = true;

  let teardown = null;

  function boot() {
    const root = document.getElementById('bookReader');
    if (!root || !root.dataset.kind || root.dataset.kind === 'pdf') return;

    const bookID = parseInt(root.dataset.bookId, 10) || 0;
    const kind = root.dataset.kind;
    let entries = [];
    try { entries = JSON.parse(root.dataset.entries || '[]'); } catch (e) { entries = []; }
    if (!bookID || !entries.length) return;

    const assetBase = `/book/asset/${bookID}/`;
    const frame = document.getElementById('bookFrame');
    const page = document.getElementById('bookPage');
    const stage = document.getElementById('bookStage');
    const posLabel = document.getElementById('bookPos');
    const prevBtn = document.getElementById('bookPrevBtn');
    const nextBtn = document.getElementById('bookNextBtn');

    let idx = Math.min(Math.max(parseInt(root.dataset.startIndex, 10) || 0, 0), entries.length - 1);
    let restoreFraction = parseFloat(root.dataset.startFraction) || 0;
    let lastReport = 0;
    let reachedEnd = false;

    const fraction = () => {
      if (kind !== 'epub' || !frame) return 0;
      try {
        const d = frame.contentDocument && frame.contentDocument.documentElement;
        if (!d) return 0;
        const span = d.scrollHeight - d.clientHeight;
        return span > 0 ? Math.min(1, Math.max(0, d.scrollTop / span)) : 0;
      } catch (e) { return 0; }
    };

    const report = (force) => {
      const now = Date.now();
      if (!force && now - lastReport < 15000) return;
      lastReport = now;
      const frac = fraction();
      if (idx === entries.length - 1 && (kind === 'cbz' || frac > 0.98)) reachedEnd = true;
      const body = JSON.stringify({
        book_id: bookID, spine_index: idx, scroll_fraction: frac, completed: reachedEnd,
      });
      if (navigator.sendBeacon) {
        navigator.sendBeacon('/book/reading-progress', new Blob([body], { type: 'application/json' }));
      } else {
        fetch('/book/reading-progress', { method: 'POST', body, headers: { 'Content-Type': 'application/json' } });
      }
    };

    const show = () => {
      if (posLabel) posLabel.textContent = `${idx + 1} / ${entries.length}`;
      if (prevBtn) prevBtn.disabled = idx <= 0;
      if (nextBtn) nextBtn.disabled = idx >= entries.length - 1;
      if (kind === 'epub' && frame) {
        frame.src = assetBase + entries[idx];
      } else if (page) {
        page.src = assetBase + entries[idx];
        if (stage) stage.scrollTop = 0;
      }
    };

    const step = (delta) => {
      const next = idx + delta;
      if (next < 0 || next >= entries.length) return;
      report(true); // flush the position we're leaving
      idx = next;
      restoreFraction = 0;
      show();
      report(true);
    };

    const onFrameLoad = () => {
      // Restore the stored scroll once, on the resumed chapter; every later
      // load starts at the top. Then watch scrolling for the throttled beacon.
      try {
        const doc = frame.contentDocument;
        if (!doc) return;
        if (restoreFraction > 0) {
          const d = doc.documentElement;
          d.scrollTop = restoreFraction * (d.scrollHeight - d.clientHeight);
          restoreFraction = 0;
        }
        doc.addEventListener('scroll', () => report(false), { passive: true });
      } catch (e) { /* cross-origin can't happen (same origin), but stay quiet */ }
    };
    if (frame) frame.addEventListener('load', onFrameLoad);

    const onPrev = () => step(-1);
    const onNext = () => step(1);
    if (prevBtn) prevBtn.addEventListener('click', onPrev);
    if (nextBtn) nextBtn.addEventListener('click', onNext);

    // ←/→ on the focused CBZ stage step pages; handled here (bubble reaches
    // this container before couch.js's document listener) and consumed so the
    // remote's arrows page the comic instead of moving the focus ring.
    const onStageKey = (e) => {
      if (e.key === 'ArrowLeft') { e.preventDefault(); e.stopPropagation(); step(-1); }
      else if (e.key === 'ArrowRight') { e.preventDefault(); e.stopPropagation(); step(1); }
    };
    if (stage) stage.addEventListener('keydown', onStageKey);

    const onUnload = () => report(true);
    window.addEventListener('beforeunload', onUnload);

    teardown = () => {
      report(true);
      window.removeEventListener('beforeunload', onUnload);
      if (frame) frame.removeEventListener('load', onFrameLoad);
      if (prevBtn) prevBtn.removeEventListener('click', onPrev);
      if (nextBtn) nextBtn.removeEventListener('click', onNext);
      if (stage) stage.removeEventListener('keydown', onStageKey);
      teardown = null;
    };

    show();
    if (stage) stage.focus();
  }

  document.addEventListener('turbo:load', boot);
  document.addEventListener('turbo:before-cache', () => { if (teardown) teardown(); });
  if (document.readyState !== 'loading') boot();
  else document.addEventListener('DOMContentLoaded', boot, { once: true });
})();
