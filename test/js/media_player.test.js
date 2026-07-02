// Tests for web/static/media_player.js — the shared TV/movie video controller.
//
// Table-driven where it fits, plain node:assert (mirroring the Go suite's style).
// jsdom drives the real controller through its DOM effects; the media engine is
// stubbed (see harness.js). Caption *cue* rendering isn't covered here — jsdom
// doesn't parse WebVTT into TextTrack cues — that path stays in the Playwright
// smoke.

const { test } = require('node:test');
const assert = require('node:assert');
const { loadController, makeMockHls, makeFetch, flush } = require('./harness');

// The player DOM the controller queries. Mirrors the ids/classes in
// tv_player.html / movie_player.html that media_player.js reaches for.
function fixture({ kind = 'tv', fileId = 7, nextFile = 0, osEnabled = '0' } = {}) {
  return `<!DOCTYPE html><html><body>
    <div class="tv-player-video-wrap">
      <video id="tvVideo" data-media-kind="${kind}" data-file-id="${fileId}" data-next-file="${nextFile}" data-os-enabled="${osEnabled}"></video>
      <div id="mediaOverlay">
        <div id="audioPick" hidden><select id="audioSelect"></select></div>
        <div id="subPick" hidden><select id="subSelect"></select></div>
        <div id="tvTransport">
          <button id="tvRewindBtn"></button>
          <button id="tvToggleBtn"></button>
          <button id="tvForwardBtn"></button>
          <button id="tvBoostBtn"><span class="tv-glyph-vol"></span><span class="tv-glyph-mute"></span></button>
          <button id="tvMuteBtn"><span class="tv-glyph-vol"></span><span class="tv-glyph-mute"></span></button>
          <input id="tvVolume" type="range" min="0" max="1" step="0.01" />
          <button id="tvFullscreenBtn"></button>
          <button id="skipAutoBtn" hidden></button>
        </div>
        <div id="mediaScrubber"><div id="mediaScrubberFill"></div><div id="mediaScrubberThumb"></div></div>
        <span id="mediaCur"></span><span id="mediaDur"></span>
      </div>
      <span id="playbackMode"></span>
      <button id="subsSearchBtn" hidden></button>
    </div>
    <div id="subs-modal" class="hidden"><span id="subs-status"></span><ul id="subs-results"></ul><button id="subs-close-btn"></button></div>
  </body></html>`;
}

// A representative playback session the fetch stub returns for the controller's
// initial loadFromSession(0,0,null).
function session(overrides = {}) {
  return Object.assign({
    ok: true,
    decision: 'direct_play',
    protocol: 'file',
    url: '/stream/tv/7',
    duration_seconds: 3661, // 1:01:01 — exercises the hours branch of fmtTime
    resume_position_seconds: 0,
    completed: false,
    audio_tracks: [],
    subtitle_tracks: [],
    skip_segments: [],
  }, overrides);
}

// Boot the controller against a session and settle the async load chain.
async function boot({ fixtureOpts, sessionData, stubs, storage } = {}) {
  const routes = { '/tv/playback-session': sessionData || session(), '/movie/playback-session': sessionData || session() };
  const env = loadController('media_player.js', {
    html: fixture(fixtureOpts),
    url: 'http://localhost/tv/player?file=7',
    stubs: Object.assign({ fetch: makeFetch(routes), Hls: makeMockHls() }, stubs || {}),
    storage,
  });
  env.window.document.dispatchEvent(new env.window.Event('turbo:load'));
  await flush();
  return env;
}

test('mediaPlayerConfig returns per-kind endpoints', async () => {
  const env = await boot();
  const tv = env.window.mediaPlayerConfig('tv');
  const movie = env.window.mediaPlayerConfig('movie');
  assert.strictEqual(tv.sessionURL, '/tv/playback-session');
  assert.strictEqual(movie.sessionURL, '/movie/playback-session');
  assert.strictEqual(movie.playerURL, '/movie/player');
  // The progressive regex must match remux/burn-in and nothing else.
  assert.ok(tv.progressiveRe.test('/stream/tv-remux/7'));
  assert.ok(tv.progressiveRe.test('/stream/tv-burnin/7'));
  assert.ok(!tv.progressiveRe.test('/stream/tv-hls/7/manifest.m3u8'));
  assert.ok(movie.progressiveRe.test('/stream/movie-burnin/3'));
});

test('buildSelects populates audio + subtitle pickers and labels burn-in subs', async () => {
  const env = await boot({
    sessionData: session({
      audio_tracks: [
        { ordinal: 1, language: 'eng', title: 'Stereo', codec: 'aac', default: true },
        { ordinal: 2, language: 'fra', codec: 'ac3' },
      ],
      subtitle_tracks: [
        { ordinal: 1, language: 'eng', title: 'Full', text: true },
        { ordinal: 2, language: 'eng', title: 'Forced', text: false }, // bitmap → burn-in
      ],
    }),
  });
  const doc = env.document;
  assert.strictEqual(doc.getElementById('audioPick').hidden, false, 'audio picker revealed for >1 track');
  assert.strictEqual(doc.getElementById('audioSelect').options.length, 2);
  assert.strictEqual(doc.getElementById('audioSelect').options[0].textContent, 'eng · Stereo · aac');

  const sub = doc.getElementById('subSelect');
  assert.strictEqual(doc.getElementById('subPick').hidden, false);
  assert.strictEqual(sub.options[0].textContent, 'Off');           // synthetic Off first
  assert.strictEqual(sub.options[1].textContent, 'eng · Full');    // text sub
  assert.strictEqual(sub.options[2].textContent, 'eng · Forced · burn-in'); // bitmap flagged
});

test('OpenSubtitles search button shows only with a key and no text track', async () => {
  const withText = await boot({
    fixtureOpts: { osEnabled: '1' },
    sessionData: session({ subtitle_tracks: [{ ordinal: 1, language: 'eng', text: true }] }),
  });
  assert.strictEqual(withText.document.getElementById('subsSearchBtn').hidden, true, 'text track present → no search offer');

  const noText = await boot({
    fixtureOpts: { osEnabled: '1' },
    sessionData: session({ subtitle_tracks: [{ ordinal: 1, language: 'eng', text: false }] }),
  });
  assert.strictEqual(noText.document.getElementById('subsSearchBtn').hidden, false, 'only burn-in subs → offer search');

  const noKey = await boot({ fixtureOpts: { osEnabled: '0' }, sessionData: session({ subtitle_tracks: [] }) });
  assert.strictEqual(noKey.document.getElementById('subsSearchBtn').hidden, true, 'no key → never offer');
});

test('fmtTime renders the scrubber duration/current labels (incl. hours)', async () => {
  const env = await boot(); // duration 3661 = 1:01:01
  const video = env.document.getElementById('tvVideo');
  video.currentTime = 65; // 1:05
  video.dispatchEvent(new env.window.Event('timeupdate'));
  assert.strictEqual(env.document.getElementById('mediaDur').textContent, '1:01:01');
  assert.strictEqual(env.document.getElementById('mediaCur').textContent, '1:05');
});

test('a resume seek on a progressive stream re-anchors via ?start=', async () => {
  const env = await boot({
    sessionData: session({ protocol: 'file', url: '/stream/tv-remux/7', resume_position_seconds: 90 }),
  });
  const src = env.document.getElementById('tvVideo').getAttribute('src') || '';
  assert.ok(src.indexOf('/stream/tv-remux/7') >= 0, 'progressive URL used');
  assert.ok(/[?&]start=90\b/.test(src), `?start= carries the resume position: got ${src}`);
});

test('an HLS resume loads the target segment first via startPosition', async () => {
  const Hls = makeMockHls();
  const env = await boot({
    stubs: { Hls },
    sessionData: session({ protocol: 'hls', url: '/stream/tv-hls/7/manifest.m3u8', resume_position_seconds: 120 }),
  });
  assert.strictEqual(Hls.instances.length, 1, 'one hls.js pipeline built');
  assert.strictEqual(Hls.instances[0].cfg.startPosition, 120, 'startPosition = resume, so the seek segment appends first (DTS order)');
  assert.strictEqual(Hls.instances[0].loadedUrl, '/stream/tv-hls/7/manifest.m3u8');
});

test('a fatal HLS media error runs the guarded recovery, not an infinite loop', async () => {
  const Hls = makeMockHls();
  const env = await boot({ stubs: { Hls }, sessionData: session({ protocol: 'hls', url: '/stream/tv-hls/7/manifest.m3u8' }) });
  const inst = Hls.instances[0];
  // First fatal media error → recoverMediaError().
  inst.emit(Hls.Events.ERROR, { fatal: true, type: Hls.ErrorTypes.MEDIA_ERROR });
  assert.strictEqual(inst.recoverMediaCount, 1);
  // A network error restarts loading rather than recovering media.
  inst.emit(Hls.Events.ERROR, { fatal: true, type: Hls.ErrorTypes.NETWORK_ERROR });
  assert.strictEqual(inst.startLoadCount, 1);
  // A non-fatal error is ignored.
  inst.emit(Hls.Events.ERROR, { fatal: false, type: Hls.ErrorTypes.MEDIA_ERROR });
  assert.strictEqual(inst.recoverMediaCount, 1);
});

test('skip-segment button appears inside a segment with the right label', async () => {
  const env = await boot({ sessionData: session({ skip_segments: [{ start: 10, end: 30, kind: 'recap' }] }) });
  const video = env.document.getElementById('tvVideo');
  const skipBtn = env.document.querySelector('.media-skip-btn');
  assert.ok(skipBtn, 'skip button created');
  assert.strictEqual(skipBtn.hidden, true, 'hidden before the segment');
  video.currentTime = 15;
  video.dispatchEvent(new env.window.Event('timeupdate'));
  assert.strictEqual(skipBtn.hidden, false, 'shown inside the segment');
  assert.strictEqual(skipBtn.textContent, 'Skip recap');
  video.currentTime = 40;
  video.dispatchEvent(new env.window.Event('timeupdate'));
  assert.strictEqual(skipBtn.hidden, true, 'hidden again past the segment');
});

test('auto-skip jumps past a segment once when enabled', async () => {
  const env = await boot({
    storage: { skip_auto: '1' },
    sessionData: session({ skip_segments: [{ start: 10, end: 30, kind: 'intro' }] }),
  });
  const video = env.document.getElementById('tvVideo');
  video.currentTime = 12;
  video.dispatchEvent(new env.window.Event('timeupdate'));
  assert.strictEqual(video.currentTime, 30, 'auto-skipped to the segment end');
});

test('persisted volume + muted are applied at init', async () => {
  const env = await boot({ storage: { tv_volume: '0.3', tv_muted: '1' } });
  const video = env.document.getElementById('tvVideo');
  assert.strictEqual(video.volume, 0.3);
  assert.strictEqual(video.muted, true);
  assert.strictEqual(env.document.getElementById('tvMuteBtn').getAttribute('aria-pressed'), 'true');
});

test('dialogue boost defaults ON and reflects an explicit OFF', async () => {
  const on = await boot();
  assert.strictEqual(on.document.getElementById('tvBoostBtn').getAttribute('aria-pressed'), 'true', 'default on');
  const off = await boot({ storage: { tv_boost: '0' } });
  assert.strictEqual(off.document.getElementById('tvBoostBtn').getAttribute('aria-pressed'), 'false');
  assert.strictEqual(off.document.getElementById('tvBoostBtn').classList.contains('is-on'), false);
});

test('progress is reported to the per-kind endpoint on timeupdate', async () => {
  const env = await boot();
  const video = env.document.getElementById('tvVideo');
  video.currentTime = 42;
  video.dispatchEvent(new env.window.Event('timeupdate'));
  assert.ok(env.beacons.some((b) => b.url.indexOf('/tv/playback-progress') >= 0), 'progress beacon sent to /tv endpoint');
});

test('a movie fixture drives the /movie endpoints', async () => {
  const env = await boot({ fixtureOpts: { kind: 'movie' } });
  const video = env.document.getElementById('tvVideo');
  video.currentTime = 42;
  video.dispatchEvent(new env.window.Event('timeupdate'));
  assert.ok(env.beacons.some((b) => b.url.indexOf('/movie/playback-progress') >= 0), 'movie kind → /movie endpoints');
});
