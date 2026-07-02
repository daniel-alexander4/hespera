// Tests for web/static/player.js — the persistent music player controller,
// focused on the dual-engine YouTube behaviour: the resume-across-Turbo-nav fix
// and the album-less cover guard (both shipped in 2a3d39f), plus the autoload
// guard. jsdom drives the real IIFE through its DOM effects; the YouTube IFrame
// player is mocked so we can inspect loadVideoById calls and simulate the
// onReady the iframe re-fires when Turbo reparents it. (Real iframe reload and
// audio decode stay in the Playwright smoke.)

const { test } = require('node:test');
const assert = require('node:assert');
const { loadController, makeFetch, flush } = require('./harness');

// A mock YouTube IFrame player. player.js keeps the instance in a closure var and
// calls its methods; we capture every loadVideoById argument and expose the
// events map so a test can fire onReady (what the API does after the iframe
// (re)connects).
function makeMockYT() {
  const instances = [];
  class MockYTPlayer {
    constructor(id, opts) {
      this.id = id;
      this.events = (opts && opts.events) || {};
      this.loads = [];   // every loadVideoById arg, in order
      this.state = 1;    // 1 = playing
      this.time = 0;
      instances.push(this);
    }
    loadVideoById(arg) { this.loads.push(arg); }
    getCurrentTime() { return this.time; }
    getDuration() { return 200; }
    getPlayerState() { return this.state; }
    getVideoData() { return { video_id: 'vid', title: 'Title' }; }
    seekTo() {}
    playVideo() { this.state = 1; }
    pauseVideo() { this.state = 2; }
  }
  const YT = { Player: MockYTPlayer, PlayerState: { PLAYING: 1, PAUSED: 2, ENDED: 0 } };
  YT.instances = instances;
  return YT;
}

// The persistent-player DOM: the layout-shell permanents (audio, #np-cluster,
// #yt-host) plus the now-playing .player-page view that bindView wires. A
// data-haskey .js-yt button is the deterministic entry into YouTube mode.
function fixture({ autoload = '' } = {}) {
  const autoAttr = autoload ? ` data-autoload="${autoload}"` : '';
  return `<!DOCTYPE html><html><body>
    <audio id="hespera-audio"></audio>
    <div id="np-cluster"><button id="np-toggle"></button><a id="np-title"></a><button id="np-close"></button></div>
    <div id="yt-host"><div id="yt-player"></div></div>

    <button class="js-yt" data-haskey="1" data-artist="Cher" data-song="Believe">YT</button>

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

// Boot player.js with a YT mock + a fetch router, then settle async chains.
function boot({ autoload, routes, YT } = {}) {
  const ytMock = YT || makeMockYT();
  const env = loadController('player.js', {
    html: fixture({ autoload }),
    url: 'http://localhost/music/player',
    stubs: { YT: ytMock, fetch: makeFetch(routes || {}) },
  });
  env.YT = ytMock;
  env.window.document.dispatchEvent(new env.window.Event('turbo:load'));
  return env;
}

// Drive a .js-yt one-off into YouTube mode and return the created mock player.
async function startYouTube(env) {
  env.document.querySelector('.js-yt').dispatchEvent(new env.window.MouseEvent('click', { bubbles: true }));
  await flush();
  assert.strictEqual(env.YT.instances.length, 1, 'a YT player was created');
  return env.YT.instances[0];
}

test('a resolved un-owned song starts on the YouTube engine', async () => {
  const env = boot({ routes: { '/music/youtube/resolve': { videoId: 'BELIEVE0001' } } });
  const yt = await startYouTube(env);
  yt.events.onReady(); // the API fires this once connected
  assert.deepStrictEqual(yt.loads, ['BELIEVE0001'], 'first play loads the bare videoId (from position 0)');
});

test('a Turbo nav resumes the YouTube track at its position, not 0', async () => {
  const env = boot({ routes: { '/music/youtube/resolve': { videoId: 'BELIEVE0001' } } });
  const yt = await startYouTube(env);
  yt.events.onReady(); // initial connect → bare load
  assert.deepStrictEqual(yt.loads[0], 'BELIEVE0001');

  // Simulate the mid-track nav: player is 42s in and playing.
  yt.time = 42;
  yt.state = 1;
  env.document.dispatchEvent(new env.window.Event('turbo:before-render'));
  // The reparented iframe reloads and the API re-fires onReady. (Assert fields
  // individually: loads[1] is built inside player.js's jsdom realm, so a whole-
  // object deepStrictEqual trips on the cross-realm prototype.)
  yt.events.onReady();
  assert.strictEqual(yt.loads[1].videoId, 'BELIEVE0001', 'reconnect reloads the same video');
  assert.strictEqual(yt.loads[1].startSeconds, 42, 'and resumes at the captured position');
});

test('the resume offset is consumed once, so a later reconnect starts clean', async () => {
  const env = boot({ routes: { '/music/youtube/resolve': { videoId: 'BELIEVE0001' } } });
  const yt = await startYouTube(env);
  yt.events.onReady();

  yt.time = 42; yt.state = 1;
  env.document.dispatchEvent(new env.window.Event('turbo:before-render'));
  yt.events.onReady(); // resumes at 42, then clears the offset

  yt.events.onReady(); // a second reconnect with no intervening nav
  assert.deepStrictEqual(yt.loads[2], 'BELIEVE0001', 'no stale offset — loads bare');
});

test('a paused/ended track is not captured for resume', async () => {
  const env = boot({ routes: { '/music/youtube/resolve': { videoId: 'BELIEVE0001' } } });
  const yt = await startYouTube(env);
  yt.events.onReady();

  yt.time = 88; yt.state = 0; // ended
  env.document.dispatchEvent(new env.window.Event('turbo:before-render'));
  yt.events.onReady();
  assert.deepStrictEqual(yt.loads[1], 'BELIEVE0001', 'ended track → no resume offset, loads bare');
});

test('an album-less YouTube track shows the placeholder, never /art/album/0', async () => {
  const env = boot({ routes: { '/music/youtube/resolve': { videoId: 'BELIEVE0001' } } });
  await startYouTube(env); // .js-yt has no data-art → coverUrl stays ''
  const img = env.document.getElementById('player-cover-img');
  const ph = env.document.getElementById('player-cover-ph');
  assert.strictEqual(img.classList.contains('hidden'), true, 'cover img hidden for an art-less YT track');
  assert.strictEqual(ph.classList.contains('hidden'), false, 'placeholder shown');
  assert.ok(!/\/art\/album\/0\b/.test(img.getAttribute('src') || ''), 'never requests /art/album/0');
});

test('a params-bearing player page autoloads its queue exactly once', async () => {
  const routes = {
    '/music/queue': { title: 'Top 100', tracks: [{ kind: 'yt', title: 'Believe', artist: 'Cher' }] },
    '/music/youtube/resolve': { videoId: 'BELIEVE0001' },
  };
  const env = boot({ autoload: 'source=top100&y=1999', routes });
  await flush();
  const queueCalls = env.fetch.calls.filter((c) => c.url.indexOf('/music/queue') >= 0);
  assert.strictEqual(queueCalls.length, 1, 'autoload fetched the queue');
  assert.ok(queueCalls[0].url.indexOf('source=top100') >= 0, 'with the page params');

  // A repeat turbo:load with the same data-autoload must NOT refetch (the guard
  // that stops a back/restore visit from restarting the current track).
  env.document.dispatchEvent(new env.window.Event('turbo:load'));
  await flush();
  const after = env.fetch.calls.filter((c) => c.url.indexOf('/music/queue') >= 0);
  assert.strictEqual(after.length, 1, 'same params → no reload');
});
