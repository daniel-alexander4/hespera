// sw.js — install shim and offline fallback for the remote.
//
// NETWORK-FIRST, deliberately. The obvious choice for a PWA shell is
// cache-first, and it is wrong here: this app is a remote for a box on the same
// LAN, so it is useless without that box — there is no offline mode worth
// optimising for. What cache-first buys is a stale shell. It bit immediately in
// testing: a rebuilt binary with new CSS kept serving the old stylesheet from
// the worker's cache, and every future asset change would silently need a
// CACHE bump to land. Freshness is worth more than a few ms of launch time.
//
// The cache is kept only as a fallback so the app still opens (and can say the
// box is unreachable) when the network is gone.
const CACHE = 'hesplay-remote-v1';

const SHELL = [
  '.',
  'index.html',
  'app.css',
  'app.js',
  'manifest.webmanifest',
  'icons/icon-192.png',
  'icons/icon-512.png',
];

self.addEventListener('install', (e) => {
  // Take over immediately rather than waiting for every tab to close — an
  // updated binary should not be shadowed by a worker from the previous one.
  self.skipWaiting();
  e.waitUntil(caches.open(CACHE).then((c) => c.addAll(SHELL)));
});

self.addEventListener('activate', (e) => {
  e.waitUntil(
    caches.keys()
      .then((keys) => Promise.all(keys.filter((k) => k !== CACHE).map((k) => caches.delete(k))))
      .then(() => self.clients.claim())
  );
});

self.addEventListener('fetch', (e) => {
  if (e.request.method !== 'GET') return; // every control call is a POST — pass it straight through
  const url = new URL(e.request.url);

  // The API and artwork are never cached and never fall back: a remote that
  // renders a stale "now playing" from disk, or quietly swallows a failed
  // /api/next, is worse than one that shows an error.
  if (url.pathname.startsWith('/api/') || url.pathname.startsWith('/art/')) return;

  e.respondWith(
    fetch(e.request)
      .then((res) => {
        if (res && res.ok) {
          const copy = res.clone();
          caches.open(CACHE).then((c) => c.put(e.request, copy)).catch(() => {});
        }
        return res;
      })
      .catch(() => caches.match(e.request).then((hit) => hit || Promise.reject(new Error('offline'))))
  );
});
