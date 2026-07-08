// carousel.js — mouse chevrons for the horizontal card rows (.home-carousel-row).
// The native scrollbar is hidden at every scale (app.css); a mouse user pages
// the row with ‹ › chevrons overlaid on its edges, shown only when that
// direction has more content to reveal. Remote/keyboard users advance via
// couch.js focus (scrollIntoView), so the chevrons are mouse-only (CSS gates
// them on html.using-mouse + a display:none default) and never join the focus
// ring. Injected DOM is torn down on turbo:before-cache so a cached snapshot
// never double-wraps when the back button restores it.
(() => {
  const svg = (d) =>
    '<svg class="icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" ' +
    'stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' +
    '<path d="' + d + '"/></svg>';
  const CHEV = (dir) => {
    const b = document.createElement('button');
    b.type = 'button';
    b.className = 'carousel-chevron carousel-chevron-' + dir;
    b.tabIndex = -1;
    b.setAttribute('aria-hidden', 'true');
    b.innerHTML = svg(dir === 'prev' ? 'm15 18-6-6 6-6' : 'm9 18 6-6-6-6');
    return b;
  };

  const wire = (row) => {
    if (row.__carouselInit) return; // one controller per row
    row.__carouselInit = true;

    const wrap = document.createElement('div');
    wrap.className = 'carousel-wrap';
    row.parentNode.insertBefore(wrap, row);
    wrap.appendChild(row);
    const prev = CHEV('prev'), next = CHEV('next');
    wrap.appendChild(prev);
    wrap.appendChild(next);

    const page = (sign) => row.scrollBy({ left: sign * row.clientWidth * 0.85, behavior: 'smooth' });
    prev.addEventListener('click', () => page(-1));
    next.addEventListener('click', () => page(1));

    // Show a chevron only when the row can still scroll that way. Cards are
    // CSS-sized (min/max-width, flex-shrink:0), so scrollWidth is stable before
    // lazy images load — scroll + resize are enough, no image-load recompute.
    const update = () => {
      const max = row.scrollWidth - row.clientWidth;
      prev.classList.toggle('is-shown', row.scrollLeft > 1);
      next.classList.toggle('is-shown', row.scrollLeft < max - 1);
    };
    row.__carouselUpdate = update;
    row.addEventListener('scroll', update, { passive: true });
    window.addEventListener('resize', update, { passive: true });
    update();
    requestAnimationFrame(update); // recompute once layout has settled
  };

  const init = () => document.querySelectorAll('.home-carousel-row').forEach(wire);

  // Unwrap every carousel and drop its listeners so the pre-cache snapshot is
  // clean (the restored bare row re-wires fresh on the next turbo:load).
  const teardown = () => {
    document.querySelectorAll('.carousel-wrap').forEach((wrap) => {
      const row = wrap.querySelector('.home-carousel-row');
      if (row) {
        if (row.__carouselUpdate) {
          row.removeEventListener('scroll', row.__carouselUpdate);
          window.removeEventListener('resize', row.__carouselUpdate);
        }
        row.__carouselInit = false;
        wrap.parentNode.insertBefore(row, wrap);
      }
      wrap.remove();
    });
  };

  document.addEventListener('turbo:load', init);
  document.addEventListener('turbo:before-cache', teardown);
})();
