// Play a charting song the user doesn't own on YouTube. A [data-yt]/.js-yt play
// button resolves the song to a video via /music/youtube/resolve (cache-first,
// server-side key) and opens it on YouTube in a new tab — the watch page
// autoplays. With no key (or no embeddable hit), the resolver returns a
// searchUrl and we open a YouTube search instead.
//
// One delegated document listener, bound once (the inline include re-runs on
// every Turbo render). The new tab is opened synchronously in the click gesture
// (so popup blockers allow it) and redirected once the lookup returns.
(function () {
  if (window.__ytBound) return;
  window.__ytBound = true;

  document.addEventListener('click', (e) => {
    const t = e.target;
    if (!(t instanceof Element)) return;
    const play = t.closest('.js-yt');
    if (!play) return;
    e.preventDefault();

    const artist = play.getAttribute('data-artist') || '';
    const song = play.getAttribute('data-song') || '';

    // Open the tab now, inside the user gesture, then point it at the resolved
    // video. (No noopener — we need the handle to redirect it.)
    const win = window.open('about:blank', '_blank');

    const url =
      '/music/youtube/resolve?artist=' +
      encodeURIComponent(artist) +
      '&song=' +
      encodeURIComponent(song);
    fetch(url, { headers: { Accept: 'application/json' } })
      .then((r) => (r.ok ? r.json() : Promise.reject(new Error('resolve failed'))))
      .then((d) => {
        const dest = d.watchUrl || d.searchUrl;
        if (win && dest) win.location = dest;
        else if (win) win.close();
      })
      .catch(() => {
        if (win) win.close();
      });
  });
})();
