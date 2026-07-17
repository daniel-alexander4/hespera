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
          <button id="tvPrevEpBtn"></button>
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

// The blur-after-pick is a MOUSE affordance: a mouse-clicked <select> matches
// :focus-visible in Chrome, so without it the auto-hiding overlay would pin open
// forever. Blurring a REMOTE user's pick instead drops focus to <body>, from
// which couch.js's next arrow restarts at the first focusable on the page — the
// ring escapes the player mid-episode. So the blur is gated on input modality.
test('a remote subtitle pick keeps the ring on the picker; a mouse pick blurs it', async () => {
  const env = await boot({
    sessionData: session({ subtitle_tracks: [{ ordinal: 1, language: 'eng', text: true }] }),
  });
  const doc = env.document;
  const sub = doc.getElementById('subSelect');

  // Remote/keyboard: html.using-mouse is absent (couch clears it on any handled key).
  sub.focus();
  sub.value = '1';
  sub.dispatchEvent(new env.window.Event('change'));
  await flush();
  assert.strictEqual(doc.activeElement, sub, 'remote pick keeps focus on the picker');

  // Mouse: couch sets using-mouse on mousedown — the select must blur so the
  // overlay can auto-hide (its :focus-visible would otherwise pin it open).
  doc.documentElement.classList.add('using-mouse');
  sub.focus();
  sub.value = '0';
  sub.dispatchEvent(new env.window.Event('change'));
  await flush();
  assert.notStrictEqual(doc.activeElement, sub, 'mouse pick blurs so the controls can hide');
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

// Dismissing the dialog must hand the ring back to the picker it was opened from.
// Otherwise focus lands on <body> when the modal hides, and couch.js's next arrow
// restarts at the first focusable on the page — the ring escapes the player.
test('the subtitle dialog returns the remote ring to the picker on close', async () => {
  const env = await boot({
    fixtureOpts: { osEnabled: '1' },
    sessionData: session({ subtitle_tracks: [{ ordinal: 1, language: 'eng', text: true }] }),
  });
  const doc = env.document;
  const sub = doc.getElementById('subSelect');

  sub.focus();
  sub.value = 'search';
  sub.dispatchEvent(new env.window.Event('change'));
  await flush();
  assert.strictEqual(doc.activeElement, doc.getElementById('subs-close-btn'), 'the ring moves into the dialog it opened');

  doc.getElementById('subs-close-btn').dispatchEvent(new env.window.MouseEvent('click', { bubbles: true }));
  await flush();
  assert.ok(doc.getElementById('subs-modal').classList.contains('hidden'), 'dialog closed');
  assert.strictEqual(doc.activeElement, sub, 'ring is back on the subtitles picker, not <body>');
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

test('a WATCHED item still resumes its partial re-watch', async () => {
  // Watched and resume are independent. The client used to zero the resume
  // whenever session.completed was set, so re-opening a watched episode could only
  // ever start from the beginning — silently defeating the decoupling. The server
  // (resumePosition) is now the sole owner of "is there anything to resume"; it has
  // already dropped a position left at the end of a finished playthrough, so a
  // position that survives to the client is a genuine one and must be honored.
  const env = await boot({
    sessionData: session({
      protocol: 'file',
      url: '/stream/tv-remux/7',
      resume_position_seconds: 900,
      completed: true,
    }),
  });
  const src = env.document.getElementById('tvVideo').getAttribute('src') || '';
  assert.ok(/[?&]start=900\b/.test(src), `a watched item resumes where it was paused: got ${src}`);
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

// |< is ALWAYS live (restart is always available); >| still hides with no next file.
test('the |< button is present on every player — a movie has one too', async () => {
  const tv = await boot({ fixtureOpts: { prevFile: 5, nextFile: 9 } });
  assert.strictEqual(tv.document.getElementById('tvPrevEpBtn').hidden, false, 'prev shown with a prior episode');
  assert.strictEqual(tv.document.getElementById('tvNextEpBtn').hidden, false, 'next shown with a following episode');

  const boundary = await boot({ fixtureOpts: { prevFile: 5, nextFile: 0 } });
  assert.strictEqual(boundary.document.getElementById('tvNextEpBtn').hidden, true, 'no next episode → next hidden');

  const movie = await boot({ fixtureOpts: { kind: 'movie', prevFile: 0, nextFile: 0 } });
  assert.strictEqual(movie.document.getElementById('tvPrevEpBtn').hidden, false, 'a movie still gets |< — it restarts');
  assert.strictEqual(movie.document.getElementById('tvNextEpBtn').hidden, true, 'no next file → next hidden');
});

test('|< past 10s restarts the current file instead of stepping back', async () => {
  const env = await boot({ fixtureOpts: { prevFile: 5, nextFile: 9 } });
  const video = env.document.getElementById('tvVideo');
  video.currentTime = 42;
  env.document.getElementById('tvPrevEpBtn').dispatchEvent(new env.window.Event('click'));
  assert.strictEqual(video.currentTime, 0, 'restarts in place (no navigation to the previous file)');
});

test('|< within 10s steps back to the previous file (it does NOT restart)', async () => {
  const env = await boot({ fixtureOpts: { prevFile: 5, nextFile: 9 } });
  const video = env.document.getElementById('tvVideo');
  video.currentTime = 5;
  env.document.getElementById('tvPrevEpBtn').dispatchEvent(new env.window.Event('click'));
  // gotoFile navigates away (jsdom can't follow it), so the tell is the absence of a
  // seek: the restart arm would have zeroed the playhead.
  assert.strictEqual(video.currentTime, 5, 'left the playhead alone → took the step-back arm');
});

test('|< restarts even inside 10s when there is no previous file', async () => {
  const env = await boot({ fixtureOpts: { kind: 'movie', prevFile: 0, nextFile: 0 } });
  const video = env.document.getElementById('tvVideo');
  video.currentTime = 5;
  env.document.getElementById('tvPrevEpBtn').dispatchEvent(new env.window.Event('click'));
  assert.strictEqual(video.currentTime, 0, 'nothing to step back to → restart is the terminal behavior');
});

test('the remote |< (media key) restarts a movie rather than doing nothing', async () => {
  const env = await boot({ fixtureOpts: { kind: 'movie', prevFile: 0, nextFile: 0 } });
  const video = env.document.getElementById('tvVideo');
  video.currentTime = 300;
  assert.strictEqual(env.window.hesperaMediaControl('previoustrack'), true, 'the bridge claims the action');
  assert.strictEqual(video.currentTime, 0, 'and it restarts the film');
});

test('a restart on a progressive stream streams from the top (no ?start=)', async () => {
  const env = await boot({ sessionData: session({ protocol: 'file', url: '/stream/tv-remux/7' }), fixtureOpts: { prevFile: 0, nextFile: 0 } });
  const video = env.document.getElementById('tvVideo');
  video.currentTime = 42; // remux is not byte-seekable → a seek is a source reload
  env.document.getElementById('tvPrevEpBtn').dispatchEvent(new env.window.Event('click'));
  await flush();
  const src = video.getAttribute('src') || '';
  assert.ok(/\/stream\/tv-remux\/7/.test(src), 'reloaded the progressive source');
  assert.ok(!/[?&]start=/.test(src), 'and asked for it from the beginning, not ?start=0');
});

// Count completed:true progress reports. sendBeacon ships a Blob (jsdom's has no .text()),
// so we disable it in the test → reportProgress falls back to fetch, whose stub records the
// raw JSON body for inspection.
function completedReports(env) {
  return env.fetch.calls.filter((c) => {
    if (c.url.indexOf('playback-progress') < 0) return false;
    try { return JSON.parse(c.opts.body).completed === true; } catch { return false; }
  }).length;
}

test('a |< restart re-arms the 90%-watched completion report', async () => {
  // No previous file → |< restarts IN PLACE (the one path that doesn't page-reload, so
  // completedReported must be cleared by hand). Default session duration 3661s → 90% ≈ 3295s.
  const env = await boot({ fixtureOpts: { kind: 'movie', prevFile: 0, nextFile: 0 } });
  env.window.navigator.sendBeacon = null; // route reportProgress through the recording fetch
  const video = env.document.getElementById('tvVideo');

  // Watch past 90% → completion reported once.
  video.currentTime = 3300;
  video.dispatchEvent(new env.window.Event('timeupdate'));
  await flush();
  assert.strictEqual(completedReports(env), 1, 'first pass past 90% reports completion once');

  // Restart in place via |<.
  env.document.getElementById('tvPrevEpBtn').dispatchEvent(new env.window.Event('click'));
  assert.strictEqual(video.currentTime, 0, 'restarted in place');

  // Re-watch past 90% → completion reported AGAIN (the restart re-armed the once-per-load gate).
  video.currentTime = 3300;
  video.dispatchEvent(new env.window.Event('timeupdate'));
  await flush();
  assert.strictEqual(completedReports(env), 2, 'the restart re-armed the completion report');
});

test('|< on the Up Next countdown restarts the file instead of being auto-advanced away', async () => {
  const env = await boot({ fixtureOpts: { prevFile: 0, nextFile: 9 } });
  const video = env.document.getElementById('tvVideo');
  // A real end-of-file: play out, then `ended` arms the 8s auto-advance card.
  video.currentTime = 3600;
  video.dispatchEvent(new env.window.Event('timeupdate'));
  video.dispatchEvent(new env.window.Event('ended'));
  const card = env.document.querySelector('.media-upnext');
  assert.ok(card && !card.hidden, 'Up Next is counting down');

  env.document.getElementById('tvPrevEpBtn').dispatchEvent(new env.window.Event('click'));
  assert.strictEqual(card.hidden, true, 'the countdown is dismissed — it must not navigate to the next episode');
  assert.strictEqual(video.currentTime, 0, 'and the file restarts');
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

// --- progressive-path seek behaviour (the remux/burn-in streams) ---
// These matter far more since v0.34.0: an h264 file with Dolby audio is now a
// stream-copy remux, so a third of a real library rides this path.

const remux = (extra) => session(Object.assign({ protocol: 'file', url: '/stream/tv-remux/7' }, extra || {}));
const arrowKey = (env, scrubber, key) =>
  scrubber.dispatchEvent(new env.window.KeyboardEvent('keydown', { key, bubbles: true, cancelable: true }));
// Progress beacons are sent as Blobs, so String(data) is '[object Blob]' — decode
// them, or an assertion about the payload silently passes no matter what was sent.
// (jsdom's Blob has no .text(); FileReader is the one it does implement.)
const readBlob = (env, blob) =>
  new Promise((res) => {
    const r = new env.window.FileReader();
    r.onload = () => res(String(r.result));
    r.readAsText(blob);
  });
const beaconBodies = (env) =>
  Promise.all((env.window.__beacons || []).map((b) => (typeof b.data === 'string' ? b.data : readBlob(env, b.data))));
// Advance the element the way real playback does — in small continuous steps — so
// the player's continuity tracker records how far we actually WATCHED. This is the
// only signal a dying stream can't fake: at a false 'ended', Chrome snaps
// currentTime, duration AND buffered.end all to the declared duration.
const playTo = (env, video, absSec) => {
  for (let t = 0; t <= absSec; t += 2) {
    video.currentTime = t;
    video.dispatchEvent(new env.window.Event('timeupdate'));
  }
};

test('rapid ±10s presses on a progressive stream accumulate and issue ONE reload', async () => {
  const env = await boot({ sessionData: remux() });
  const video = env.document.getElementById('tvVideo');
  const scrubber = env.document.getElementById('mediaScrubber');
  scrubber.setAttribute('data-couch-engaged', ''); // couch.js's engage protocol
  video.currentTime = 100;
  const before = env.window.__fetchCalls ? env.window.__fetchCalls.length : 0;

  for (let i = 0; i < 5; i++) arrowKey(env, scrubber, 'ArrowRight'); // 5 presses, key-repeat fast
  // The bar must track the presses immediately, without waiting for any server round-trip.
  assert.strictEqual(env.document.getElementById('mediaCur').textContent, '2:30', 'scrubber shows 100+50s at once');

  await new Promise((r) => setTimeout(r, 400)); // past the 250ms arrow debounce
  await flush();
  const src = video.getAttribute('src') || '';
  // The whole point: five presses move 50s, not 10s. Dropping the in-flight seeks
  // (the old `if (reloading) return`) lost the accumulation and under-seeked.
  assert.ok(/[?&]start=150\b/.test(src), `five +10s presses land at 150s: got ${src}`);
});

test('seeking a PAUSED progressive stream leaves it paused', async () => {
  const env = await boot({ sessionData: remux() });
  const video = env.document.getElementById('tvVideo');
  const scrubber = env.document.getElementById('mediaScrubber');
  scrubber.setAttribute('data-couch-engaged', '');
  video.pause();
  assert.strictEqual(video.paused, true, 'precondition: paused');

  video.currentTime = 60;
  arrowKey(env, scrubber, 'ArrowRight');
  await new Promise((r) => setTimeout(r, 400));
  await flush();

  assert.ok(/[?&]start=70\b/.test(video.getAttribute('src') || ''), 'the seek still happened');
  // attachSource sets autoplay = !startPaused, so a resumed seek would show here.
  assert.strictEqual(video.autoplay, false, 'a paused seek must not switch autoplay back on');
  assert.strictEqual(video.paused, true, 'a paused seek must not resume playback');
});

// --- stream_start_seconds: anchoring the offset at the ACTUAL stream start ---
// A remux stream-copies video, so the server's input -ss lands on the previous
// keyframe — up to one GOP before the requested position. The session reports
// where the stream really begins (stream_start_seconds); the client anchors
// streamStartOffset there while the ?start= URL still carries the REQUEST.

test('a progressive resume anchors the timeline at the server-reported stream start', async () => {
  const env = await boot({ sessionData: remux({ resume_position_seconds: 90, stream_start_seconds: 86 }) });
  const video = env.document.getElementById('tvVideo');
  const src = video.getAttribute('src') || '';
  assert.ok(/[?&]start=90\b/.test(src), `the URL carries the REQUESTED position: got ${src}`);
  video.currentTime = 5;
  video.dispatchEvent(new env.window.Event('timeupdate'));
  assert.strictEqual(env.document.getElementById('mediaCur').textContent, '1:31', 'elapsed = 86 (actual start) + 5, not 90 + 5');
  const bodies = await beaconBodies(env);
  const last = JSON.parse(bodies[bodies.length - 1]);
  assert.strictEqual(last.position_seconds, 91, 'progress reports the real timeline position');
});

test('a missing or out-of-range stream_start_seconds falls back to the requested position', async () => {
  for (const extra of [{}, { stream_start_seconds: 95 }, { stream_start_seconds: -3 }]) {
    const env = await boot({ sessionData: remux(Object.assign({ resume_position_seconds: 90 }, extra)) });
    const video = env.document.getElementById('tvVideo');
    video.currentTime = 5;
    video.dispatchEvent(new env.window.Event('timeupdate'));
    assert.strictEqual(env.document.getElementById('mediaCur').textContent, '1:35',
      `offset falls back to the request for ${JSON.stringify(extra)}`);
  }
});

test('an in-play seek re-fetches the session WITH the target (&start=) so the server can compute the landing', async () => {
  const env = await boot({ sessionData: remux() });
  const video = env.document.getElementById('tvVideo');
  // The boot fetch carries no start — the server derives the landing from the
  // resume position the same response hands back.
  const bootCall = env.fetch.calls.find((c) => c.url.indexOf('/tv/playback-session') >= 0);
  assert.ok(bootCall && bootCall.url.indexOf('start=') < 0, `boot session fetch has no start: ${bootCall && bootCall.url}`);
  video.currentTime = 42;
  env.document.getElementById('tvReloadBtn').dispatchEvent(new env.window.Event('click'));
  await flush();
  const seekCall = env.fetch.calls.filter((c) => c.url.indexOf('/tv/playback-session') >= 0).pop();
  assert.ok(/[?&]start=42\b/.test(seekCall.url), `seek session fetch carries the target: ${seekCall.url}`);
});

test('the FF/RW scan commit DOES resume — the one seek that must override "stay paused"', async () => {
  const env = await boot({ sessionData: remux() });
  const video = env.document.getElementById('tvVideo');
  video.play();
  video.currentTime = 200;

  // Fast-forward scan: the first press pauses the video and runs a virtual playhead.
  env.document.getElementById('tvForwardBtn').dispatchEvent(new env.window.Event('click'));
  assert.strictEqual(video.paused, true, 'the scan pauses playback in place');

  // Play commits the scan. Because the scan left the element paused, a naive
  // "preserve video.paused" seek would strand the player frozen at the scanned frame.
  env.document.getElementById('tvToggleBtn').dispatchEvent(new env.window.Event('click'));
  await new Promise((r) => setTimeout(r, 50));
  await flush();
  assert.strictEqual(video.autoplay, true, 'the scan commit resumes: autoplay stays on');
  assert.strictEqual(video.paused, false, 'playback resumed after committing the scan');
});

test('a dead progressive stream is detected by the stall watchdog and re-anchored', async () => {
  const env = await boot({ sessionData: remux() });
  const video = env.document.getElementById('tvVideo');
  video.play();
  video.currentTime = 300;
  video.dispatchEvent(new env.window.Event('timeupdate'));

  // Now the ffmpeg behind the stream dies. Verified in a real Chrome: the element
  // fires NO 'error' and NO 'ended' — it fires waiting/stalled and then freezes with
  // readyState starved and currentTime never advancing again. Reproduce exactly that:
  video.readyState = 2; // HAVE_CURRENT_DATA — nothing further to play
  video.dispatchEvent(new env.window.Event('waiting'));
  video.dispatchEvent(new env.window.Event('stalled'));

  // Let the watchdog tick once at the REAL clock first, so it records 300s as the
  // last known-good position. (Jumping the clock before this baseline exists makes
  // the very first tick record the shifted time, and no elapsed stall is ever seen.)
  await new Promise((r) => setTimeout(r, 1200));

  // Now jump the clock past the stall window. The next tick sees a frozen position
  // and a starved buffer, and re-anchors the stream where it died.
  const realNow = env.window.Date.now;
  env.window.Date.now = () => realNow() + 20000;
  await new Promise((r) => setTimeout(r, 1300));
  await flush();
  env.window.Date.now = realNow;

  const src = video.getAttribute('src') || '';
  assert.ok(/[?&]start=30[0-9]\b/.test(src), `the stalled stream is re-anchored at ~300s: got ${src}`);
});

test('a progressive stream that dies mid-episode must NOT mark it watched or auto-advance', async () => {
  // The real failure, caught by killing the ffmpeg behind a live episode in Chrome:
  // the handler returns normally, so Go closes the chunked response CLEANLY, and the
  // browser — seeing a complete resource — fires 'ended' right there. It fired ~30s
  // into a 29-minute file, which marked the episode WATCHED and auto-advanced Up Next
  // into the next one. A genuine end happens near the end; anything else is a corpse.
  const env = await boot({ fixtureOpts: { nextFile: 9 }, sessionData: remux() }); // duration 3661
  const video = env.document.getElementById('tvVideo');
  video.play();
  playTo(env, video, 30); // we actually watched 30s of a 61-minute episode
  // Now the stream dies. Chrome snaps currentTime (and duration, and buffered) up to
  // the declared duration, so the element looks exactly like a finished episode.
  video.currentTime = 3661;
  video.dispatchEvent(new env.window.Event('ended')); // the lie
  await flush();

  const upnext = env.document.querySelector('.media-upnext');
  assert.ok(!upnext || upnext.hidden, 'a dead stream must NOT open the Up Next countdown');
  const bodies = await beaconBodies(env);
  assert.ok(!bodies.some((b) => /"completed":true/.test(b)),
    'a dead stream must NOT report the episode completed — that destroys the resume position');
  // …and it re-anchors the stream where it died rather than leaving a dead player.
  assert.ok(/[?&]start=30\b/.test(video.getAttribute('src') || ''),
    `the stream is re-anchored where the DATA stopped, not where currentTime claimed: got ${video.getAttribute('src')}`);
});

test('a genuine end (at the duration) still completes and offers Up Next', async () => {
  const env = await boot({ fixtureOpts: { nextFile: 9 }, sessionData: remux() }); // duration 3661
  const video = env.document.getElementById('tvVideo');
  video.play();
  playTo(env, video, 3660); // genuinely watched through to the end
  video.dispatchEvent(new env.window.Event('ended'));
  await flush();

  const upnext = env.document.querySelector('.media-upnext');
  assert.ok(upnext && !upnext.hidden, 'finishing the episode still offers Up Next');
  const bodies = await beaconBodies(env);
  assert.ok(bodies.some((b) => /"completed":true/.test(b)), 'finishing still marks it watched');
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

test('FF/RW scan: Escape / remote Back cancels without seeking and resumes', async () => {
  for (const key of ['Escape', 'BrowserBack']) {
    const env = await boot();
    const doc = env.document;
    const video = doc.getElementById('tvVideo');

    video.currentTime = 50;
    doc.getElementById('tvForwardBtn').click();
    await new Promise((r) => setTimeout(r, 450)); // virtual playhead runs ahead
    const e = new env.window.KeyboardEvent('keydown', { key, bubbles: true, cancelable: true });
    doc.dispatchEvent(e);
    assert.strictEqual(doc.getElementById('mediaScanPill').hidden, true, `${key} cancels the scan`);
    assert.strictEqual(video.currentTime, 50, `${key}: no seek — position untouched`);
    assert.ok(doc.getElementById('tvTransport').classList.contains('playing'), `${key}: playback resumed`);
    assert.ok(e.defaultPrevented, `${key}: consumed so couch.js never navigates mid-scan`);
  }
});

test('FF/RW scan: Escape while fullscreen is left to the native exit (no cancel); Back still cancels', async () => {
  const env = await boot();
  const doc = env.document;
  Object.defineProperty(doc, 'fullscreenElement', { configurable: true, value: doc.documentElement });

  doc.getElementById('tvForwardBtn').click();
  const esc = new env.window.KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true });
  doc.dispatchEvent(esc);
  assert.strictEqual(doc.getElementById('mediaScanPill').hidden, false, 'scan survives — fullscreen owns Escape');
  assert.ok(!esc.defaultPrevented, 'Escape left untouched for the browser');

  doc.dispatchEvent(new env.window.KeyboardEvent('keydown', { key: 'BrowserBack', bubbles: true, cancelable: true }));
  assert.strictEqual(doc.getElementById('mediaScanPill').hidden, true, 'remote Back cancels even in fullscreen');
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
  assert.strictEqual(prev.title, 'Previous clip or restart', 'episode wording swapped for clips');
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

test('a dead-end arrow brings the auto-hidden chrome back up', async () => {
  // The ring can sit outside the player (the page header) with the overlay hidden.
  // An Up/Down that moves it nowhere means the viewer is at the edge of the page's
  // navigation — that press must surface the controls rather than do nothing. (In
  // fullscreen the header is out of the layout entirely, app.css, so the ring can't
  // strand there in the first place; this is the backstop for every other case.)
  const env = await boot();
  const doc = env.document;
  const wrap = doc.querySelector('.tv-player-video-wrap');
  doc.getElementById('tvVideo').paused = false; // playing — the hide gate is live
  await new Promise((r) => setTimeout(r, 2700)); // HIDE_MS
  assert.ok(wrap.classList.contains('controls-hidden'), 'controls auto-hid');

  // Focus something outside the player and press an arrow that moves nothing.
  // (No couch.js here, so focus never moves — exactly the dead-end condition.)
  const outside = doc.createElement('button');
  doc.body.appendChild(outside);
  outside.focus();
  doc.dispatchEvent(new env.window.KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }));
  await new Promise((r) => setTimeout(r, 20));
  assert.ok(!wrap.classList.contains('controls-hidden'), 'a dead-end ArrowDown revealed the chrome');

  // …and Up behaves the same.
  await new Promise((r) => setTimeout(r, 2700));
  assert.ok(wrap.classList.contains('controls-hidden'), 'controls hid again');
  doc.dispatchEvent(new env.window.KeyboardEvent('keydown', { key: 'ArrowUp', bubbles: true }));
  await new Promise((r) => setTimeout(r, 20));
  assert.ok(!wrap.classList.contains('controls-hidden'), 'a dead-end ArrowUp revealed the chrome');
});

test('remote: the chrome hides over a focused control and an arrow restores the ring', async () => {
  // The v0.33.6 no-blur fix left the remote's ring pinning the overlay open
  // after a track pick. Now the idle hide parks focus on the toggle (OK while
  // hidden still = pause) and the next ARROW restores the ring where it was.
  const env = await boot();
  const doc = env.document;
  const wrap = doc.querySelector('.tv-player-video-wrap');
  const mute = doc.getElementById('tvMuteBtn');
  doc.getElementById('tvVideo').paused = false;
  mute.focus(); // the remote's ring rests on an overlay control (no using-mouse class → remote path)
  await new Promise((r) => setTimeout(r, 2700));
  assert.ok(wrap.classList.contains('controls-hidden'), 'chrome hid despite the focused control');
  assert.strictEqual(doc.activeElement, doc.getElementById('tvToggleBtn'), 'focus parked on the toggle');

  doc.activeElement.dispatchEvent(new env.window.KeyboardEvent('keydown', { key: 'ArrowLeft', bubbles: true }));
  await new Promise((r) => setTimeout(r, 20));
  assert.ok(!wrap.classList.contains('controls-hidden'), 'the arrow revealed the chrome');
  assert.strictEqual(doc.activeElement, mute, 'and restored the ring to the stashed control');
});

test('remote: Enter while hidden stays the pause idiom — no ring restore', async () => {
  const env = await boot();
  const doc = env.document;
  const wrap = doc.querySelector('.tv-player-video-wrap');
  const mute = doc.getElementById('tvMuteBtn');
  const toggle = doc.getElementById('tvToggleBtn');
  doc.getElementById('tvVideo').paused = false;
  mute.focus();
  await new Promise((r) => setTimeout(r, 2700));
  assert.ok(wrap.classList.contains('controls-hidden'), 'chrome hid');

  toggle.dispatchEvent(new env.window.KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));
  await new Promise((r) => setTimeout(r, 20));
  assert.ok(!wrap.classList.contains('controls-hidden'), 'Enter revealed the chrome (bump)');
  assert.strictEqual(doc.activeElement, toggle, 'but the ring stayed on the toggle — Enter never re-engages a picker');

  // The reveal invalidated the stash: hiding again with the toggle focused and
  // then pressing an arrow must NOT jump back to the long-abandoned control.
  await new Promise((r) => setTimeout(r, 2700));
  assert.ok(wrap.classList.contains('controls-hidden'), 'chrome hid again');
  toggle.dispatchEvent(new env.window.KeyboardEvent('keydown', { key: 'ArrowLeft', bubbles: true }));
  await new Promise((r) => setTimeout(r, 20));
  assert.notStrictEqual(doc.activeElement, mute, 'a stale stash never restores');
});

test('an engaged control is never hidden over mid-adjust', async () => {
  // Yanking focus off an engaged control would fire couch's focusout
  // auto-release and COMMIT an engaged select's change — the hide must wait.
  const env = await boot();
  const doc = env.document;
  const wrap = doc.querySelector('.tv-player-video-wrap');
  const vol = doc.getElementById('tvVolume');
  doc.getElementById('tvVideo').paused = false;
  vol.setAttribute('data-couch-engaged', '');
  vol.focus();
  await new Promise((r) => setTimeout(r, 2700));
  assert.ok(!wrap.classList.contains('controls-hidden'), 'chrome stayed up over the engaged control');
});

test('mouse path never parks focus on the toggle (park/stash is remote-only)', async () => {
  // jsdom's :focus-visible matches ANY focused element (Chrome's mouse-click
  // heuristic doesn't exist here), so whether the mouse path pins or blurs is
  // Chrome territory (the Playwright/live ceiling). What IS testable — and is
  // the invariant this change must hold — is that mouse modality never takes
  // the remote branch: focus is never moved to the toggle by the idle hide.
  const env = await boot();
  const doc = env.document;
  doc.documentElement.classList.add('using-mouse'); // couch's modality class
  const mute = doc.getElementById('tvMuteBtn');
  doc.getElementById('tvVideo').paused = false;
  mute.focus();
  await new Promise((r) => setTimeout(r, 2700));
  assert.notStrictEqual(doc.activeElement, doc.getElementById('tvToggleBtn'),
    'the mouse path never parks focus on the toggle');
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
