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
function fixture({ kind = 'tv', fileId = 7, prevFile = 0, nextFile = 0, osEnabled = '0' } = {}) {
  return `<!DOCTYPE html><html><body>
    <div class="tv-player-video-wrap">
      <video id="tvVideo" data-media-kind="${kind}" data-file-id="${fileId}" data-prev-file="${prevFile}" data-next-file="${nextFile}" data-os-enabled="${osEnabled}"></video>
      <div id="mediaOverlay">
        <div id="audioPick" hidden><select id="audioSelect"></select></div>
        <div id="subPick" hidden><select id="subSelect"></select></div>
        <div id="speedPick"><input id="speedSlider" type="range" min="0.5" max="2" step="0.25" value="1" /><span id="speedVal"></span></div>
        <div id="tvTransport">
          <button id="tvPrevEpBtn" hidden></button>
          <button id="tvRewindBtn"></button>
          <button id="tvToggleBtn"></button>
          <button id="tvForwardBtn"></button>
          <button id="tvNextEpBtn" hidden></button>
          <button id="tvBoostBtn"><span class="tv-glyph-vol"></span><span class="tv-glyph-mute"></span></button>
          <button id="tvMuteBtn"><span class="tv-glyph-vol"></span><span class="tv-glyph-mute"></span></button>
          <input id="tvVolume" type="range" min="0" max="1" step="0.01" />
          <button id="tvReloadBtn"></button>
          <button id="tvFullscreenBtn"></button>
          <button id="skipAutoBtn" hidden></button>
          <button id="muteAdsBtn" hidden></button>
        </div>
        <div id="mediaScrubber"><div id="mediaScrubberFill"></div><div id="mediaScrubberThumb"></div>
          <div class="media-scan-pill" id="mediaScanPill" hidden><span class="media-scan-rw"></span><span class="media-scan-ff"></span><span class="media-scan-speed"></span></div>
        </div>
        <span id="mediaCur"></span><span id="mediaDur"></span>
      </div>
      <span id="playbackMode"></span>
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
async function boot({ fixtureOpts, sessionData, stubs, storage, url } = {}) {
  const routes = { '/tv/playback-session': sessionData || session(), '/movie/playback-session': sessionData || session() };
  const env = loadController('media_player.js', {
    html: fixture(fixtureOpts),
    url: url || 'http://localhost/tv/player?file=7',
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

test('server-applied defaults select the pickers; explicit Off sends sub=-1 and sticks', async () => {
  // The server resolved playback defaults (language-preference audio, subtitles
  // on) and echoes applied_audio/applied_subtitle — the pickers must show the
  // served tracks, not disposition-default/Off.
  const fetchStub = makeFetch({
    '/tv/playback-session': session({
      audio_tracks: [
        { ordinal: 1, language: 'jpn', codec: 'aac', default: true },
        { ordinal: 2, language: 'eng', codec: 'aac' },
      ],
      subtitle_tracks: [{ ordinal: 1, language: 'eng', text: true }],
      subtitle_url: '/stream/tv-subtitles/7?track=1',
      applied_audio: 2,
      applied_subtitle: 1,
    }),
  });
  const env = await boot({ stubs: { fetch: fetchStub } });
  const doc = env.document;
  assert.strictEqual(doc.getElementById('audioSelect').value, '2', 'applied audio beats the disposition-default flag');
  const sub = doc.getElementById('subSelect');
  assert.strictEqual(sub.value, '1', 'applied subtitle selected instead of Off');

  // Explicit Off must reach the server as sub=-1 — a plain 0 reads as "unpinned"
  // and the subtitles-on default would re-apply against the user.
  sub.value = '0';
  sub.dispatchEvent(new env.window.Event('change'));
  await flush();
  let last = fetchStub.calls[fetchStub.calls.length - 1].url;
  assert.ok(last.includes('sub=-1'), `explicit off request carries sub=-1 (got ${last})`);

  // A later audio change keeps the explicit off rather than reverting to 0.
  const audio = doc.getElementById('audioSelect');
  audio.value = '1';
  audio.dispatchEvent(new env.window.Event('change'));
  await flush();
  last = fetchStub.calls[fetchStub.calls.length - 1].url;
  assert.ok(last.includes('sub=-1'), `audio change preserves explicit off (got ${last})`);
});

test('the subtitles dropdown leads with "Search subtitles…" whenever a key is configured', async () => {
  const withText = await boot({
    fixtureOpts: { osEnabled: '1' },
    sessionData: session({ subtitle_tracks: [{ ordinal: 1, language: 'eng', text: true }] }),
  });
  let sub = withText.document.getElementById('subSelect');
  assert.strictEqual(sub.options[0].value, 'search', 'key + text track → search option leads');
  assert.strictEqual(sub.options[1].textContent, 'Off');
  assert.strictEqual(sub.value, '0', 'Off stays the default selection');
  assert.strictEqual(sub.options[2].value, '1', 'file tracks follow Off');

  const noSubs = await boot({ fixtureOpts: { osEnabled: '1' }, sessionData: session({ subtitle_tracks: [] }) });
  sub = noSubs.document.getElementById('subSelect');
  assert.strictEqual(noSubs.document.getElementById('subPick').hidden, false, 'key + no tracks → dropdown still shown');
  assert.strictEqual(sub.options.length, 2, 'search + Off only');
  assert.strictEqual(sub.options[0].value, 'search');

  const noKey = await boot({ fixtureOpts: { osEnabled: '0' }, sessionData: session({ subtitle_tracks: [] }) });
  assert.strictEqual(noKey.document.getElementById('subPick').hidden, true, 'no key + no tracks → no dropdown');
  const noKeySubs = await boot({
    fixtureOpts: { osEnabled: '0' },
    sessionData: session({ subtitle_tracks: [{ ordinal: 1, language: 'eng', text: true }] }),
  });
  sub = noKeySubs.document.getElementById('subSelect');
  assert.notStrictEqual(sub.options[0].value, 'search', 'no key → never offered');
});

test('picking "Search subtitles…" opens the dialog and restores the previous selection', async () => {
  const env = await boot({
    fixtureOpts: { osEnabled: '1' },
    sessionData: session({ subtitle_tracks: [{ ordinal: 1, language: 'eng', text: true }] }),
  });
  const doc = env.document;
  const sub = doc.getElementById('subSelect');

  // Activate the text sub first, so "previous selection" is non-trivial.
  sub.value = '1';
  sub.dispatchEvent(new env.window.Event('change'));
  await flush();

  const sessionCalls = env.fetch.calls.filter((c) => c.url.indexOf('/tv/playback-session') >= 0).length;
  sub.value = 'search';
  sub.dispatchEvent(new env.window.Event('change'));
  await flush();

  assert.ok(!doc.getElementById('subs-modal').classList.contains('hidden'), 'dialog opened');
  assert.strictEqual(sub.value, '1', 'selection restored — the action never switches subtitles');
  const after = env.fetch.calls.filter((c) => c.url.indexOf('/tv/playback-session') >= 0).length;
  assert.strictEqual(after, sessionCalls, 'no playback-session reload from the action option');
  assert.ok(env.fetch.calls.some((c) => c.url.indexOf('/tv/subtitles/search') >= 0), 'search endpoint queried');
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

test('a home resume (?paused=1) still seeks to the saved position but does NOT autoplay', async () => {
  const Hls = makeMockHls();
  const env = await boot({
    stubs: { Hls },
    url: 'http://localhost/tv/player?file=7&paused=1',
    sessionData: session({ protocol: 'hls', url: '/stream/tv-hls/7/manifest.m3u8', resume_position_seconds: 120 }),
  });
  const video = env.document.getElementById('tvVideo');
  assert.strictEqual(Hls.instances[0].cfg.startPosition, 120, 'resume seek preserved (loads the target segment first)');
  assert.strictEqual(video.paused, true, 'starts paused — arriving from the dashboard does not autoplay');
  // The template ships the native `autoplay` attribute (fires on src attach,
  // independent of the JS play() call) — it must be cleared for a paused start.
  assert.strictEqual(video.autoplay, false, 'native autoplay attribute cleared so the browser does not play it either');
  // The spinner (shown on loadstart) must clear once the first frame decodes, even
  // though a paused start never fires "playing".
  const spinner = env.document.querySelector('.media-spinner');
  video.dispatchEvent(new env.window.Event('loadstart'));
  assert.strictEqual(spinner.hidden, false, 'loadstart shows the spinner');
  video.dispatchEvent(new env.window.Event('loadeddata'));
  assert.strictEqual(spinner.hidden, true, 'loadeddata clears it on a paused start');
});

test('a normal resume (no paused flag) autoplays', async () => {
  const env = await boot({ sessionData: session({ protocol: 'file', url: '/stream/tv/7', resume_position_seconds: 30 }) });
  const video = env.document.getElementById('tvVideo');
  assert.strictEqual(video.paused, false, 'autoplays when not launched with ?paused=1');
  assert.strictEqual(video.autoplay, true, 'native autoplay left on for a normal resume');
});

test('HLS fragment-load patience is raised to match the server segment-build ceiling', async () => {
  const Hls = makeMockHls();
  await boot({ stubs: { Hls }, sessionData: session({ protocol: 'hls', url: '/stream/tv-hls/7/manifest.m3u8' }) });
  const pol = Hls.instances[0].cfg.fragLoadPolicy && Hls.instances[0].cfg.fragLoadPolicy.default;
  assert.ok(pol, 'an explicit fragLoadPolicy.default is supplied');
  assert.strictEqual(pol.maxTimeToFirstByteMs, 300000, 'TTFB budget mirrors segBuildTimeout (5min) so a slow on-demand segment waits instead of timing out');
  assert.strictEqual(pol.maxLoadTimeMs, 300000);
  // The FULL default object is supplied (hls.js shallow-merges config), so a partial
  // policy can't silently drop errorRetry and stop retrying genuine failures.
  assert.strictEqual(pol.errorRetry.maxNumRetry, 6, 'errorRetry preserved — real failures still retry then fatal out');
});

test('the buffering spinner shows while the element is starved for data, hides on play/pause', async () => {
  const env = await boot({ sessionData: session({ protocol: 'file', url: '/stream/tv/7' }) });
  const video = env.document.getElementById('tvVideo');
  const spinner = env.document.querySelector('.media-spinner');
  assert.ok(spinner, 'a spinner overlay is created in the video wrap');
  video.dispatchEvent(new env.window.Event('waiting'));
  assert.strictEqual(spinner.hidden, false, 'waiting (buffering) reveals the spinner');
  video.dispatchEvent(new env.window.Event('playing'));
  assert.strictEqual(spinner.hidden, true, 'playing hides it');
  // Autoplay-block guard: a paused element (play() rejected) must not spin forever.
  video.dispatchEvent(new env.window.Event('waiting'));
  video.dispatchEvent(new env.window.Event('pause'));
  assert.strictEqual(spinner.hidden, true, 'pause hides the spinner so an autoplay-blocked video does not spin indefinitely');
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

test('HLS fatal errors are capped — a buffered frag resets the budget, cap+1 gives up', async () => {
  const Hls = makeMockHls();
  const env = await boot({ stubs: { Hls }, sessionData: session({ protocol: 'hls', url: '/stream/tv-hls/7/manifest.m3u8' }) });
  const inst = Hls.instances[0];
  const mode = env.document.getElementById('playbackMode');
  // A buffered fragment between failures is real progress → the budget resets, so
  // an occasional error over a long healthy stream never exhausts the cap.
  for (let i = 0; i < 8; i++) {
    inst.emit(Hls.Events.ERROR, { fatal: true, type: Hls.ErrorTypes.MEDIA_ERROR });
    inst.emit(Hls.Events.FRAG_BUFFERED, {});
  }
  assert.strictEqual(inst.destroyed, false, 'progress between errors keeps recovering, never gives up');
  // No progress this time: consecutive fatals past the cap → give up, not an infinite loop.
  for (let i = 0; i < 5; i++) inst.emit(Hls.Events.ERROR, { fatal: true, type: Hls.ErrorTypes.NETWORK_ERROR });
  assert.strictEqual(inst.destroyed, true, 'gives up after the consecutive-fatal cap instead of thrashing');
  assert.match(mode.textContent, /reload to continue/i);
});

test('begin=1 starts the target at the beginning even when a resume position exists', async () => {
  const Hls = makeMockHls();
  const env = await boot({
    stubs: { Hls },
    url: 'http://localhost/tv/player?file=7&begin=1',
    sessionData: session({ protocol: 'hls', url: '/stream/tv-hls/7/manifest.m3u8', resume_position_seconds: 120 }),
  });
  // Without begin=1 this would be startPosition 120 (see the resume test above);
  // begin=1 makes the boot pass seekTo=0, so no seek — the episode starts fresh.
  assert.strictEqual(Hls.instances[0].cfg.startPosition, -1, 'begin=1 overrides the saved resume → startPosition -1');
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

test('prev/next episode buttons appear only with adjacent files (TV), hidden on movies', async () => {
  const tv = await boot({ fixtureOpts: { prevFile: 5, nextFile: 9 } });
  assert.strictEqual(tv.document.getElementById('tvPrevEpBtn').hidden, false, 'prev shown with a prior episode');
  assert.strictEqual(tv.document.getElementById('tvNextEpBtn').hidden, false, 'next shown with a following episode');

  const boundary = await boot({ fixtureOpts: { prevFile: 5, nextFile: 0 } });
  assert.strictEqual(boundary.document.getElementById('tvNextEpBtn').hidden, true, 'no next episode → hidden');

  const movie = await boot({ fixtureOpts: { kind: 'movie', prevFile: 0, nextFile: 0 } });
  assert.strictEqual(movie.document.getElementById('tvPrevEpBtn').hidden, true, 'hidden on movies');
  assert.strictEqual(movie.document.getElementById('tvNextEpBtn').hidden, true, 'hidden on movies');
});

test('the reload button re-loads the stream at the current position', async () => {
  const env = await boot({ sessionData: session({ protocol: 'file', url: '/stream/tv-remux/7' }) });
  const video = env.document.getElementById('tvVideo');
  video.currentTime = 42; // progressive stream → reload re-anchors via ?start=
  env.document.getElementById('tvReloadBtn').dispatchEvent(new env.window.Event('click'));
  await flush();
  const src = video.getAttribute('src') || '';
  assert.ok(/[?&]start=42\b/.test(src), `reload re-anchors at the current position: got ${src}`);
});

test('playback speed: slider input sets playbackRate + persists; stored value applied on boot', async () => {
  let env = await boot();
  let video = env.document.getElementById('tvVideo');
  const slider = env.document.getElementById('speedSlider');
  slider.value = '1.5';
  slider.dispatchEvent(new env.window.Event('input', { bubbles: true }));
  assert.strictEqual(video.playbackRate, 1.5, 'input applies the rate');
  assert.strictEqual(env.window.localStorage.getItem('tv_speed'), '1.5', 'rate persisted');
  assert.strictEqual(env.document.getElementById('speedVal').textContent, '1.5×', 'the readout tracks the rate');

  // A fresh boot with the stored value restores the slider + applies it.
  env = await boot({ storage: { tv_speed: '2' } });
  video = env.document.getElementById('tvVideo');
  assert.strictEqual(video.playbackRate, 2, 'stored rate applied on boot');
  assert.strictEqual(env.document.getElementById('speedSlider').value, '2');
  assert.strictEqual(env.document.getElementById('speedVal').textContent, '2×');

  // Garbage in storage falls back to 1× rather than an off-range rate.
  env = await boot({ storage: { tv_speed: '17' } });
  assert.strictEqual(env.document.getElementById('tvVideo').playbackRate, 1);
  assert.strictEqual(env.document.getElementById('speedSlider').value, '1');
});

test('Up Next: episode end shows a cancelable countdown card instead of an instant jump', async () => {
  const env = await boot({ fixtureOpts: { nextFile: 5 } });
  const video = env.document.getElementById('tvVideo');
  const card = env.document.querySelector('.media-upnext');
  assert.ok(card, 'card created when a next file exists');
  assert.strictEqual(card.hidden, true, 'hidden until the episode ends');

  video.dispatchEvent(new env.window.Event('ended'));
  assert.strictEqual(card.hidden, false, 'shown at episode end');
  assert.strictEqual(card.querySelector('#upnextCount').textContent, '8');
  assert.ok(card.hasAttribute('data-couch-overlay'), 'remote Back can dismiss it');

  card.querySelector('#upnextCancel').click();
  assert.strictEqual(card.hidden, true, 'cancel dismisses');

  // Rewinding back into the credits must not be fought.
  video.dispatchEvent(new env.window.Event('ended'));
  assert.strictEqual(card.hidden, false);
  video.dispatchEvent(new env.window.Event('seeking'));
  assert.strictEqual(card.hidden, true, 'a seek cancels the countdown');
});

test('Up Next: no card without a next file (movies, last episode)', async () => {
  const env = await boot({ fixtureOpts: { nextFile: 0 } });
  env.document.getElementById('tvVideo').dispatchEvent(new env.window.Event('ended'));
  assert.strictEqual(env.document.querySelector('.media-upnext'), null);
});

test('seek-bar marks: chapter ticks + skip-segment spans render from the session', async () => {
  const env = await boot({
    sessionData: session({
      chapters: [{ start: 90, title: 'Chapter 2' }, { start: 0, title: 'Start' }],
      skip_segments: [{ start: 10, end: 40, kind: 'intro' }],
    }),
  });
  const marks = env.document.querySelector('.media-scrub-marks');
  assert.ok(marks, 'marks layer created');
  const ticks = marks.querySelectorAll('.media-scrub-tick');
  assert.strictEqual(ticks.length, 1, 'chapter at 0 is skipped as noise');
  assert.strictEqual(ticks[0].title, 'Chapter 2');
  // 90s of 3661 ≈ 2.458%
  assert.ok(ticks[0].style.left.startsWith('2.4'), `tick position: ${ticks[0].style.left}`);
  const segs = marks.querySelectorAll('.media-scrub-seg');
  assert.strictEqual(segs.length, 1);
  assert.ok(segs[0].classList.contains('media-scrub-seg-intro'));
});

test('hover preview: manifest fetched lazily; sprite math positions the frame; 404 degrades silently', async () => {
  const manifest = { interval_sec: 10, width: 240, height: 100, tile: 5, frames: 599 };
  const env = await boot({
    stubs: {
      fetch: makeFetch({
        '/tv/playback-session': session(),
        '/tv-trickplay/7/manifest.json': manifest,
      }),
    },
  });
  const scrubber = env.document.getElementById('mediaScrubber');
  const preview = env.document.querySelector('.media-scrub-preview');
  assert.ok(preview, 'preview element created');

  // First hover triggers exactly one manifest fetch.
  scrubber.dispatchEvent(new env.window.Event('pointerenter', { bubbles: true }));
  scrubber.dispatchEvent(new env.window.Event('pointerenter', { bubbles: true }));
  await flush();
  const calls = env.fetch.calls.filter((c) => c.url.indexOf('trickplay') >= 0);
  assert.strictEqual(calls.length, 1, 'manifest fetched once');

  // A pointermove mid-bar shows the frame; jsdom rects are 0-wide, so drive
  // the math via a synthetic event and assert background positioning exists.
  scrubber.dispatchEvent(new env.window.MouseEvent('pointermove', { bubbles: true, clientX: 0 }));
  const frame = env.document.querySelector('.media-scrub-preview-frame');
  assert.ok(!preview.hidden, 'preview shown on move');
  assert.ok(frame.style.backgroundImage.indexOf('sprite00000.jpg') >= 0, `sprite sheet: ${frame.style.backgroundImage}`);

  scrubber.dispatchEvent(new env.window.Event('pointerleave', { bubbles: true }));
  assert.ok(preview.hidden, 'hidden on leave');
});

test('FF/RW scan: presses cycle 2×/8×/32× with the indicator riding the preview cluster', async () => {
  const env = await boot();
  const doc = env.document;
  const video = doc.getElementById('tvVideo');
  const pill = doc.getElementById('mediaScanPill');
  const speed = pill.querySelector('.media-scan-speed');
  const ff = doc.getElementById('tvForwardBtn');
  const rw = doc.getElementById('tvRewindBtn');

  // The indicator is reparented into the trickplay preview cluster, so during a
  // scan it rides the playhead with the frame + timestamp (the Plex/Roku look).
  assert.ok(pill.closest('.media-scrub-preview'), 'pill lives inside the preview cluster');

  video.play(); // "watching" — the transport reflects play/pause events
  const transport = doc.getElementById('tvTransport');
  assert.ok(transport.classList.contains('playing'));
  ff.click();
  assert.ok(!transport.classList.contains('playing'), 'entering scan pauses playback');
  assert.strictEqual(pill.hidden, false, 'pill shows from the first press');
  assert.strictEqual(speed.textContent, '2×');
  assert.strictEqual(pill.querySelector('.media-scan-ff').hidden, false, 'forward glyph shown');
  assert.strictEqual(pill.querySelector('.media-scan-rw').hidden, true);
  assert.ok(!pill.closest('.media-scrub-preview').hidden, 'preview cluster appears with the first press');

  ff.click();
  assert.strictEqual(speed.textContent, '8×');
  ff.click();
  assert.strictEqual(speed.textContent, '32×');
  ff.click();
  assert.strictEqual(speed.textContent, '2×', '4th press wraps back to 2×');

  rw.click();
  assert.strictEqual(speed.textContent, '2×', 'opposite direction restarts at 2×');
  assert.strictEqual(pill.querySelector('.media-scan-rw').hidden, false, 'rewind glyph shown');
  assert.strictEqual(pill.querySelector('.media-scan-ff').hidden, true);

  doc.getElementById('tvToggleBtn').click();
  assert.strictEqual(pill.hidden, true, 'play ends the scan and hides the pill');
});

test('FF/RW scan: play commits the advanced position as one real seek and resumes', async () => {
  const env = await boot();
  const doc = env.document;
  const video = doc.getElementById('tvVideo');
  const ff = doc.getElementById('tvForwardBtn');

  video.currentTime = 100;
  ff.click(); ff.click(); ff.click(); // 32× forward
  await new Promise((r) => setTimeout(r, 450)); // let the 200ms ticker advance the virtual playhead
  assert.strictEqual(video.currentTime, 100, 'no seek while scanning — the playhead is virtual');
  doc.getElementById('tvToggleBtn').click();
  assert.ok(video.currentTime > 100, `commit seeks past the entry point: ${video.currentTime}`);
  // jsdom's `paused` is a getter the stubs can't flip — assert resumption via the
  // 'play' event's DOM effect (the transport class the controller maintains).
  assert.ok(doc.getElementById('tvTransport').classList.contains('playing'), 'playback resumed');
});

test('FF/RW scan: rewind moves backward; a scrubber drag cancels without seeking', async () => {
  const env = await boot();
  const doc = env.document;
  const video = doc.getElementById('tvVideo');

  video.currentTime = 100;
  doc.getElementById('tvRewindBtn').click();
  await new Promise((r) => setTimeout(r, 450));
  doc.getElementById('tvToggleBtn').click();
  assert.ok(video.currentTime < 100 && video.currentTime > 90, `2× rewind landed just behind the entry point: ${video.currentTime}`);

  // Drag-cancel: enter a scan, then grab the scrubber — scan dies, no commit.
  video.currentTime = 50;
  doc.getElementById('tvForwardBtn').click();
  doc.getElementById('mediaScrubber').dispatchEvent(new env.window.Event('pointerdown', { bubbles: true }));
  assert.strictEqual(doc.getElementById('mediaScanPill').hidden, true, 'drag cancels the scan');
  assert.strictEqual(video.currentTime, 50, 'no seek committed by the canceled scan');
});

test('mute ads: mutes inside a commercial, restores on exit, never touches saved prefs', async () => {
  const env = await boot({
    storage: { mute_ads: '1' },
    sessionData: session({ skip_segments: [{ start: 10, end: 30, kind: 'commercial' }, { start: 100, end: 120, kind: 'intro' }] }),
  });
  const doc = env.document;
  const video = doc.getElementById('tvVideo');
  const btn = doc.getElementById('muteAdsBtn');
  assert.strictEqual(btn.hidden, false, 'toggle revealed — file has a commercial segment');
  assert.strictEqual(btn.getAttribute('aria-pressed'), 'true', 'stored preference reflected');

  video.currentTime = 15;
  video.dispatchEvent(new env.window.Event('timeupdate'));
  assert.strictEqual(video.muted, true, 'muted inside the commercial');
  video.currentTime = 40;
  video.dispatchEvent(new env.window.Event('timeupdate'));
  assert.strictEqual(video.muted, false, 'restored past the commercial');

  // Intros/recaps keep their audio — only kind=commercial mutes.
  video.currentTime = 105;
  video.dispatchEvent(new env.window.Event('timeupdate'));
  assert.strictEqual(video.muted, false, 'intro segment never mutes');

  // The transient ad-mute must never write the persisted mute preference.
  assert.strictEqual(env.window.localStorage.getItem('tv_muted'), null, 'tv_muted untouched');
});

test('mute ads: a user muted before the ad stays muted; a mid-ad unmute is not fought', async () => {
  const seg = { start: 10, end: 30, kind: 'commercial' };
  // Muted-before-ad: nothing to restore, stays muted after.
  let env = await boot({ storage: { mute_ads: '1', tv_muted: '1' }, sessionData: session({ skip_segments: [seg] }) });
  let video = env.document.getElementById('tvVideo');
  video.currentTime = 15;
  video.dispatchEvent(new env.window.Event('timeupdate'));
  video.currentTime = 40;
  video.dispatchEvent(new env.window.Event('timeupdate'));
  assert.strictEqual(video.muted, true, 'user mute survives the ad untouched');

  // Mid-ad unmute (via the mute button) wins: no re-mute, no exit "restore".
  env = await boot({ storage: { mute_ads: '1' }, sessionData: session({ skip_segments: [seg] }) });
  video = env.document.getElementById('tvVideo');
  video.currentTime = 15;
  video.dispatchEvent(new env.window.Event('timeupdate'));
  assert.strictEqual(video.muted, true);
  env.document.getElementById('tvMuteBtn').click(); // deliberate unmute inside the ad
  assert.strictEqual(video.muted, false);
  video.currentTime = 20;
  video.dispatchEvent(new env.window.Event('timeupdate'));
  assert.strictEqual(video.muted, false, 'override respected — not re-muted');
  video.currentTime = 40;
  video.dispatchEvent(new env.window.Event('timeupdate'));
  assert.strictEqual(video.muted, false, 'no phantom restore at exit');
});

test('mute ads: off by default, hidden without commercial segments, toggle persists', async () => {
  // Default off: inside an ad, nothing happens.
  let env = await boot({ sessionData: session({ skip_segments: [{ start: 10, end: 30, kind: 'commercial' }] }) });
  let video = env.document.getElementById('tvVideo');
  video.currentTime = 15;
  video.dispatchEvent(new env.window.Event('timeupdate'));
  assert.strictEqual(video.muted, false, 'toggle off → no auto-mute');
  // Clicking the toggle mid-ad enables + mutes immediately, and persists.
  env.document.getElementById('muteAdsBtn').click();
  assert.strictEqual(video.muted, true, 'enabling mid-ad acts immediately');
  assert.strictEqual(env.window.localStorage.getItem('mute_ads'), '1');

  // No commercial segments (intro only) → toggle stays hidden.
  env = await boot({ sessionData: session({ skip_segments: [{ start: 10, end: 30, kind: 'intro' }] }) });
  assert.strictEqual(env.document.getElementById('muteAdsBtn').hidden, true, 'no commercials → hidden');
});

test('hardware media keys: the video page installs the bridge — FF/RW drive the scan, play commits, teardown clears it', async () => {
  const env = await boot();
  const doc = env.document;
  const mc = env.window.hesperaMediaControl;
  assert.strictEqual(typeof mc, 'function', 'bridge installed on a video page');

  assert.strictEqual(mc('seekforward'), true, 'FF consumed');
  const pill = doc.getElementById('mediaScanPill');
  assert.strictEqual(pill.hidden, false, 'FF starts the DVR scan');
  assert.strictEqual(pill.querySelector('.media-scan-speed').textContent, '2×');

  assert.strictEqual(mc('play'), true, 'play consumed');
  assert.strictEqual(pill.hidden, true, 'play commits and ends the scan');
  assert.ok(doc.getElementById('tvTransport').classList.contains('playing'), 'video resumed');
  assert.strictEqual(env.window.navigator.mediaSession.playbackState, 'playing', 'video play drives playbackState');

  assert.strictEqual(mc('nexttrack'), true, 'episode actions consumed even with no adjacent file (never falls through to music)');

  doc.dispatchEvent(new env.window.Event('turbo:before-cache'));
  assert.strictEqual(env.window.hesperaMediaControl, null, 'teardown returns the media keys to the music engine');
});

test('photo clips: prev/next buttons unhide with clip wording; media keys route to them', async () => {
  const env = await boot({ fixtureOpts: { kind: 'photo', prevFile: 3, nextFile: 9 } });
  const doc = env.document;
  const prev = doc.getElementById('tvPrevEpBtn');
  const next = doc.getElementById('tvNextEpBtn');
  assert.strictEqual(prev.hidden, false, 'prev clip button shown');
  assert.strictEqual(next.hidden, false, 'next clip button shown');
  assert.strictEqual(prev.title, 'Previous clip', 'episode wording swapped for clips');
  assert.strictEqual(next.getAttribute('aria-label'), 'Next clip');
  // The hardware-media-key bridge consumes next/prev on a clip page too.
  assert.strictEqual(env.window.hesperaMediaControl('nexttrack'), true);
});

test('boot lands keyboard focus on the play/pause button', async () => {
  const env = await boot();
  assert.strictEqual(env.document.activeElement, env.document.getElementById('tvToggleBtn'),
    'first Enter/OK after opening a player must toggle playback, not activate a nav link');
});

test('overlay auto-hide neither pins on nor blurs the focused play/pause button', async () => {
  const env = await boot();
  const doc = env.document;
  const wrap = doc.querySelector('.tv-player-video-wrap');
  const video = doc.getElementById('tvVideo');
  video.paused = false; // playing — the hide gate is live
  assert.ok(!wrap.classList.contains('controls-hidden'), 'controls start visible');
  assert.strictEqual(doc.activeElement, doc.getElementById('tvToggleBtn'));
  // HIDE_MS is 2500 — wait it out; the focused toggle must not pin the overlay
  // open (the :focus-visible keep-visible rule exempts it) …
  await new Promise((r) => setTimeout(r, 2700));
  assert.ok(wrap.classList.contains('controls-hidden'), 'controls auto-hid with the toggle focused');
  // … and must KEEP focus while hidden (exempt from the blur-on-hide), so an
  // invisible Enter/Space is a deliberate pause — the TV-app idiom.
  assert.strictEqual(doc.activeElement, doc.getElementById('tvToggleBtn'), 'toggle keeps focus while hidden');
});

test('anamorphic aspect correction engages only on rendered-vs-flagged mismatch', async () => {
  // Squeezed render: the browser reports the storage grid (702×576) while the
  // session flags the source as ~16:9 (a PAL DVD rip whose SAR got lost).
  const dar = 499 / 288;
  const env = await boot({ sessionData: session({ video_dar: dar }) });
  const video = env.document.getElementById('tvVideo');
  Object.defineProperty(video, 'videoWidth', { configurable: true, value: 702 });
  Object.defineProperty(video, 'videoHeight', { configurable: true, value: 576 });
  video.dispatchEvent(new env.window.Event('loadedmetadata'));
  assert.ok(video.classList.contains('aspect-fix'), 'mismatch applies the fix');
  assert.ok(Math.abs(parseFloat(video.style.getPropertyValue('--dar')) - dar) < 1e-6);

  // Browser honored the flag (rendered dims already DAR-shaped) → fix clears.
  Object.defineProperty(video, 'videoWidth', { configurable: true, value: 998 });
  video.dispatchEvent(new env.window.Event('loadedmetadata'));
  assert.ok(!video.classList.contains('aspect-fix'), 'matching render stays untouched');
  assert.strictEqual(video.style.getPropertyValue('--dar'), '');

  // Progressive-path reality: loadedmetadata fires with dims still 0 (empty_moov
  // fMP4) → no fix yet; the dims arrive later announced by 'resize' → fix applies.
  Object.defineProperty(video, 'videoWidth', { configurable: true, value: 0 });
  Object.defineProperty(video, 'videoHeight', { configurable: true, value: 0 });
  video.dispatchEvent(new env.window.Event('loadedmetadata'));
  assert.ok(!video.classList.contains('aspect-fix'), 'no dims yet → no fix');
  Object.defineProperty(video, 'videoWidth', { configurable: true, value: 702 });
  Object.defineProperty(video, 'videoHeight', { configurable: true, value: 576 });
  video.dispatchEvent(new env.window.Event('resize'));
  assert.ok(video.classList.contains('aspect-fix'), 'resize with real dims applies the fix');

  // Session without video_dar (square-pixel file / old probe) → never engages.
  const env2 = await boot();
  const v2 = env2.document.getElementById('tvVideo');
  Object.defineProperty(v2, 'videoWidth', { configurable: true, value: 702 });
  Object.defineProperty(v2, 'videoHeight', { configurable: true, value: 576 });
  v2.dispatchEvent(new env2.window.Event('loadedmetadata'));
  assert.ok(!v2.classList.contains('aspect-fix'), 'no DAR → no correction');
});
