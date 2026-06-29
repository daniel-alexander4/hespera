// In-app YouTube playback for un-owned songs on the "Rediscover a Year" page.
// A [data-yt]/.js-yt play button resolves the song to a YouTube video via
// /music/youtube/resolve (cache-first, server-side key) and plays it in the
// #yt-modal overlay. With no key (or no embeddable hit), the resolver returns a
// searchUrl and we open YouTube in a new tab instead.
//
// One delegated document listener, bound once (the inline include re-runs on
// every Turbo render). Element lookups are live so they survive body swaps; the
// iframe is torn down on close and before Turbo caches the page (stops audio).
(function () {
  function modal() {
    return document.getElementById('yt-modal');
  }

  function close() {
    const m = modal();
    if (!m) return;
    const frame = document.getElementById('yt-frame');
    if (frame) frame.innerHTML = ''; // removing the iframe stops playback
    m.classList.add('hidden');
  }

  function open(artist, song) {
    const m = modal();
    if (!m) return;
    // Starting YouTube pauses the persistent local audio, like any other media.
    const audio = document.getElementById('hespera-audio');
    if (audio && !audio.paused) audio.pause();

    const titleEl = document.getElementById('yt-title');
    if (titleEl) titleEl.textContent = (artist ? artist + ' — ' : '') + song;

    const url =
      '/music/youtube/resolve?artist=' +
      encodeURIComponent(artist) +
      '&song=' +
      encodeURIComponent(song);
    fetch(url, { headers: { Accept: 'application/json' } })
      .then((r) => (r.ok ? r.json() : Promise.reject(new Error('resolve failed'))))
      .then((d) => {
        const frame = document.getElementById('yt-frame');
        if (d.embedUrl && frame) {
          frame.innerHTML =
            '<iframe width="100%" height="100%" src="' +
            d.embedUrl +
            '" title="YouTube player" frameborder="0" ' +
            'allow="autoplay; encrypted-media; picture-in-picture" allowfullscreen></iframe>';
          m.classList.remove('hidden');
        } else if (d.searchUrl) {
          window.open(d.searchUrl, '_blank', 'noopener');
        }
      })
      .catch(() => {});
  }

  if (!window.__ytBound) {
    window.__ytBound = true;
    document.addEventListener('click', (e) => {
      const t = e.target;
      if (!(t instanceof Element)) return;
      const play = t.closest('.js-yt');
      if (play) {
        e.preventDefault();
        open(play.getAttribute('data-artist') || '', play.getAttribute('data-song') || '');
        return;
      }
      const m = modal();
      if (!m || m.classList.contains('hidden')) return;
      // Close on the X (data-couch-dismiss in couch mode routes Back here too) or backdrop.
      if (t === m || t.closest('#yt-close')) close();
    });
    // Stop playback when navigating away (Turbo body swap).
    document.addEventListener('turbo:before-cache', close);
  }
})();
