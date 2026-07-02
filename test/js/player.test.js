// Tests for web/static/player.js — the persistent (local-only) music player
// controller. jsdom drives the real IIFE through its DOM effects; the media
// engine (<audio> play/load/decode) is stubbed by the harness, so these cover
// the queue/autoload wiring and view rendering, not real audio. (Audio decode
// stays in the Playwright smoke.)

const { test } = require('node:test');
const assert = require('node:assert');
const { loadController, makeFetch, flush } = require('./harness');

// The persistent-player DOM: the layout-shell permanents (audio, #np-cluster)
// plus the now-playing .player-page view that bindView wires.
function fixture({ autoload = '' } = {}) {
  const autoAttr = autoload ? ` data-autoload="${autoload}"` : '';
  return `<!DOCTYPE html><html><body>
    <audio id="hespera-audio"></audio>
    <div id="np-cluster"><button id="np-toggle"></button><a id="np-title"></a><button id="np-close"></button></div>

    <div class="player-page"${autoAttr}>
      <div id="player-empty"></div>
      <div id="player-main">
        <img id="player-cover-img" /><div id="player-cover-ph"></div>
        <div id="player-album-title"></div><a id="player-track-title"></a>
        <input id="player-seek" type="range" min="0" max="1000" /><span id="player-time"></span>
        <div id="player-karaoke-current"></div><div id="player-karaoke-next"></div>
        <div id="player-transport">
          <button id="player-prev-btn"></button><button id="player-rewind-btn"></button>
          <button id="player-toggle-btn"></button><button id="player-forward-btn"></button>
          <button id="player-next-btn"></button>
        </div>
        <button id="playlist-open-btn"></button><button id="playlist-close-btn"></button>
        <div id="playlist-drawer"></div><div id="playlist-scrim"></div><ul id="playlist-list"></ul>
      </div>
    </div>
  </body></html>`;
}

// Boot player.js with a fetch router, then settle async chains.
function boot({ autoload, routes } = {}) {
  const env = loadController('player.js', {
    html: fixture({ autoload }),
    url: 'http://localhost/music/player',
    stubs: { fetch: makeFetch(routes || {}) },
  });
  env.window.document.dispatchEvent(new env.window.Event('turbo:load'));
  return env;
}

test('a params-bearing player page autoloads its queue exactly once and starts the local track', async () => {
  const routes = {
    '/music/queue': { title: 'All Songs', tracks: [{ id: 7, albumId: 3, album: 'Ray of Light', title: 'Frozen', artist: 'Madonna' }] },
  };
  const env = boot({ autoload: 'source=all&library=1', routes });
  await flush();

  const queueCalls = env.fetch.calls.filter((c) => c.url.indexOf('/music/queue') >= 0);
  assert.strictEqual(queueCalls.length, 1, 'autoload fetched the queue');
  assert.ok(queueCalls[0].url.indexOf('source=all') >= 0, 'with the page params');

  const audio = env.document.getElementById('hespera-audio');
  assert.strictEqual(audio.getAttribute('src'), '/stream/track/7', 'playback started on the local <audio>');

  // A repeat turbo:load with the same data-autoload must NOT refetch (the guard
  // that stops a back/restore visit from restarting the current track).
  env.document.dispatchEvent(new env.window.Event('turbo:load'));
  await flush();
  const after = env.fetch.calls.filter((c) => c.url.indexOf('/music/queue') >= 0);
  assert.strictEqual(after.length, 1, 'same params → no reload');
});

test('an album-less track shows the placeholder, never /art/album/0', async () => {
  const routes = {
    '/music/queue': { title: 'All Songs', tracks: [{ id: 9, albumId: 0, album: '', title: 'Loose Track', artist: 'Nobody' }] },
  };
  const env = boot({ autoload: 'source=all&library=1', routes });
  await flush();

  const img = env.document.getElementById('player-cover-img');
  const ph = env.document.getElementById('player-cover-ph');
  assert.strictEqual(img.classList.contains('hidden'), true, 'cover img hidden for an album-less track');
  assert.strictEqual(ph.classList.contains('hidden'), false, 'placeholder shown');
  assert.ok(!/\/art\/album\/0\b/.test(img.getAttribute('src') || ''), 'never requests /art/album/0');
});
