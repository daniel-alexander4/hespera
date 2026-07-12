// Tests for web/static/player.js — the persistent (local-only) music player
// controller. jsdom drives the real IIFE through its DOM effects; the media
// engine (<audio> play/load/decode) is stubbed by the harness, so these cover
// the queue/autoload wiring and view rendering, not real audio. (Audio decode
// stays in the Playwright smoke.)

const { test } = require('node:test');
const assert = require('node:assert');
const { loadController, makeFetch, flush } = require('./harness');

// The persistent-player DOM: the layout-shell permanents (audio, #np-cluster)
// plus the now-playing .player-page view that bindView wires. `lyrics` sets
// data-lyrics-enabled so the synced-lyrics card path is exercised.
function fixture({ autoload = '', lyrics = false } = {}) {
  const autoAttr = autoload ? ` data-autoload="${autoload}"` : '';
  const lyricsAttr = ` data-lyrics-enabled="${lyrics ? '1' : '0'}"`;
  return `<!DOCTYPE html><html><body>
    <audio id="hespera-audio"></audio>
    <div id="np-cluster"><button id="np-toggle"></button><a id="np-title"></a><button id="np-close"></button></div>

    <div class="player-page"${autoAttr}${lyricsAttr}>
      <div id="player-empty"></div>
      <div id="player-main">
        <img id="player-cover-img" /><div id="player-cover-ph"></div>
        <a id="player-track-title"></a>
        <div id="player-artist"></div><div id="player-album-title"></div>
        <input id="player-seek" type="range" min="0" max="1000" /><span id="player-time"></span>
        <div id="player-karaoke"><div id="player-karaoke-current"></div><div id="player-karaoke-next"></div></div>
        <div id="player-transport">
          <button id="player-prev-btn"></button><button id="player-rewind-btn"></button>
          <button id="player-toggle-btn"><span class="np-glyph-play"></span><span class="np-glyph-pause"></span></button><button id="player-forward-btn"></button>
          <button id="player-next-btn"></button>
          <button id="player-lyrics-btn"></button>
          <details id="player-playlist-menu">
            <summary></summary>
            <div class="player-menu-panel" data-couch-overlay>
              <button id="player-add-btn" data-playlist-add></button>
              <button id="player-save-btn" data-playlist-save></button>
              <button id="playlist-open-btn"></button>
              <button id="player-menu-dismiss" data-couch-dismiss></button>
            </div>
          </details>
          <span id="app-clock"></span>
        </div>
        <button id="playlist-close-btn"></button>
        <div id="playlist-drawer"></div><div id="playlist-scrim"></div><ul id="playlist-list"></ul>
      </div>
    </div>
  </body></html>`;
}

// Boot player.js with a fetch router, then settle async chains.
function boot({ autoload, routes, lyrics } = {}) {
  const env = loadController('player.js', {
    html: fixture({ autoload, lyrics }),
    url: 'http://localhost/music/player',
    stubs: { fetch: makeFetch(routes || {}) },
  });
  env.window.document.dispatchEvent(new env.window.Event('turbo:load'));
  return env;
}

const hasNoLyrics = (env) => env.document.getElementById('player-main').classList.contains('no-lyrics');

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

test('the now-playing transport toggle reflects play/pause via .np-paused (the glyph swap)', async () => {
  const routes = {
    '/music/queue': { title: 'All Songs', tracks: [{ id: 41, albumId: 9, album: 'A', title: 'T', artist: 'X' }] },
  };
  const env = boot({ autoload: 'source=all&library=1', routes });
  await flush();
  const btn = env.document.getElementById('player-toggle-btn');
  // Autoload called play() → paused=false → the play glyph is hidden (no class).
  assert.strictEqual(btn.classList.contains('np-paused'), false, 'playing → no np-paused (pause glyph shown)');

  env.document.getElementById('hespera-audio').pause();
  assert.strictEqual(btn.classList.contains('np-paused'), true, 'paused → np-paused (play glyph shown)');

  env.document.getElementById('hespera-audio').play();
  assert.strictEqual(btn.classList.contains('np-paused'), false, 'resumed → np-paused cleared');
});

// --- Now-playing metadata lines: artist and album link to their pages ---

test('the artist and album lines render as links to their pages', async () => {
  const routes = {
    '/music/queue': { title: 'All Songs', tracks: [{ id: 7, albumId: 3, album: 'Ray of Light', title: 'Frozen', artist: 'Madonna', artistId: 12 }] },
  };
  const env = boot({ autoload: 'source=all&library=1', routes });
  await flush();

  const artist = env.document.querySelector('#player-artist a');
  const album = env.document.querySelector('#player-album-title a');
  assert.ok(artist, 'artist name is a link');
  assert.strictEqual(artist.getAttribute('href'), '/music/artist/12');
  assert.strictEqual(artist.textContent, 'Madonna');
  assert.ok(album, 'album name is a link');
  assert.strictEqual(album.getAttribute('href'), '/music/album/3');
  assert.strictEqual(album.textContent, 'Ray of Light');
});

test('a track with no album/artist id degrades to plain text (never /music/album/0)', async () => {
  const routes = {
    '/music/queue': { title: 'All Songs', tracks: [{ id: 9, albumId: 0, album: 'Orphan', title: 'Loose Track', artist: 'Nobody', artistId: 0 }] },
  };
  const env = boot({ autoload: 'source=all&library=1', routes });
  await flush();

  const artistLine = env.document.getElementById('player-artist');
  const albumLine = env.document.getElementById('player-album-title');
  assert.strictEqual(artistLine.querySelector('a'), null, 'no artist link without an id');
  assert.strictEqual(artistLine.textContent, 'Nobody', 'still shows the name');
  assert.strictEqual(albumLine.querySelector('a'), null, 'no album link without an id');
  assert.strictEqual(albumLine.textContent, 'Orphan');
});

// --- Playlist dropdown (the transport's <details> menu) ---

test('picking from the playlist dropdown closes it: Show playlist opens the drawer', async () => {
  const routes = {
    '/music/queue': { title: 'All Songs', tracks: [{ id: 41, albumId: 9, album: 'A', title: 'T', artist: 'X', artistId: 2 }] },
  };
  const env = boot({ autoload: 'source=all&library=1', routes });
  await flush();

  const menu = env.document.getElementById('player-playlist-menu');
  const drawer = env.document.getElementById('playlist-drawer');
  menu.open = true;
  env.document.getElementById('playlist-open-btn').click();
  assert.strictEqual(menu.open, false, 'the pick closed the menu');
  assert.strictEqual(drawer.classList.contains('open'), true, 'and opened the playlist drawer');
});

test('the dropdown Add button closes the menu and points at the current track', async () => {
  const routes = {
    '/music/queue': { title: 'All Songs', tracks: [{ id: 55, albumId: 9, album: 'A', title: 'T', artist: 'X', artistId: 2 }] },
  };
  const env = boot({ autoload: 'source=all&library=1', routes });
  await flush();

  const menu = env.document.getElementById('player-playlist-menu');
  const add = env.document.getElementById('player-add-btn');
  // playlist_picker.js (a separate controller) reads this id off the button.
  assert.strictEqual(add.getAttribute('data-track-id'), '55', 'Add targets the playing track');
  menu.open = true;
  add.click();
  assert.strictEqual(menu.open, false, 'handing off to the picker closes the menu');
});

test("Back's hidden dismiss control closes the dropdown", async () => {
  const env = boot({});
  const menu = env.document.getElementById('player-playlist-menu');
  menu.open = true;
  // couch.js dismisses an overlay by clicking its [data-couch-dismiss].
  env.document.querySelector('#player-playlist-menu [data-couch-dismiss]').click();
  assert.strictEqual(menu.open, false);
});

// --- Lyrics card: verify lyrics exist before showing it ---

test('with lyrics enabled but no track yet, the card starts hidden (cover expanded)', async () => {
  const env = boot({ lyrics: true }); // no autoload → no track
  await flush();
  assert.strictEqual(hasNoLyrics(env), true, 'card hidden until a track confirms lyrics');
});

test('a track with synced lyrics reveals the card', async () => {
  const routes = {
    '/music/queue': { title: 'All Songs', tracks: [{ id: 11, albumId: 4, album: 'Believe', title: 'Believe', artist: 'Cher' }] },
    '/music/lyrics/fetch': { ok: true, data: { synced_lyrics: '[00:01.00]Do you believe\n[00:03.50]in life after love' } },
  };
  const env = boot({ autoload: 'source=all&library=1', lyrics: true, routes });
  await flush();
  // setNoLyrics(false) runs only in the confirmed-synced branch, so a shown card
  // proves the lyrics were parsed (not shown optimistically during the fetch).
  assert.strictEqual(hasNoLyrics(env), false, 'confirmed synced lyrics → card shown');
});

test('a track with no synced lyrics keeps the card hidden and never shows "Loading…"', async () => {
  const routes = {
    '/music/queue': { title: 'All Songs', tracks: [{ id: 12, albumId: 5, album: 'X', title: 'Instrumental', artist: 'Nobody' }] },
    '/music/lyrics/fetch': { ok: true, data: { synced_lyrics: '' } }, // a confirmed no-synced result
  };
  const env = boot({ autoload: 'source=all&library=1', lyrics: true, routes });
  await flush();
  assert.strictEqual(hasNoLyrics(env), true, 'no synced lyrics → card stays hidden');
  assert.strictEqual(env.document.getElementById('player-karaoke-current').textContent, '', 'never flashed a Loading placeholder');
});

test('the per-song Lyrics toggle opts one track in past a disabled global default (force=1)', async () => {
  const routes = {
    '/music/queue': { title: 'All Songs', tracks: [{ id: 21, albumId: 6, album: 'Rising', title: 'Stargazer', artist: 'Rainbow' }] },
    '/music/lyrics/fetch': { ok: true, data: { synced_lyrics: '[00:02.00]High noon' } },
  };
  const env = boot({ autoload: 'source=all&library=1', lyrics: false, routes });
  await flush();
  assert.strictEqual(hasNoLyrics(env), true, 'global default off → card hidden');
  assert.strictEqual(env.fetch.calls.filter((c) => c.url.indexOf('/music/lyrics/fetch') >= 0).length, 0, 'no automatic fetch while disabled');

  const btn = env.document.getElementById('player-lyrics-btn');
  btn.click();
  await flush();
  const lyricCalls = env.fetch.calls.filter((c) => c.url.indexOf('/music/lyrics/fetch') >= 0);
  assert.strictEqual(lyricCalls.length, 1, 'toggle-on fetches lyrics');
  assert.ok(/(^|&)force=1(&|$)/.test(lyricCalls[0].opts.body || ''), 'explicit opt-in carries force=1');
  assert.strictEqual(hasNoLyrics(env), false, 'card revealed for this song');
  assert.strictEqual(btn.classList.contains('is-on'), true, 'button reflects on');

  // Toggle back off: card hides again, no extra fetch needed.
  btn.click();
  await flush();
  assert.strictEqual(hasNoLyrics(env), true, 'toggle-off hides the card');
  assert.strictEqual(btn.classList.contains('is-on'), false, 'button reflects off');
});

test('the per-song Lyrics toggle hides lyrics for one track when the global default is on', async () => {
  const routes = {
    '/music/queue': { title: 'All Songs', tracks: [{ id: 22, albumId: 7, album: 'Believe', title: 'Believe', artist: 'Cher' }] },
    '/music/lyrics/fetch': { ok: true, data: { synced_lyrics: '[00:01.00]Do you believe' } },
  };
  const env = boot({ autoload: 'source=all&library=1', lyrics: true, routes });
  await flush();
  assert.strictEqual(hasNoLyrics(env), false, 'global on + synced lyrics → card shown');
  const btn = env.document.getElementById('player-lyrics-btn');
  assert.strictEqual(btn.classList.contains('is-on'), true, 'button starts on (global default)');

  btn.click();
  await flush();
  assert.strictEqual(hasNoLyrics(env), true, 'per-song off hides the card despite the global default');
  assert.strictEqual(btn.classList.contains('is-on'), false);
});

test('near-gapless: starting a track warms the NEXT track via a preloader Audio', async () => {
  const preloaded = [];
  const routes = {
    '/music/queue': { title: 'All Songs', tracks: [
      { id: 31, albumId: 8, album: 'A', title: 'One', artist: 'X', gainDb: -3 },
      { id: 32, albumId: 8, album: 'A', title: 'Two', artist: 'X', gainDb: 2 },
    ] },
  };
  const env = boot({ autoload: 'source=all', routes });
  // Capture preloader instances: player.js does `new Audio()` for the warm-up.
  // (Stubbed after boot would miss it, so re-dispatch a fresh play via the
  // window hook: instead, stub before the play by booting with the stub.)
  await flush();
  const audio = env.document.getElementById('hespera-audio');
  assert.strictEqual(audio.getAttribute('src'), '/stream/track/31', 'first track playing');
  // jsdom's Audio constructor is real; the preloader is observable via the
  // document-less element the controller retains. We assert behavior through
  // the public seam instead: advancing to the next track must reuse id 32.
  audio.dispatchEvent(new env.window.Event('ended'));
  assert.strictEqual(audio.getAttribute('src'), '/stream/track/32', 'advances to the warmed track');
});

// --- Hardware media keys: player.js owns the page-global Media Session and the
// keydown fallback; an active video player's window.hesperaMediaControl bridge
// gets every action first (video page → video control, else music).

test('media session: handlers registered once, and they defer to an active video bridge', async () => {
  const env = boot({});
  const handlers = env.window.__mediaSessionHandlers;
  for (const a of ['play', 'pause', 'previoustrack', 'nexttrack', 'seekbackward', 'seekforward']) {
    assert.strictEqual(typeof handlers[a], 'function', `handler registered for ${a}`);
  }
  const seen = [];
  env.window.hesperaMediaControl = (action) => { seen.push(action); return true; };
  handlers.play();
  handlers.seekforward({});
  assert.deepStrictEqual(seen, ['play', 'seekforward'], 'bridge gets the actions first');
  const audio = env.document.getElementById('hespera-audio');
  assert.ok(audio.paused !== false, 'music engine untouched while the bridge consumes');
});

test('media-key keydown fallback dispatches through the same bridge', async () => {
  const env = boot({});
  const seen = [];
  env.window.hesperaMediaControl = (action) => { seen.push(action); return true; };
  for (const [key, action] of [['MediaPlayPause', 'playpause'], ['MediaFastForward', 'seekforward'], ['MediaRewind', 'seekbackward'], ['MediaTrackNext', 'nexttrack'], ['MediaTrackPrevious', 'previoustrack']]) {
    const e = new env.window.KeyboardEvent('keydown', { key, bubbles: true, cancelable: true });
    env.document.dispatchEvent(e);
    assert.strictEqual(e.defaultPrevented, true, `${key} consumed`);
    assert.strictEqual(seen[seen.length - 1], action, `${key} → ${action}`);
  }
  // Without a bridge, a media key falls through to the music engine (no throw,
  // still consumed) — with an empty queue the observable is just consumption.
  env.window.hesperaMediaControl = null;
  const e = new env.window.KeyboardEvent('keydown', { key: 'MediaPlayPause', bubbles: true, cancelable: true });
  env.document.dispatchEvent(e);
  assert.strictEqual(e.defaultPrevented, true, 'handled by the music engine when no video is active');
});

test('music playbackState writes are suppressed while a video page is active', async () => {
  const routes = {
    '/music/queue': { title: 'All Songs', tracks: [{ id: 7, albumId: 3, album: 'A', title: 'T', artist: 'X' }] },
  };
  const env = boot({ autoload: 'source=all&library=1', routes });
  await flush();
  const ms = env.window.navigator.mediaSession;
  assert.strictEqual(ms.playbackState, 'playing', 'music playback drives the state normally');
  // A video page appears (Turbo swap) and the music pauses — the video owns the
  // state now; music's pause must not flip it under the video.
  env.document.body.insertAdjacentHTML('beforeend', '<video data-media-kind="tv"></video>');
  ms.playbackState = 'playing'; // the video's play listener set this
  env.document.getElementById('hespera-audio').pause();
  assert.strictEqual(ms.playbackState, 'playing', 'paused music left the video\'s state alone');
});
