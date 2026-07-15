// media_player.js — the shared TV/movie video player controller.
//
// Extracted verbatim from tv_player.html's inline script so both the TV and movie
// player pages drive the same verified logic (session decision, hls.js, the
// transport row, dynamic-range compression, subtitles, resume + progress
// reporting, Turbo teardown). The only per-media-type differences are the
// endpoint URLs and page framing: the URLs are derived from the video element's
// data-media-kind ("tv" | "movie"), the framing lives in the page template.
//
// Loaded once from the layout shell (defer) and run on turbo:load, mirroring
// couch.js/subtabs.js — so it survives Turbo body swaps without a body-script
// race. initMediaPlayer is a no-op on pages with no #tvVideo.
function mediaPlayerConfig(kind) {
  if (kind === 'photo') {
    // Home-video clips in a photos library — same player, photo endpoints.
    return {
      sessionURL: '/photo/playback-session',
      progressURL: '/photo/playback-progress',
      playerURL: '/photo/player',
      progressiveRe: /\/stream\/photo-(remux|burnin)\//,
      subtitleSearchURL: '/photo/subtitles/search',
      subtitleFetchURL: '/photo/subtitles/fetch',
    };
  }
  if (kind === 'movie') {
    return {
      sessionURL: '/movie/playback-session',
      progressURL: '/movie/playback-progress',
      playerURL: '/movie/player',
      progressiveRe: /\/stream\/movie-(remux|burnin)\//,
      // Movies don't wire OpenSubtitles search (the button stays hidden); these
      // are unused but defined so the dialog code never sees undefined URLs.
      subtitleSearchURL: '/movie/subtitles/search',
      subtitleFetchURL: '/movie/subtitles/fetch',
    };
  }
  return {
    sessionURL: '/tv/playback-session',
    progressURL: '/tv/playback-progress',
    playerURL: '/tv/player',
    progressiveRe: /\/stream\/tv-(remux|burnin)\//,
    subtitleSearchURL: '/tv/subtitles/search',
    subtitleFetchURL: '/tv/subtitles/fetch',
  };
}

function initMediaPlayer() {
  const video = document.getElementById('tvVideo');
  if (!video) return;
  const cfg = mediaPlayerConfig(video.dataset.mediaKind);

  const fileID = parseInt(video.dataset.fileId, 10);
  const prevFile = parseInt(video.dataset.prevFile, 10) || 0;
  const nextFile = parseInt(video.dataset.nextFile, 10) || 0;
  const audioSelect = document.getElementById('audioSelect');
  const subSelect = document.getElementById('subSelect');
  const modeLabel = document.getElementById('playbackMode');

  let hls = null;          // active hls.js instance, if any
  let currentAud = 0, currentSub = 0;
  let selectsBuilt = false;
  let lastReport = 0;
  let completedReported = false; // gate the 90%-watched completion report to fire once
  let streamStartOffset = 0; // server pre-seek of a progressive stream (remux/burn-in); see attachSource
  let sessionDuration = 0;   // full source duration (progressive streams don't expose it via video.duration)
  let isProgressive = false; // current stream is remux/burn-in (linear, seeks via ?start= reload)
  let reloading = false;     // a ?start= reload is in flight (re-entrancy guard for seeks)
  let skipSegments = [];     // intro/recap/commercial ranges (absolute timeline) from the session
  let chapterList = [];      // raw chapter starts (absolute timeline) — seek-bar ticks
  let renderScrubMarks = () => {}; // bound to the real renderer once the scrubber wires up
  // Trickplay-preview hooks for the FF/RW scan — bound to the real preview
  // functions once the scrubber wires up (same pattern as renderScrubMarks).
  let scanShowPreview = () => {}, scanHidePreview = () => {}, scanLoadPreview = () => {};
  const subBurnIn = new Set(); // subtitle ordinals the server burns in (bitmap subs) — these change the video stream
  let hlsFails = 0;               // consecutive fatal hls.js errors with no buffered progress
  const HLS_FAIL_CAP = 4;         // give up (vs. loop) after this many fatals without a FRAG_BUFFERED
  let destroyed = false;          // this player is being torn down (Turbo swap) — stop all timers/recovery
  // The furthest point actually WATCHED, grown only by continuous playback. It is the
  // one thing a dying progressive stream can't fake — see endedIsGenuine. Declared up
  // here because attachSource (far above its listeners) anchors it on every load.
  let lastPlayedAbs = 0;

  const nativeHLS = video.canPlayType('application/vnd.apple.mpegurl') !== '';

  // currentAbsTime is the playback position on the real episode timeline. The
  // remux/burn-in streams are rebased to zero at their server-side start, so their
  // video.currentTime is relative — add the offset back to get the true position.
  const currentAbsTime = () => streamStartOffset + (video.currentTime || 0);

  function teardownHLS() {
    if (hls) { hls.destroy(); hls = null; }
  }

  // Buffering spinner — a self-owned overlay in the video wrap (mirrors
  // .media-captions), pointer-events:none so it never eats the click-to-toggle-
  // play. Driven by the element's own buffering events, so it covers every
  // playback path (direct/remux/burn-in/HLS/photo), not just hls.js: shown while
  // the element is starved for data (waiting/loadstart), hidden once it plays or
  // pauses. The pause-hide keeps an autoplay-blocked video (play() rejected, so
  // the element sits paused) from spinning forever while it awaits a user gesture.
  const spinner = (() => {
    const w = video.closest('.tv-player-video-wrap');
    if (!w) return null;
    let el = w.querySelector('.media-spinner');
    if (!el) { el = document.createElement('div'); el.className = 'media-spinner'; el.hidden = true; w.appendChild(el); }
    return el;
  })();
  const showSpinner = () => { if (spinner) spinner.hidden = false; };
  const hideSpinner = () => { if (spinner) spinner.hidden = true; };
  ['waiting', 'loadstart'].forEach((e) => video.addEventListener(e, showSpinner));
  // 'loadeddata' hides the spinner once the first frame at the current position is
  // decoded — this is what clears it for a deliberately-paused resume start (which
  // never fires 'playing'); benign for normal playback (fires once, early).
  ['playing', 'pause', 'ended', 'error', 'loadeddata'].forEach((e) => video.addEventListener(e, hideSpinner));

  // --- progressive stall watchdog ---
  // A remux/burn-in stream is one long ffmpeg pipe with no seek index. If that
  // ffmpeg dies mid-episode (killed, OOM, a bad region of the file), the HTTP body
  // simply ends — and the element does NOT tell us. Verified in a real Chrome
  // against a truncated stream: it fires 'waiting' then 'stalled' and hangs there
  // FOREVER — no 'error', no 'ended', networkState stuck at LOADING, currentTime
  // frozen (34s later: zero progress, zero events). So there is no event to listen
  // for; the only detector is noticing that time stopped moving. hls.js already owns
  // this for the HLS path (its ERROR handler retries), hence the isProgressive gate.
  // Recovery is the seek we already have: re-anchor the stream at the stall point.
  const STALL_SECS = 8;            // frozen this long while trying to play → dead stream
  const STALL_RETRY_CAP = 3;       // then stop retrying and say so (mirrors HLS_FAIL_CAP)
  const STALL_RECOVERED_SECS = 5;  // this much real playback after a retry = recovered
  let stallRetries = 0;
  let recoveredAnchor = -1;        // abs position of the last retry, pending confirmation
  let lastProgressAt = 0, lastProgressPos = -1, stallTimer = null;
  // Track the ABSOLUTE position, not video.currentTime: a recovery reload rebases the
  // element to zero, so currentTime would read as a huge jump and look like progress —
  // which would clear the retry budget on every retry and loop forever.
  const noteProgress = () => { lastProgressAt = Date.now(); lastProgressPos = currentAbsTime(); };
  function checkStall() {
    // Only a stream that is *trying* to play can be stalled. A deliberate pause, an
    // in-flight reload, or a healthy buffer are all fine.
    if (!isProgressive || video.paused || video.ended || reloading || destroyed) { noteProgress(); return; }
    if (currentAbsTime() !== lastProgressPos) { noteProgress(); return; }
    // Frozen AND starved: readyState < HAVE_FUTURE_DATA means it has nothing to play
    // next, which distinguishes a dead pipe from a merely slow tick.
    if (video.readyState >= 3 /* HAVE_FUTURE_DATA */) { noteProgress(); return; }
    if (Date.now() - lastProgressAt < STALL_SECS * 1000) return;
    if (stallRetries >= STALL_RETRY_CAP) {
      modeLabel.textContent = 'Playback stopped — reload to continue';
      hideSpinner();
      return;
    }
    stallRetries++;
    recoveredAnchor = currentAbsTime();
    console.warn('hespera: progressive stream stalled, re-anchoring at', recoveredAnchor);
    noteProgress();
    loadFromSession(currentAud, currentSub, recoveredAnchor);
  }
  stallTimer = setInterval(checkStall, 1000);
  // Real playback after a retry clears the budget (the FRAG_BUFFERED analogue), so a
  // long healthy session can never exhaust it — but a retry that buys nothing can't
  // refill it either.
  video.addEventListener('timeupdate', () => {
    if (recoveredAnchor >= 0 && currentAbsTime() > recoveredAnchor + STALL_RECOVERED_SECS) {
      stallRetries = 0;
      recoveredAnchor = -1;
    }
    // Deliberately NOT calling noteProgress() here: it would refresh the stall clock
    // on any timeupdate, and a browser that ticks timeupdate without advancing the
    // position would keep the watchdog asleep forever. checkStall compares the
    // position itself, which is the only honest signal.
  });
  ['playing', 'seeking', 'loadstart'].forEach((e) => video.addEventListener(e, noteProgress));

  // Anamorphic-aspect correction: some sources (PAL DVD rips) are stored at one
  // pixel grid (e.g. 702×576) but flagged to display at another (16:9) via
  // container SAR — a flag the pipeline/browser can lose, rendering the picture
  // squeezed. The session carries the authoritative display aspect (video_dar);
  // when the browser's own rendering (videoWidth/Height reflect any aspect
  // metadata it honored) disagrees by >2%, CSS reshapes the element to the true
  // aspect and stretches the frame into it (.aspect-fix + --dar). A file the
  // browser gets right never engages — rendering stays identical to before.
  let expectedDAR = 0;
  const applyAspectFix = () => {
    const w = video.videoWidth, h = video.videoHeight;
    const fix = expectedDAR > 0 && w > 0 && h > 0 &&
      Math.abs(w / h - expectedDAR) / expectedDAR > 0.02;
    video.classList.toggle('aspect-fix', fix);
    if (fix) video.style.setProperty('--dar', String(expectedDAR));
    else video.style.removeProperty('--dar');
  };
  video.addEventListener('loadedmetadata', applyAspectFix);
  // 'resize' is the load-bearing hook: on the progressive remux path the
  // empty_moov fMP4 carries no dimensions up front, so loadedmetadata fires
  // with videoWidth 0 and the real dims only arrive with the first fragment —
  // announced by this event (also re-evaluates any mid-stream dim change).
  video.addEventListener('resize', applyAspectFix);

  // attachSource points the element (or hls.js) at the stream. seekTo is the
  // desired position on the real episode timeline. Direct-play and HLS are
  // byte-range/segment seekable, so we set video.currentTime. Remux and burn-in
  // are progressive (no random access), so instead we ask the server to begin the
  // stream at seekTo (?start=, an input -ss) and track the offset; the element's
  // own currentTime then runs from zero. This is what lets those paths resume.
  function attachSource(session, seekTo, startPaused) {
    teardownHLS();
    expectedDAR = session.video_dar || 0;
    // The <video> carries the native `autoplay` attribute, which fires the moment a
    // src attaches — independent of the explicit play() below. Clear it for a paused
    // start (before any src is set) or it would defeat the pause; restore it
    // otherwise so seeks/track-changes keep autoplaying as before.
    video.autoplay = !startPaused;
    hlsFails = 0; // fresh stream → fresh fatal-error budget
    isProgressive = cfg.progressiveRe.test(session.url || '');
    const progressive = isProgressive;
    let url = session.url;
    if (progressive && seekTo > 0) {
      streamStartOffset = seekTo;
      url += (url.indexOf('?') >= 0 ? '&' : '?') + 'start=' + encodeURIComponent(seekTo);
    } else {
      streamStartOffset = 0;
    }
    const clientSeek = progressive ? 0 : seekTo; // only the seekable paths seek the element
    const onReady = () => { if (clientSeek > 0) { try { video.currentTime = clientSeek; } catch (e) {} } };
    // Anchor the continuity tracker at the intended start; playback grows it from
    // here, and an 'ended' that arrives without it having grown is a dead stream.
    lastPlayedAbs = Math.max(0, seekTo || 0);

    // Prefer hls.js whenever MSE supports it (Chrome, Firefox, desktop Safari) — the
    // whole transcode player (seeking, error recovery, self-rendered subtitles) is built
    // on hls.js/MSE. Native <video src> HLS is the fallback only for MSE-less browsers
    // (iOS Safari). We must NOT gate on !nativeHLS: modern Chrome reports HLS as playable
    // ('maybe' from canPlayType) yet its native HLS wedges on a resume/seek (seg0-then-jump
    // DTS append failure), so trusting it silently bypassed the entire hls.js player.
    if (session.protocol === 'hls' && window.Hls && Hls.isSupported()) {
      // startPosition makes hls.js load the seek/resume segment FIRST, rather than
      // loading seg0 and then jumping via onReady's currentTime set. The seg0-then-jump
      // appends fragments out of DTS order on a fresh pipeline, which Chrome MSE rejects
      // (CHUNK_DEMUXER_ERROR_APPEND_FAILED: "not in DTS sequence") — wedging any resume/seek
      // into a transcoded file. Loading the target segment first keeps the appends in order.
      // Our HLS segments transcode on demand: the server holds the connection
      // open until the segment is built (no bytes until then), so a fragment's
      // time-to-first-byte IS its full transcode time — which, on a loaded box,
      // easily exceeds hls.js's default 10s maxTimeToFirstByteMs. That timeout
      // fires a fatal NETWORK_ERROR, the handler below restarts loading, and the
      // stream thrashes (buffer/restart/buffer/restart). Raise the client's
      // patience to match the server's own ceiling (segBuildTimeout, 5min, in
      // internal/video/ffmpeg.go) so it WAITS for a slow segment instead of
      // giving up. The full default object is supplied verbatim (only the two
      // timeouts change) because hls.js shallow-merges config — a partial policy
      // would silently drop errorRetry and stop retrying genuine failures.
      hls = new Hls({
        enableWorker: true,
        startPosition: clientSeek > 0 ? clientSeek : -1,
        fragLoadPolicy: {
          default: {
            maxTimeToFirstByteMs: 300000, // mirror server segBuildTimeout (5min)
            maxLoadTimeMs: 300000,
            timeoutRetry: { maxNumRetry: 4, retryDelayMs: 0, maxRetryDelayMs: 0 },
            errorRetry: { maxNumRetry: 6, retryDelayMs: 1000, maxRetryDelayMs: 8000 },
          },
        },
      });
      hls.loadSource(url);
      hls.attachMedia(video);
      hls.on(Hls.Events.MANIFEST_PARSED, onReady);
      // A buffered fragment is real forward progress — the stream recovered, so
      // reset the fatal-error budget. This is what lets a long, healthy stream
      // survive the occasional transient error without ever exhausting the cap.
      hls.on(Hls.Events.FRAG_BUFFERED, () => { hlsFails = 0; });
      // A fatal hls.js error (e.g. a transient MSE append/parse failure on a seek —
      // CHUNK_DEMUXER_ERROR_APPEND_FAILED, or a segment whose cold transcode outran
      // hls.js's fragment-load timeout) otherwise leaves the pipeline idle and the
      // screen black until a manual reload. Run the documented recovery: restart
      // loading on a network error, re-init the media (then swap-audio-codec) on a
      // media error. Bound BOTH paths by a consecutive-fatal cap so a genuinely
      // stuck stream degrades to a message instead of thrashing play/restart/play/
      // restart forever — the count resets on any FRAG_BUFFERED above, so only
      // failures with no progress between them count toward the cap.
      hls.on(Hls.Events.ERROR, (_evt, data) => {
        if (!data || !data.fatal) return;
        if (++hlsFails > HLS_FAIL_CAP) {
          console.warn('[hespera] HLS gave up after', HLS_FAIL_CAP, 'fatal errors without progress; last:', data.type, data.details);
          teardownHLS();
          hideSpinner();
          modeLabel.textContent = 'Playback error — reload to continue';
          return;
        }
        console.warn('[hespera] HLS fatal', data.type, data.details, '— recovery', hlsFails, 'of', HLS_FAIL_CAP);
        if (data.type === Hls.ErrorTypes.NETWORK_ERROR) {
          hls.startLoad();
          return;
        }
        if (data.type === Hls.ErrorTypes.MEDIA_ERROR) {
          if (hlsFails === 1) hls.recoverMediaError();
          else { hls.swapAudioCodec(); hls.recoverMediaError(); }
          return;
        }
        teardownHLS();
        hideSpinner();
        modeLabel.textContent = 'Playback error — reload to continue';
      });
    } else {
      // Direct play, remux, burn-in, native-HLS (Safari): the element loads the URL directly.
      video.src = url;
      video.addEventListener('loadedmetadata', onReady, { once: true });
      video.load();
    }
    if (startPaused) {
      // A resume-from-home load lands at the saved position but PAUSED, so the
      // user starts playback when ready (no audio blast on arrival). The seek
      // above still runs (onReady / hls startPosition / progressive ?start=); we
      // just skip autoplay. Reflect paused so a hardware key toggles to play.
      if ('mediaSession' in navigator) { try { navigator.mediaSession.playbackState = 'paused'; } catch (e) {} }
    } else {
      video.play().catch(() => {}); // autoplay may be blocked; user can press play
    }
  }

  // --- subtitle rendering ---
  // We paint subtitles ourselves rather than relying on the browser's native cue
  // rendering: under MSE/hls.js the UA leaves orphaned cue boxes across track
  // add/remove (toggling subtitles off/on/off/on stacks stale lines) and on
  // switch-off (the last line sticks). The sidecar <track> is kept mode='hidden'
  // — cues are still parsed and activeCues/cuechange still fire, but the UA paints
  // nothing — and we render the active cue(s) into one overlay div we fully own,
  // so stacking and stranding are structurally impossible.
  const captions = (() => {
    const w = video.closest('.tv-player-video-wrap');
    if (!w) return null;
    let el = w.querySelector('.media-captions');
    if (!el) { el = document.createElement('div'); el.className = 'media-captions'; el.hidden = true; w.appendChild(el); }
    return el;
  })();
  let captionTrack = null; // the sidecar TextTrack we're painting, if any
  let lastCueKey = '';     // change-detection so we only touch the DOM on a real change
  const rvfcSupported = typeof video.requestVideoFrameCallback === 'function';
  let rvfcRunning = false;

  // The active cue(s) are computed from a media-clock time against the parsed cue
  // list — NOT read from captionTrack.activeCues. The clock is the REAL episode
  // timeline (currentAbsTime): the frame's exact presentation time
  // (requestVideoFrameCallback's metadata.mediaTime) plus streamStartOffset where
  // supported, else currentAbsTime(). Sidecar VTTs (embedded and OpenSubtitles)
  // are extracted with no -ss, so their cue times are absolute; a resumed
  // progressive stream (remux/burn-in) is rebased to zero, so we must add the
  // offset back or the cues shift by the resume position and never paint.
  // Seekable paths (HLS via -output_ts_offset, direct) carry offset 0, so this is
  // identical to video.currentTime there. The browser's own TextTrack cue
  // scheduler (activeCues/cuechange) can drift ahead of that clock over a long
  // MSE/HLS session — subtitles creeping earlier and earlier until you toggle
  // them — so we never touch it. A linear scan of an episode's cues per frame is
  // trivial (a few µs).
  function computeActiveCues(t) {
    const all = captionTrack && captionTrack.cues;
    if (!all || !all.length) return [];
    if (typeof t !== 'number') t = currentAbsTime(); // event-handler arg / no-arg → real episode time
    const out = [];
    for (let i = 0; i < all.length; i++) {
      const c = all[i];
      if (c.startTime <= t && t < c.endTime) out.push(c);
    }
    return out;
  }

  function renderActiveCues(t) {
    if (!captions) return;
    const cues = computeActiveCues(t);
    let key = '';
    for (let i = 0; i < cues.length; i++) key += cues[i].startTime + '|' + cues[i].text + '\n';
    if (key === lastCueKey) return;
    lastCueKey = key;
    captions.textContent = '';
    if (cues.length === 0) { captions.hidden = true; return; }
    for (let i = 0; i < cues.length; i++) {
      const line = document.createElement('div');
      line.className = 'media-caption-line';
      line.appendChild(cues[i].getCueAsHTML()); // preserves <i>/<b> markup + multi-line
      captions.appendChild(line);
    }
    captions.hidden = false;
  }

  // While a caption track is attached and frames are presenting, drive the render
  // from each presented frame's exact media time — frame-accurate cue boundaries,
  // with no dependence on the cue scheduler. requestVideoFrameCallback only fires
  // while frames present (paused/stalled → the cue correctly stays put), so the
  // unconditional timeupdate listener below remains the portable floor (and the
  // sole driver on browsers without rVFC). Self-stops when rvfcRunning clears.
  function frameTick(_now, meta) {
    if (!rvfcRunning) return;
    renderActiveCues(meta && typeof meta.mediaTime === 'number' ? meta.mediaTime + streamStartOffset : undefined);
    video.requestVideoFrameCallback(frameTick);
  }
  function startRVFC() {
    if (rvfcSupported && !rvfcRunning) { rvfcRunning = true; video.requestVideoFrameCallback(frameTick); }
  }
  function stopRVFC() { rvfcRunning = false; }

  function clearCaptionTrack() {
    stopRVFC();
    captionTrack = null;
    lastCueKey = '';
    if (captions) { captions.textContent = ''; captions.hidden = true; }
  }

  function addCaptionTrack(url) {
    const track = document.createElement('track');
    track.kind = 'subtitles';
    track.src = url;
    track.default = true;
    track.label = 'Subtitles';
    video.appendChild(track);
    const tt = video.textTracks[video.textTracks.length - 1];
    if (!tt) return;
    tt.mode = 'hidden'; // parsed but unpainted; renderActiveCues does the painting
    captionTrack = tt;
    renderActiveCues();
    startRVFC();
  }

  function attachSubtitle(session) {
    for (const t of [...video.querySelectorAll('track')]) t.remove();
    clearCaptionTrack();
    if (session.subtitle_url) addCaptionTrack(session.subtitle_url);
  }

  function buildSelects(session) {
    // The server echoes the ordinals it actually applied (it may have resolved
    // defaults: language-preference audio, subtitles-on) — the pickers must
    // show the served tracks, not assume disposition-default/Off.
    const appliedAud = session.applied_audio | 0;
    const appliedSub = session.applied_subtitle | 0;
    const audio = session.audio_tracks || [];
    if (audio.length > 1) {
      audioSelect.innerHTML = '';
      audio.forEach((a) => {
        const o = document.createElement('option');
        o.value = a.ordinal;
        o.textContent = [a.language, a.title, a.codec].filter(Boolean).join(' · ') || ('Track ' + a.ordinal);
        if (appliedAud > 0 ? a.ordinal === appliedAud : a.default) o.selected = true;
        audioSelect.appendChild(o);
      });
      document.getElementById('audioPick').hidden = false;
    }
    // Text subtitles deliver as a WebVTT sidecar; bitmap subs (PGS/DVD/DVB) are
    // burned into the video by a continuous server-side transcode. Offer both,
    // marking bitmap tracks so the transcode cost is visible. Each track keeps its
    // original 1-based ordinal (what the server expects). With an OpenSubtitles
    // key configured (TV only — the movie template never sets data-os-enabled),
    // the list leads with a "Search subtitles…" action option — one keypress above
    // the default Off — so the dropdown shows even for a file with no subtitle
    // tracks at all; the file's own tracks follow Off.
    const subs = session.subtitle_tracks || [];
    const osEnabled = video.dataset.osEnabled === '1';
    if (subs.length > 0 || osEnabled) {
      subSelect.innerHTML = '';
      if (osEnabled) {
        const search = document.createElement('option');
        search.value = 'search';
        search.textContent = 'Search subtitles…';
        subSelect.appendChild(search);
      }
      const off = document.createElement('option');
      off.value = 0; off.textContent = 'Off'; off.selected = appliedSub === 0;
      subSelect.appendChild(off);
      subs.forEach((s) => {
        const o = document.createElement('option');
        o.value = s.ordinal;
        const label = [s.language, s.title].filter(Boolean).join(' · ') || ('Subtitle ' + s.ordinal);
        o.textContent = s.text ? label : (label + ' · burn-in');
        if (s.ordinal === appliedSub) o.selected = true;
        subSelect.appendChild(o);
        if (!s.text) subBurnIn.add(Number(s.ordinal)); // bitmap → burned into the video stream
      });
      document.getElementById('subPick').hidden = false;
    }
    selectsBuilt = true;
  }

  // attachExternalSubtitle swaps in a fetched WebVTT sidecar (from the
  // OpenSubtitles search) as the active subtitle track.
  function attachExternalSubtitle(url) {
    for (const t of [...video.querySelectorAll('track')]) t.remove();
    clearCaptionTrack();
    addCaptionTrack(url);
  }

  async function loadFromSession(aud, sub, seekTo, subtitleOnly, startPaused) {
    currentAud = aud; currentSub = sub;
    let session;
    try {
      const resp = await fetch(`${cfg.sessionURL}?file=${fileID}&aud=${aud}&sub=${sub}`, { headers: { Accept: 'application/json' } });
      session = await resp.json();
    } catch (e) {
      modeLabel.textContent = 'Unable to start playback';
      return;
    }
    if (!session || !session.ok) { modeLabel.textContent = 'Unable to start playback'; return; }

    // Adopt the server's applied ordinals so subsequent track changes ride the
    // resolved tracks. Explicit sub off (sub === -1) is kept as-is: adopting
    // the echoed 0 would let a later audio change silently re-apply the
    // subtitles-on default the user just turned off.
    if (typeof session.applied_audio === 'number') currentAud = session.applied_audio;
    if (sub >= 0 && typeof session.applied_subtitle === 'number') currentSub = session.applied_subtitle;

    if (!selectsBuilt) buildSelects(session);
    // A text-subtitle change is a sidecar swap — the video stream is byte-identical
    // (the server only varies the URL for a burned-in bitmap sub). Reloading the
    // whole pipeline here is pointless and, worse, the fresh-pipeline + immediate
    // re-seek intermittently throws a fatal MSE append error → black screen. So
    // swap only the <track> and leave playback untouched (mirrors the external-
    // subtitle path). attachSource is still used for audio changes, burn-in subs,
    // progressive seeks, and the initial load (all pass subtitleOnly falsy).
    if (subtitleOnly) { attachSubtitle(session); return; }
    sessionDuration = session.duration_seconds || sessionDuration;
    skipSegments = session.skip_segments || [];
    chapterList = session.chapters || [];
    resetSkip();
    renderScrubMarks();
    // #playbackMode is the error surface only — the normal decision (direct/remux/
    // transcode) isn't shown (no viewer value, and it cluttered the control row).
    // Clear any prior error, then re-raise the one case the viewer must know about.
    modeLabel.textContent = '';
    if (session.protocol === 'hls' && !nativeHLS && !(window.Hls && Hls.isSupported())) {
      modeLabel.textContent = 'This browser cannot play the transcoded stream';
    }
    // The server owns "is there anything to resume" (resumePosition, handlers_stream_tv.go):
    // it already drops a position left at the end of a finished playthrough. Don't
    // also zero it here on session.completed — watched and resume are independent,
    // so a partial RE-watch of a watched episode must resume where it was paused.
    const resume = (seekTo != null) ? seekTo : (session.resume_position_seconds || 0);
    attachSource(session, resume, startPaused);
    attachSubtitle(session);
  }

  // blur() after a pick, but ONLY on the mouse path. A mouse-clicked <select>
  // matches :focus-visible in Chrome (unlike a <button>), so a lingering focus
  // would pin the auto-hiding .media-overlay open forever — that is what this
  // blur exists for (see hideControls). Blurring a REMOTE user's pick, though,
  // drops focus to <body>, and couch.js's next arrow then restarts from the first
  // focusable on the page — the title back-link — so the ring escapes the player
  // mid-episode. The remote keeps its ring on the picker; the overlay staying up
  // while a control is keyboard-focused is exactly what hideControls already wants.
  const usingMouse = () => document.documentElement.classList.contains('using-mouse'); // couch.js owns the class
  const blurIfMouse = (el) => { if (usingMouse()) el.blur(); };
  if (audioSelect) audioSelect.addEventListener('change', () => { loadFromSession(parseInt(audioSelect.value, 10) || 0, currentSub, currentAbsTime()); blurIfMouse(audioSelect); });
  if (subSelect) subSelect.addEventListener('change', () => {
    // The "Search subtitles…" action option: restore the previous selection
    // (picking an action must not switch subtitles off) and open the search
    // dialog. Checked before parseInt, which would misread it as Off.
    // (couch.js coalesces an engaged select's arrow steps into one change at
    // release, so arrowing PAST this option no longer fires the dialog — it opens
    // on the Enter that commits it.)
    if (subSelect.value === 'search') {
      subSelect.value = String(Math.max(0, currentSub)); // -1 (explicit off) displays as Off
      openSubsModal();
      blurIfMouse(subSelect);
      return;
    }
    const newSub = parseInt(subSelect.value, 10) || 0;
    // Reload the video stream only when burn-in is involved (entering or leaving a
    // bitmap sub changes the stream); a text/off↔text/off change is a sidecar swap.
    const reload = subBurnIn.has(newSub) || subBurnIn.has(currentSub);
    // Picking Off sends -1 (explicit off): a plain 0 reads as "unpinned" to the
    // server, which would re-apply the subtitles-on default against the user.
    loadFromSession(currentAud, newSub === 0 ? -1 : newSub, currentAbsTime(), !reload);
    blurIfMouse(subSelect);
  });

  // --- playback speed --- pure client-side (video.playbackRate; browsers
  // pitch-correct by default), persisted per device (tv_speed). Reapplied on
  // loadedmetadata so progressive ?start= reloads and hls re-attaches keep the
  // chosen rate.
  const speedSlider = document.getElementById('speedSlider');
  const speedVal = document.getElementById('speedVal');
  let speed = 1;
  try { speed = parseFloat(localStorage.getItem('tv_speed')) || 1; } catch (e) {}
  if (!(speed >= 0.5 && speed <= 2)) speed = 1; // a stale/garbage stored value never plays at an off-range rate
  const applySpeed = () => { try { video.playbackRate = speed; } catch (e) {} };
  const showSpeed = () => { if (speedVal) speedVal.textContent = speed + '×'; };
  if (speedSlider) {
    speedSlider.value = String(speed); // restore the saved rate onto the slider
    showSpeed();
    // input (not change) fires on drag AND on a couch arrow-step (the range is on
    // couch.js's engage protocol like the volume slider), so the rate + readout
    // track live; the position IS the value, so no write-back needed.
    speedSlider.addEventListener('input', () => {
      speed = parseFloat(speedSlider.value) || 1;
      try { localStorage.setItem('tv_speed', String(speed)); } catch (e) {}
      applySpeed();
      showSpeed();
    });
  }
  video.addEventListener('loadedmetadata', applySpeed);
  applySpeed();

  // --- transport controls (focusable, so a TV remote in couch mode can seek;
  //     the native <video controls> scrubber isn't remote-reachable) ---
  const transport = document.getElementById('tvTransport');
  const rewindBtn = document.getElementById('tvRewindBtn');
  const toggleBtn = document.getElementById('tvToggleBtn');
  const forwardBtn = document.getElementById('tvForwardBtn');
  // seekProgressiveTo moves a remux/burn-in stream to an absolute episode position.
  // These streams are empty_moov fragmented MP4 with NO seek index — verified live
  // that video.seekable is [0,0], so the element can't seek natively at all (a
  // currentTime assignment, even within the buffer, clamps to 0). Every seek
  // therefore re-anchors the stream server-side at the new ?start= (reusing the
  // resume -ss). The scrubber (drag-release + ±10s arrow keys) and the scan
  // commit drive it; the native <video> scrubber can't (it can't address an
  // unindexed stream). It is cheap — measured 0.2s to first byte, ~10x faster
  // than the HLS segment build it replaced for these files (a stream copy needs
  // no encode) — so a reload per seek is the right trade; it just must not lose
  // seeks or resume a paused player.
  const clampSeek = (t) => {
    t = Math.max(0, t);
    if (sessionDuration > 1 && t > sessionDuration - 1) t = sessionDuration - 1;
    return t;
  };
  // A seek requested while a reload is in flight is HELD, not dropped. Dropping it
  // lost the accumulation: five quick ±10s presses moved 10s, not 50s, because
  // presses 2-5 landed inside the ~0.2s reload window and vanished.
  let pendingSeek = null; // {target, keepPaused} awaiting the in-flight reload
  function seekProgressiveTo(targetAbs, keepPaused) {
    if (keepPaused === undefined) keepPaused = video.paused;
    targetAbs = clampSeek(targetAbs);
    if (reloading) { pendingSeek = { target: targetAbs, keepPaused: keepPaused }; return; }
    reloading = true;
    loadFromSession(currentAud, currentSub, targetAbs, false, keepPaused).finally(() => {
      reloading = false;
      const p = pendingSeek;
      pendingSeek = null;
      // Only re-fire for a target that actually moved — otherwise a seek that
      // merely coincided with its own reload would loop.
      if (p && Math.abs(p.target - targetAbs) > 0.5) seekProgressiveTo(p.target, p.keepPaused);
    });
  }

  // ±10s arrow presses are a BURST source (a remote's key-repeat fires every few
  // tens of ms), and on a progressive stream each one is a full server round-trip
  // and pipeline rebuild. So they accumulate against a virtual target that the
  // scrubber renders immediately, and commit once the presses stop — the same
  // preview-then-commit idiom the scrubber drag and the FF/RW scan already use.
  // Deliberate single seeks (drag-release, skip-intro, scan commit) stay instant.
  const ARROW_SEEK_DEBOUNCE_MS = 250;
  let arrowTarget = null, arrowTimer = null, arrowPaused = false;
  // The position the UI should show: a pending arrow target outranks the element,
  // so the scrubber tracks the presses instead of lagging a reload behind them.
  const displayAbsTime = () => (arrowTarget != null ? arrowTarget : currentAbsTime());
  function flushArrowSeek() {
    arrowTimer = null;
    if (arrowTarget == null) return;
    const t = arrowTarget;
    arrowTarget = null;
    seekProgressiveTo(t, arrowPaused);
  }
  const seekBy = (delta) => {
    if (isProgressive) {
      if (arrowTarget == null) arrowPaused = video.paused; // capture intent on the FIRST press
      arrowTarget = clampSeek((arrowTarget != null ? arrowTarget : currentAbsTime()) + delta);
      updateScrubFromPlayback(); // hoisted; paints the virtual target at once
      if (arrowTimer) clearTimeout(arrowTimer);
      arrowTimer = setTimeout(flushArrowSeek, ARROW_SEEK_DEBOUNCE_MS);
      return;
    }
    let t = video.currentTime + delta;
    if (t < 0) t = 0;
    if (Number.isFinite(video.duration) && video.duration > 0 && t > video.duration) t = video.duration;
    video.currentTime = t;
  };

  // Play (button or video-frame click) is also the scan-mode exit: it commits
  // the scanned-to position as the one real seek and resumes playback.
  const togglePlay = () => {
    if (endScan(true)) { video.play().catch(() => {}); return; }
    if (video.paused) video.play().catch(() => {}); else video.pause();
  };
  if (rewindBtn) rewindBtn.addEventListener('click', () => scanPress(-1));
  if (forwardBtn) forwardBtn.addEventListener('click', () => scanPress(1));
  if (toggleBtn) toggleBtn.addEventListener('click', togglePlay);

  // Reload: re-fetch the session and replay at the current position — recovers a
  // wedged/desynced stream in place (same path a progressive seek uses).
  const reloadBtn = document.getElementById('tvReloadBtn');
  if (reloadBtn) reloadBtn.addEventListener('click', () => loadFromSession(currentAud, currentSub, currentAbsTime()));

  // Prev/next file (TV episodes, photo clips): shown when the page supplied an
  // adjacent file id (hidden on movies and at boundaries), navigating there.
  // playerCtx (photo pages) carries the launch filters so stepping stays
  // within the list the clip was launched from.
  const playerCtx = video.dataset.playerCtx || '';
  // Explicit episode/clip advance (prev/next, Up Next, media keys) starts the
  // target at the BEGINNING — begin=1 tells the fresh boot to pass seekTo=0
  // instead of resuming. Opening a title from Continue Watching / the season page
  // is a plain <a> without this flag, so it still resumes where you left off.
  const gotoFile = (id) => {
    window.location.href = '/' + video.dataset.mediaKind + '/player?file=' + id + (playerCtx ? '&' + playerCtx : '') + '&begin=1';
  };
  const prevEpBtn = document.getElementById('tvPrevEpBtn');
  const nextEpBtn = document.getElementById('tvNextEpBtn');

  // |< is the universal transport idiom, not a bare "previous episode": a press past
  // PREV_RESTART_SECS (or on a file with no predecessor — every movie, a season's first
  // episode, an extra, the first clip of a list) RESTARTS the current file; a press
  // inside that window steps back. No double-press timer is needed — a restart puts the
  // playhead at 0, so the NEXT press falls into the step-back arm on its own. The
  // playhead IS the state. player.js's playPrev is the same shape, to the second.
  const PREV_RESTART_SECS = 10; // press |< later than this ⇒ restart rather than step back
  const restartCurrent = () => {
    // A scan press paused the video and is running a VIRTUAL playhead; abandon it rather
    // than seek from it (endScan(false) is the drag/Back path), and resume playback the
    // way that path does. A finished file is likewise paused — restarting it means watch
    // it again, so play. Otherwise leave paused-ness alone (a native seek never resumes).
    const resume = endScan(false) || video.ended;
    hideUpNext(); // the progressive path swaps the source and fires no `seeking`, so the
                  // card's 8s countdown would survive the restart and advance us anyway.
    resetSkip();  // the progressive arm gets this via loadFromSession; do it for both, so
                  // auto-skip re-arms identically on a restart whatever the playback path.
    seekToAbs(0, resume ? false : undefined);
    if (resume) video.play().catch(() => {});
  };
  const goPrev = () => {
    if (prevFile > 0 && currentAbsTime() <= PREV_RESTART_SECS) { gotoFile(prevFile); return; }
    restartCurrent();
  };
  // Bound UNCONDITIONALLY: the button is always live, because restart is always available.
  if (prevEpBtn) prevEpBtn.addEventListener('click', goPrev);
  if (nextEpBtn && nextFile > 0) { nextEpBtn.hidden = false; nextEpBtn.addEventListener('click', () => gotoFile(nextFile)); }
  // The shared transport ships episode wording; a photos-library clip isn't one.
  if (video.dataset.mediaKind === 'photo') {
    if (prevEpBtn) { prevEpBtn.title = 'Previous clip or restart'; prevEpBtn.setAttribute('aria-label', 'Previous clip or restart'); }
    if (nextEpBtn) { nextEpBtn.title = 'Next clip'; nextEpBtn.setAttribute('aria-label', 'Next clip'); }
  }

  // Hardware media keys (Flirc/BT remote): Chrome routes them to the Media
  // Session API, whose page-global handlers player.js owns. While this player
  // is active the bridge receives the dispatched actions so the remote drives
  // THE VIDEO — play/pause = the toggle path (play commits an in-progress
  // scan), FF/RW = the DVR scan, prev = restart-or-previous, next = the adjacent
  // episode — exactly the on-screen buttons. Returns true for every media action
  // (a remote press on a video page must never fall through and skip a music
  // track); cleared on turbo:before-cache so music control returns to player.js.
  window.hesperaMediaControl = (action) => {
    switch (action) {
      case 'play': case 'pause': case 'playpause': togglePlay(); return true;
      case 'seekforward': scanPress(1); return true;
      case 'seekbackward': scanPress(-1); return true;
      case 'nexttrack': if (nextFile > 0) gotoFile(nextFile); return true;
      case 'previoustrack': goPrev(); return true;
    }
    return false;
  };

  // Click the video frame to play/pause (standard player UX). The listener is on
  // the <video> itself, so it fires only on direct video clicks — the overlay
  // controls and the floating skip button are separate elements that never reach
  // it (and .media-captions is pointer-events:none, so a click through a caption
  // still toggles). When the controls are auto-hidden the overlay is
  // pointer-events:none, so the whole frame toggles; while visible its bottom strip
  // intercepts (the play button is right there). A click also bumps the controls
  // visible via the wrap's pointerdown listener.
  video.addEventListener('click', togglePlay);
  // playbackState drives Chrome's play-vs-pause translation of the single
  // hardware play/pause key — without it the (paused) music engine's state
  // decides, and the key misfires on video pages.
  video.addEventListener('play', () => {
    if (transport) transport.classList.add('playing');
    if ('mediaSession' in navigator) navigator.mediaSession.playbackState = 'playing';
  });
  video.addEventListener('pause', () => {
    if (transport) transport.classList.remove('playing');
    if ('mediaSession' in navigator) navigator.mediaSession.playbackState = 'paused';
  });

  // --- skip segments (intro / recap / commercial) --- ranges come from the
  //     playback session (embedded chapters + an EDL sidecar). A "Skip …" button
  //     shows while inside a segment; the #skipAutoBtn toggle (localStorage,
  //     per-device) makes it automatic — auto-skipping each segment once, so a
  //     deliberate manual rewind back into it isn't fought. Skipping reuses the
  //     seekable/progressive seek and runs on the absolute episode timeline.
  const skipBtn = (() => {
    const w = video.closest('.tv-player-video-wrap');
    if (!w) return null;
    let el = w.querySelector('.media-skip-btn');
    if (!el) { el = document.createElement('button'); el.type = 'button'; el.className = 'media-skip-btn'; el.hidden = true; w.appendChild(el); }
    return el;
  })();
  const skipAutoBtn = document.getElementById('skipAutoBtn');
  const skippedAuto = new Set(); // segment ids already auto-skipped this session
  let activeSkip = null;
  let skipAuto = false;
  try { skipAuto = localStorage.getItem('skip_auto') === '1'; } catch (e) {}

  // keepPaused defaults to "leave the player however the user left it" — a native
  // currentTime set never resumes, and neither should a progressive reload. The FF/RW
  // scan commit is the one caller that must override it to false: the scan PAUSES the
  // video and relies on the seek to resume, so preserving `paused` there would leave
  // the player frozen at the scanned-to frame.
  const seekToAbs = (target, keepPaused) => {
    if (isProgressive) { seekProgressiveTo(target, keepPaused); return; }
    try { video.currentTime = Math.max(0, target); } catch (e) {}
  };
  const segId = (s) => s.start + ':' + s.end;
  const skipLabel = (k) => (k === 'commercial' ? 'Skip commercial' : k === 'recap' ? 'Skip recap' : 'Skip intro');
  function segmentAt(t) { for (const s of skipSegments) { if (t >= s.start && t < s.end) return s; } return null; }
  function reflectSkipAuto() { if (skipAutoBtn) { skipAutoBtn.classList.toggle('is-on', skipAuto); skipAutoBtn.setAttribute('aria-pressed', skipAuto ? 'true' : 'false'); } }
  function resetSkip() {
    skippedAuto.clear();
    activeSkip = null;
    endAdMute(true); // a session reload mid-ad must not strand the player muted
    adMuteOverridden.clear();
    if (skipBtn) skipBtn.hidden = true;
    if (skipAutoBtn) skipAutoBtn.hidden = skipSegments.length === 0;
    if (muteAdsBtn) muteAdsBtn.hidden = !skipSegments.some((s) => s.kind === 'commercial');
  }
  function doSkip(seg) { seekToAbs(seg.end); if (skipBtn) skipBtn.hidden = true; activeSkip = null; }
  function updateSkip() {
    if (!skipBtn) return;
    const seg = segmentAt(currentAbsTime());
    if (!seg) { if (!skipBtn.hidden) skipBtn.hidden = true; activeSkip = null; return; }
    if (skipAuto && !skippedAuto.has(segId(seg))) { skippedAuto.add(segId(seg)); doSkip(seg); return; }
    if (activeSkip !== seg) { activeSkip = seg; skipBtn.textContent = skipLabel(seg.kind); skipBtn.hidden = false; }
  }
  if (skipBtn) skipBtn.addEventListener('click', () => { const seg = segmentAt(currentAbsTime()); if (seg) doSkip(seg); });
  if (skipAutoBtn) skipAutoBtn.addEventListener('click', () => { skipAuto = !skipAuto; try { localStorage.setItem('skip_auto', skipAuto ? '1' : '0'); } catch (e) {} reflectSkipAuto(); updateSkip(); });
  reflectSkipAuto();
  video.addEventListener('timeupdate', updateSkip);

  // --- mute ads --- the gentler sibling of Auto-skip: with the #muteAdsBtn
  //     toggle on (localStorage, per-device, revealed only when the file has
  //     commercial segments), audio mutes while playback is inside a commercial
  //     and restores on exit. The mute is applied to the element only — the
  //     persisted tv_muted/tv_volume preference is never written by this path,
  //     so a transient ad-mute can't corrupt the user's saved state (the mute
  //     glyph/slider follow via volumechange → reflectVolume). A user who
  //     unmutes mid-ad wins: that segment is marked overridden and never
  //     fought — no re-mute, and no "restore" unmute it didn't ask for. A user
  //     muted before the ad is left alone entirely (nothing to restore).
  const muteAdsBtn = document.getElementById('muteAdsBtn');
  const adMuteOverridden = new Set(); // segment ids the user unmuted inside
  let adMuteActive = false; // we muted; exit restores, an override just forgets
  let muteAds = false;
  try { muteAds = localStorage.getItem('mute_ads') === '1'; } catch (e) {}
  function reflectMuteAds() { if (muteAdsBtn) { muteAdsBtn.classList.toggle('is-on', muteAds); muteAdsBtn.setAttribute('aria-pressed', muteAds ? 'true' : 'false'); } }
  function endAdMute(restore) {
    if (!adMuteActive) return;
    adMuteActive = false;
    if (restore) video.muted = false;
  }
  function updateAdMute() {
    const seg = segmentAt(currentAbsTime());
    if (!seg || seg.kind !== 'commercial') { endAdMute(true); return; }
    if (adMuteActive && !video.muted) { // user unmuted mid-ad — their call, don't fight
      adMuteActive = false;
      adMuteOverridden.add(segId(seg));
      return;
    }
    if (muteAds && !adMuteActive && !video.muted && !adMuteOverridden.has(segId(seg))) {
      adMuteActive = true;
      video.muted = true;
    }
  }
  if (muteAdsBtn) muteAdsBtn.addEventListener('click', () => {
    muteAds = !muteAds;
    try { localStorage.setItem('mute_ads', muteAds ? '1' : '0'); } catch (e) {}
    reflectMuteAds();
    if (muteAds) updateAdMute(); else endAdMute(true); // mid-ad toggle acts immediately
  });
  reflectMuteAds();
  video.addEventListener('timeupdate', updateAdMute);

  // --- custom seek bar (B2) --- native <video controls> is dropped (its scrubber
  //     can't seek the progressive remux/burn-in streams — video.seekable==[0,0]),
  //     so this is the only scrubber. A click or drag-release seeks: a native
  //     currentTime seek on the byte/segment-seekable paths, a single ?start=
  //     reload on the progressive ones. The reload fires only on release (not per
  //     drag-move), so dragging never thrashes reloads.
  const scrubber = document.getElementById('mediaScrubber');
  const scrubFill = document.getElementById('mediaScrubberFill');
  const scrubThumb = document.getElementById('mediaScrubberThumb');
  const curLabel = document.getElementById('mediaCur');
  const durLabel = document.getElementById('mediaDur');
  let dragging = false, dragFrac = 0;

  function fmtTime(s) {
    s = Math.max(0, Math.floor(s || 0));
    const h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60), sec = s % 60;
    const mm = h > 0 ? String(m).padStart(2, '0') : String(m);
    return (h > 0 ? h + ':' : '') + mm + ':' + String(sec).padStart(2, '0');
  }
  function renderScrub(frac, curSec) {
    frac = Math.min(1, Math.max(0, frac || 0));
    if (scrubFill) scrubFill.style.width = (frac * 100) + '%';
    if (scrubThumb) scrubThumb.style.left = (frac * 100) + '%';
    if (scrubber) scrubber.setAttribute('aria-valuenow', Math.round(frac * 100));
    if (curLabel) curLabel.textContent = fmtTime(curSec);
    if (durLabel) durLabel.textContent = fmtTime(sessionDuration);
  }
  function updateScrubFromPlayback() {
    if (dragging || scanActive()) return; // the drag / the scan ticker owns the bar
    const dur = sessionDuration || video.duration || 0;
    // displayAbsTime, not currentAbsTime: a pending ±10s arrow target outranks the
    // element, so the bar tracks the presses instead of lagging the reload behind them.
    const t = displayAbsTime();
    renderScrub(dur > 0 ? t / dur : 0, t);
  }
  video.addEventListener('timeupdate', updateScrubFromPlayback);
  // timeupdate (~4×/s) is the portable floor for the caption render — it keeps cues
  // locked to currentTime everywhere (and is the sole driver on browsers without
  // requestVideoFrameCallback), and it covers paused-seek (seeked fires timeupdate).
  // The rVFC loop (addCaptionTrack) sharpens boundaries to ~1 frame where supported.
  // renderActiveCues no-ops when the active set is unchanged, so the overlap is free.
  video.addEventListener('timeupdate', renderActiveCues);
  video.addEventListener('loadedmetadata', updateScrubFromPlayback);

  function fracFromEvent(e) {
    if (!scrubber) return 0;
    const rect = scrubber.getBoundingClientRect();
    const x = (e.clientX || 0) - rect.left;
    return rect.width > 0 ? Math.min(1, Math.max(0, x / rect.width)) : 0;
  }
  function commitSeek(frac) {
    const dur = sessionDuration || video.duration || 0;
    if (dur <= 0) return;
    const target = frac * dur;
    if (isProgressive) { seekProgressiveTo(target); return; }
    try { video.currentTime = target; } catch (e) {}
  }
  if (scrubber) {
    scrubber.addEventListener('pointerdown', (e) => {
      endScan(false); // a drag takes over from a scan; its release sets the position
      dragging = true;
      try { scrubber.setPointerCapture(e.pointerId); } catch (e2) {}
      dragFrac = fracFromEvent(e);
      renderScrub(dragFrac, dragFrac * (sessionDuration || 0));
    });
    scrubber.addEventListener('pointermove', (e) => {
      if (!dragging) return;
      dragFrac = fracFromEvent(e);
      renderScrub(dragFrac, dragFrac * (sessionDuration || 0));
    });
    const endDrag = (e) => {
      if (!dragging) return;
      dragging = false;
      try { scrubber.releasePointerCapture(e.pointerId); } catch (e2) {}
      commitSeek(dragFrac);
    };
    scrubber.addEventListener('pointerup', endDrag);
    scrubber.addEventListener('pointercancel', endDrag);
    // Couch/remote: Left/Right on the focused bar nudge ±10s (reuses seekBy).
    // stopPropagation so couch.js's directional-focus nav doesn't ALSO move focus
    // off the bar on the same press — Up/Down still bubble, so the user can leave.
    // --- seek-bar tick marks + hover preview (trickplay) ---
  // Ticks: neutral marks for every chapter, accent marks spanning skip
  // segments — painted into a pointer-events:none layer inside the scrubber,
  // re-rendered when the session (or duration) changes. Preview: a floating
  // frame above the bar on hover/drag, background-position math over the
  // sprite sheets; the manifest is fetched lazily on first hover and a 404
  // silently disables previews.
  const marksLayer = (() => {
    if (!scrubber) return null;
    const el = document.createElement('div');
    el.className = 'media-scrub-marks';
    scrubber.appendChild(el);
    return el;
  })();
  renderScrubMarks = function () {
    if (!marksLayer) return;
    marksLayer.textContent = '';
    const dur = sessionDuration;
    if (!(dur > 0)) return;
    for (const c of chapterList) {
      if (!(c.start > 0) || c.start >= dur) continue; // a tick at 0 is noise
      const t = document.createElement('div');
      t.className = 'media-scrub-tick';
      t.style.left = (c.start / dur * 100) + '%';
      if (c.title) t.title = c.title;
      marksLayer.appendChild(t);
    }
    for (const seg of skipSegments) {
      const m = document.createElement('div');
      m.className = 'media-scrub-seg media-scrub-seg-' + (seg.kind || 'intro');
      m.style.left = (seg.start / dur * 100) + '%';
      m.style.width = (Math.max(0, seg.end - seg.start) / dur * 100) + '%';
      m.title = seg.kind || '';
      marksLayer.appendChild(m);
    }
  };
  video.addEventListener('durationchange', () => renderScrubMarks());

  let tpManifest = null, tpState = 'idle'; // idle | loading | ready | absent
  const tpBase = () => '/stream/' + video.dataset.mediaKind + '-trickplay/' + fileID + '/';
  const preview = (() => {
    if (!scrubber) return null;
    const el = document.createElement('div');
    el.className = 'media-scrub-preview';
    el.hidden = true;
    const img = document.createElement('div');
    img.className = 'media-scrub-preview-frame';
    const label = document.createElement('div');
    label.className = 'media-scrub-preview-time';
    el.appendChild(img);
    el.appendChild(label);
    // The FF/RW scan indicator (#mediaScanPill, server-rendered in the scrubber
    // for its inline SVG icons) joins the preview cluster, so during a scan the
    // direction + speed ride the playhead with the frame + timestamp — the
    // Plex/Roku trick-mode look — instead of floating as a static popup.
    const pill = document.getElementById('mediaScanPill');
    if (pill) el.appendChild(pill);
    scrubber.appendChild(el);
    return { el, img, label };
  })();
  function loadTPManifest() {
    if (tpState !== 'idle') return;
    tpState = 'loading';
    fetch(tpBase() + 'manifest.json')
      .then((res) => { if (!res.ok) throw new Error('absent'); return res.json(); })
      .then((m) => { tpManifest = m; tpState = 'ready'; })
      .catch(() => { tpState = 'absent'; });
  }
  function showPreview(frac) {
    if (!preview) return;
    const dur = sessionDuration;
    if (!(dur > 0)) return;
    const t = frac * dur;
    preview.label.textContent = fmtTime(t);
    if (tpState === 'ready' && tpManifest) {
      const m = tpManifest;
      let idx = Math.floor(t / m.interval_sec);
      if (idx >= m.frames) idx = m.frames - 1;
      const per = m.tile * m.tile;
      const sheet = Math.floor(idx / per), cell = idx % per;
      const col = cell % m.tile, row = Math.floor(cell / m.tile);
      preview.img.style.width = m.width + 'px';
      preview.img.style.height = m.height + 'px';
      preview.img.style.backgroundImage = "url('" + tpBase() + 'sprite' + String(sheet).padStart(5, '0') + ".jpg')";
      preview.img.style.backgroundPosition = (-col * m.width) + 'px ' + (-row * m.height) + 'px';
      preview.img.hidden = false;
    } else {
      preview.img.hidden = true; // timestamp-only until (unless) sprites exist
    }
    const frameW = (tpState === 'ready' && tpManifest) ? tpManifest.width : 60;
    const barW = scrubber.getBoundingClientRect().width || 1;
    let leftPx = frac * barW - frameW / 2;
    leftPx = Math.max(0, Math.min(barW - frameW, leftPx));
    preview.el.style.left = leftPx + 'px';
    preview.el.hidden = false;
  }
  function hidePreview() { if (preview) preview.el.hidden = true; }
  scanShowPreview = showPreview; scanHidePreview = hidePreview; scanLoadPreview = loadTPManifest;
  scrubber.addEventListener('pointerenter', loadTPManifest);
  scrubber.addEventListener('pointermove', (e) => {
    if (e.pointerType === 'touch' && !dragging) return;
    showPreview(fracFromEvent(e));
  });
  scrubber.addEventListener('pointerleave', () => { if (!dragging) hidePreview(); });
  scrubber.addEventListener('pointerup', hidePreview);
  scrubber.addEventListener('pointercancel', hidePreview);

  // Arrow seeking only while engaged (couch.js's [data-couch-capture] protocol:
  // Enter captures, Enter/Back/blur release) — an unengaged scrubber stays
  // transparent so the remote's arrows move focus past it.
  scrubber.addEventListener('keydown', (e) => {
      if (!scrubber.hasAttribute('data-couch-engaged')) return;
      if (e.key === 'ArrowRight') { e.preventDefault(); e.stopPropagation(); seekBy(10); }
      else if (e.key === 'ArrowLeft') { e.preventDefault(); e.stopPropagation(); seekBy(-10); }
    });
  }

  // --- FF/RW scan (DVR-style) --- the rewind/fast-forward buttons drive a
  //     *virtual* playhead, not the video: the first press pauses playback in
  //     place and scans at 2× (timeline seconds per real second); repeat
  //     presses cycle 8× → 32× → 2× (Roku's few-but-fast trick-mode tiers);
  //     the opposite direction restarts at 2×. The scrubber fill and the
  //     trickplay preview track the scan position, with the direction + speed
  //     indicator riding the playhead inside the preview cluster.
  //     Play (button or video-frame click) commits the single real seek — the
  //     drag-release pattern, timer-driven — so a scan costs the server nothing
  //     until it lands and works identically on the seekable and progressive
  //     paths (browsers have no reverse playback, and a per-step progressive
  //     seek would reload the stream every step; the virtual playhead sidesteps
  //     both). The overlay stays pinned throughout because the video is paused.
  const SCAN_SPEEDS = [2, 8, 32]; // timeline seconds per real second, per press
  const scanPill = document.getElementById('mediaScanPill');
  let scanDir = 0, scanTier = 0, scanPos = 0, scanTimer = null, scanLast = 0;
  function scanActive() { return scanDir !== 0; }
  function renderScanPill() {
    if (!scanPill) return;
    const rw = scanPill.querySelector('.media-scan-rw');
    const ff = scanPill.querySelector('.media-scan-ff');
    const speed = scanPill.querySelector('.media-scan-speed');
    if (rw) rw.hidden = scanDir >= 0;
    if (ff) ff.hidden = scanDir <= 0;
    if (speed) speed.textContent = SCAN_SPEEDS[scanTier] + '×';
    scanPill.hidden = !scanActive();
  }
  function scanTick() {
    const now = performance.now();
    const dt = (now - scanLast) / 1000;
    scanLast = now;
    const dur = sessionDuration || video.duration || 0;
    scanPos = Math.max(0, scanPos + scanDir * SCAN_SPEEDS[scanTier] * dt);
    if (dur > 1 && scanPos > dur - 1) scanPos = dur - 1; // pin at the edges; play commits
    const frac = dur > 0 ? scanPos / dur : 0;
    renderScrub(frac, scanPos);
    scanShowPreview(frac);
  }
  function scanPress(dir) {
    if (!scanActive()) {
      scanPos = currentAbsTime();
      scanDir = dir; scanTier = 0;
      video.pause();
      scanLoadPreview();
      scanLast = performance.now();
      scanTimer = setInterval(scanTick, 200);
      scanTick(); // dt≈0: no movement, but the preview cluster appears at the playhead immediately
    } else if (scanDir === dir) {
      scanTier = (scanTier + 1) % SCAN_SPEEDS.length; // 2× → 8× → 32× → 2×
    } else {
      scanDir = dir; scanTier = 0; // opposite direction restarts at 2×
    }
    renderScanPill();
  }
  // endScan leaves scan mode; commit=true seeks to the scanned-to position (the
  // play path), commit=false abandons it (a drag or teardown takes over).
  function endScan(commit) {
    if (!scanActive()) return false;
    clearInterval(scanTimer); scanTimer = null;
    scanDir = 0; scanTier = 0;
    renderScanPill();
    scanHidePreview();
    // keepPaused=false: the scan paused the video, and committing it means "play from
    // here" (play is literally the button that commits a scan).
    if (commit) seekToAbs(scanPos, false);
    return true;
  }
  document.addEventListener('turbo:before-cache', () => endScan(false), { once: true });
  // Escape / a remote's Back cancels an in-progress scan: abandon the virtual
  // playhead and resume where playback paused — the video never moved, so no
  // seek is issued (the same endScan(false) a scrubber drag uses). Capture
  // phase so the cancel wins over couch.js's document-level Back handling,
  // which registered first and would otherwise navigate Home mid-scan.
  // Escape while fullscreen is left untouched: the browser's native
  // exit-fullscreen outranks the cancel (matching couch.js's fullscreen yield).
  const BACK_KEYS = new Set(['Backspace', 'Escape', 'BrowserBack', 'GoBack']);
  const scanCancelKey = (e) => {
    if (!scanActive() || !BACK_KEYS.has(e.key)) return;
    if (e.key === 'Escape' && document.fullscreenElement) return;
    e.preventDefault();
    e.stopPropagation();
    endScan(false);
    video.play().catch(() => {});
  };
  document.addEventListener('keydown', scanCancelKey, true);
  document.addEventListener('turbo:before-cache', () => {
    document.removeEventListener('keydown', scanCancelKey, true);
  }, { once: true });

  // Fullscreen is app-root fullscreen everywhere: #tvFullscreenBtn carries
  // data-app-fullscreen, so the layout shell's delegated handler owns it (one
  // fullscreen state that survives Turbo body swaps like Up Next auto-advance);
  // CSS expands the video wrap to fill the viewport while html.is-fullscreen.

  // --- volume + mute --- native <video controls> is dropped, so this restores
  //     the volume slider it used to provide. Persisted in localStorage. Works
  //     even with the dynamic-range graph active: the MediaElementAudioSource taps
  //     the element AFTER its volume/muted are applied.
  const volSlider = document.getElementById('tvVolume');
  const muteBtn = document.getElementById('tvMuteBtn');
  let savedVol = 1;
  try { const v = parseFloat(localStorage.getItem('tv_volume')); if (!isNaN(v)) savedVol = Math.min(1, Math.max(0, v)); } catch (e) {}
  let savedMuted = false;
  try { savedMuted = localStorage.getItem('tv_muted') === '1'; } catch (e) {}
  video.volume = savedVol;
  video.muted = savedMuted;

  function reflectVolume() {
    const muted = video.muted || video.volume === 0;
    if (volSlider) volSlider.value = muted ? 0 : video.volume;
    if (muteBtn) {
      const vg = muteBtn.querySelector('.tv-glyph-vol');
      const mg = muteBtn.querySelector('.tv-glyph-mute');
      if (vg) vg.hidden = muted;
      if (mg) mg.hidden = !muted;
      muteBtn.setAttribute('aria-pressed', muted ? 'true' : 'false');
    }
  }
  if (volSlider) {
    volSlider.value = savedMuted ? 0 : savedVol;
    volSlider.addEventListener('input', () => {
      const v = parseFloat(volSlider.value);
      video.volume = v;
      video.muted = v === 0;
      savedVol = v > 0 ? v : savedVol;
      try { localStorage.setItem('tv_volume', String(v)); localStorage.setItem('tv_muted', video.muted ? '1' : '0'); } catch (e) {}
    });
    // Arrow handling is couch.js's engage protocol: an unengaged range input is
    // transparent (arrows move focus past it); Enter captures it, then the
    // native range arrows adjust the volume.
  }
  if (muteBtn) {
    muteBtn.addEventListener('click', () => {
      video.muted = !video.muted;
      if (!video.muted && video.volume === 0) { video.volume = savedVol > 0 ? savedVol : 0.5; }
      try { localStorage.setItem('tv_muted', video.muted ? '1' : '0'); } catch (e) {}
    });
  }
  video.addEventListener('volumechange', reflectVolume);
  reflectVolume();

  // --- controls overlay auto-hide --- the controls overlay the video and fade
  //     out after a few seconds of inactivity, so they stay reachable in
  //     fullscreen without exiting. Hiding keys off pointer *movement* going
  //     idle, not pointer presence/position — so a mouse/trackpad resting over
  //     the controls (or jittering in place, common on a couch/TV setup) still
  //     fades, matching how YouTube/Netflix/VLC behave. Real movement re-shows.
  //     Never hide while paused, while a control is keyboard-focused (couch/
  //     remote arrow-nav, via :focus-visible), or mid scrubber-drag.
  const wrap = video.closest('.tv-player-video-wrap');
  const overlay = document.getElementById('mediaOverlay');
  if (wrap && overlay) {
    const HIDE_MS = 2500;
    const JITTER_PX = 6; // sub-threshold pointermove = sensor noise, not activity
    let hideTimer = null, lastX = null, lastY = null;
    const showControls = () => wrap.classList.remove('controls-hidden');
    const hideControls = () => {
      // Keyboard/remote focus (couch arrow-nav) sets :focus-visible — don't yank
      // controls out from under the control being arrowed through. Mouse-click
      // focus is :focus only (not :focus-visible), so it doesn't pin here.
      // The play/pause button is the one exemption on BOTH rules below: boot
      // focuses it (see the end of init), and it must neither pin the overlay
      // open forever nor lose focus on hide — an invisibly-focused toggle
      // firing on Enter/Space is a deliberate pause (the TV-app idiom), unlike
      // the stray-button re-fire the blur guard exists for.
      const ae = document.activeElement;
      if (video.paused || dragging ||
        (overlay.contains(ae) && ae !== toggleBtn && ae.matches(':focus-visible'))) return;
      // Reaching here, a focused overlay control is mouse-focused (not
      // :focus-visible). The hidden overlay is opacity:0 + pointer-events:none —
      // which blocks the pointer but NOT keyboard activation, so a button the
      // mouse last clicked would re-fire on Space/Enter while invisible. Drop its
      // focus before hiding. <button> only: never a <select> (blur would close an
      // open popup; a mouse-picked select blurs itself on change — blurIfMouse) and
      // not the scrubber/volume (their arrow behavior is harmless). A REMOTE user's
      // focused select is :focus-visible and pins the overlay open above, like any
      // other keyboard-focused control. Blur before the class-add so the
      // synchronous focusout→bump re-show is immediately overridden — overlay still
      // ends hidden.
      if (overlay.contains(ae) && ae !== toggleBtn && ae.tagName === 'BUTTON') ae.blur();
      wrap.classList.add('controls-hidden');
    };
    const bump = () => {
      showControls();
      clearTimeout(hideTimer);
      hideTimer = setTimeout(hideControls, HIDE_MS);
    };
    wrap.addEventListener('pointermove', (e) => {
      // Only count real movement past the jitter threshold so a resting/noisy
      // pointer doesn't keep re-arming the timer. Compare to the last committed
      // position (not the previous event) so steady noise never accumulates.
      if (lastX !== null && Math.abs(e.clientX - lastX) <= JITTER_PX && Math.abs(e.clientY - lastY) <= JITTER_PX) return;
      lastX = e.clientX; lastY = e.clientY;
      bump();
    });
    ['pointerdown', 'keydown'].forEach((ev) => wrap.addEventListener(ev, bump));
    overlay.addEventListener('focusin', showControls);
    overlay.addEventListener('focusout', bump);
    video.addEventListener('pause', showControls);  // paused → pin controls up
    video.addEventListener('play', bump);
    document.addEventListener('turbo:before-cache', () => clearTimeout(hideTimer), { once: true });
    bump(); // start visible, then fade
  }

  // --- "Even loudness": client-side dynamic-range compression via Web Audio.
  //     Compresses the decoded audio in the browser, so it evens the loud
  //     music/quiet dialogue gap on every playback path (direct/remux/HLS) with
  //     no server transcode and full seeking preserved. Routing engages only
  //     once the AudioContext is actually running, so a context that starts
  //     suspended (autoplay policy) never silences playback. Preference persists
  //     in localStorage, mirroring the theme toggle. ---
  const boostBtn = document.getElementById('tvBoostBtn');
  // Default ON — quiet dialogue under loud music/SFX is the common pain point, so
  // it's on unless the user has explicitly turned it off.
  let boostOn = true;
  try { boostOn = localStorage.getItem('tv_boost') !== '0'; } catch (e) {}
  let audioCtx = null, srcNode = null, compressor = null, makeup = null, graphBuilt = false;

  function buildAudioGraph() {
    if (graphBuilt) return true;
    const AC = window.AudioContext || window.webkitAudioContext;
    if (!AC) return false;
    try {
      audioCtx = new AC();
      srcNode = audioCtx.createMediaElementSource(video); // once per element
      compressor = audioCtx.createDynamicsCompressor();
      compressor.threshold.value = -24;
      compressor.knee.value = 30;
      compressor.ratio.value = 4;
      compressor.attack.value = 0.003;
      compressor.release.value = 0.25;
      makeup = audioCtx.createGain();
      makeup.gain.value = 2.0; // ~ +6 dB, restoring loudness shaved by compression
      audioCtx.addEventListener('statechange', routeBoost);
    } catch (e) { return false; }
    graphBuilt = true;
    return true;
  }

  function routeBoost() {
    if (!graphBuilt) return;
    try { srcNode.disconnect(); compressor.disconnect(); makeup.disconnect(); } catch (e) {}
    if (boostOn && audioCtx.state === 'running') {
      srcNode.connect(compressor); compressor.connect(makeup); makeup.connect(audioCtx.destination);
    } else {
      srcNode.connect(audioCtx.destination); // off, or context not yet running: pass through
    }
  }

  function applyBoost() {
    if (!boostOn) { if (graphBuilt) routeBoost(); return; }
    if (!buildAudioGraph()) return;
    audioCtx.resume().catch(() => {});
    routeBoost();
  }

  function reflectBoost() {
    if (!boostBtn) return;
    // aria-pressed drives the solid-accent "on" style (#tvBoostBtn[aria-pressed]
    // in app.css) — clearer than .btn-primary, which is near-invisible in light
    // mode. Keep the title in sync so the state is obvious on hover too.
    boostBtn.classList.toggle('is-on', boostOn);
    boostBtn.setAttribute('aria-pressed', boostOn ? 'true' : 'false');
    boostBtn.setAttribute('title', boostOn ? 'Dialogue boost: On' : 'Dialogue boost: Off');
  }

  if (boostBtn) {
    reflectBoost();
    boostBtn.addEventListener('click', () => {
      boostOn = !boostOn;
      try { localStorage.setItem('tv_boost', boostOn ? '1' : '0'); } catch (e) {}
      reflectBoost();
      applyBoost();
    });
    // A persisted-on boost (or a context that started suspended) engages on the
    // first real gesture / when playback starts, without needing a click.
    video.addEventListener('play', () => { if (boostOn && graphBuilt) audioCtx.resume().then(routeBoost).catch(() => {}); });
    if (boostOn) applyBoost();
  }

  // --- progress reporting --- positions are on the real episode timeline
  // (currentAbsTime), so a resumed remux/burn-in stream (rebased to zero) still
  // saves the true position. Completion falls back to the session duration because
  // a progressive stream doesn't expose the full length via video.duration.
  function reportProgress(completed) {
    const now = Date.now();
    if (!completed && now - lastReport < 15000) return;
    lastReport = now;
    const body = JSON.stringify({
      file_id: fileID,
      position_seconds: currentAbsTime(),
      duration_seconds: sessionDuration || video.duration || 0,
      completed: !!completed,
    });
    if (navigator.sendBeacon) {
      navigator.sendBeacon(cfg.progressURL, new Blob([body], { type: 'application/json' }));
    } else {
      fetch(cfg.progressURL, { method: 'POST', body, headers: { 'Content-Type': 'application/json' } });
    }
  }
  video.addEventListener('timeupdate', () => {
    reportProgress(false);
    // Report completion exactly once past 90% — timeupdate fires ~4×/s and
    // completion bypasses the 15s throttle, so without this it floods the endpoint
    // for the whole last 10% of the episode.
    const dur = sessionDuration || video.duration || 0;
    if (!completedReported && dur > 0 && currentAbsTime() / dur >= 0.9) {
      completedReported = true;
      reportProgress(true);
    }
  });
  video.addEventListener('pause', () => { lastReport = 0; reportProgress(false); });
  // --- Up Next --- at episode end a cancelable countdown card replaces an
  // instant jump: Play now / a live "in Ns" count / Cancel. data-couch-overlay
  // makes the remote's Back dismiss it (couch.js overlay contract) instead of
  // leaving the page; play/seeking cancel it so rewinding back into the
  // credits isn't fought. Movies have no next file, so it never exists there.
  let upnextTimer = null;
  const upnextCard = (() => {
    if (!(nextFile > 0)) return null;
    const w = video.closest('.tv-player-video-wrap');
    if (!w) return null;
    const el = document.createElement('div');
    el.className = 'media-upnext';
    el.hidden = true;
    el.setAttribute('data-couch-overlay', '');
    el.innerHTML = '<span class="media-upnext-label">Up next in <span id="upnextCount">8</span>s</span>' +
      '<button type="button" class="btn btn-sm btn-primary" id="upnextPlay">Play now</button>' +
      '<button type="button" class="btn btn-sm" id="upnextCancel" data-couch-dismiss>Cancel</button>';
    w.appendChild(el);
    el.querySelector('#upnextPlay').addEventListener('click', () => gotoFile(nextFile));
    el.querySelector('#upnextCancel').addEventListener('click', () => hideUpNext());
    return el;
  })();
  function hideUpNext() {
    if (upnextTimer) { clearInterval(upnextTimer); upnextTimer = null; }
    if (upnextCard) upnextCard.hidden = true;
  }
  function showUpNext() {
    if (!upnextCard || !upnextCard.hidden) return;
    let left = 8;
    upnextCard.querySelector('#upnextCount').textContent = String(left);
    upnextCard.hidden = false;
    upnextCard.querySelector('#upnextPlay').focus();
    upnextTimer = setInterval(() => {
      left--;
      if (left <= 0) { hideUpNext(); gotoFile(nextFile); return; }
      upnextCard.querySelector('#upnextCount').textContent = String(left);
    }, 1000);
  }
  // A progressive stream can lie about ending. It is one long ffmpeg pipe; if that
  // ffmpeg dies mid-episode the handler returns normally, so Go finishes the chunked
  // response cleanly — and the browser, seeing a complete resource, fires 'ended'
  // right there. Verified live by killing the ffmpeg behind a playing episode: the
  // element fired 'ended' ~30s into a 29-minute file, which marked it WATCHED and
  // auto-advanced Up Next into the next episode. (No 'error' is fired — there is
  // nothing else to hook.) So a genuine end is one that happens near the end: short
  // of that, the stream died, and we recover instead of destroying the user's
  // position and yanking them out of the show.
  // Every state the element exposes is a lie at this moment. Measured in a live
  // Chrome, at the instant a truncated progressive stream "ends": currentTime,
  // duration AND buffered.end are ALL snapped to the patched moov duration (1767s)
  // even though only ~12s of video ever arrived. So neither the position nor the
  // buffer can tell a corpse from a finished episode.
  //
  // What cannot be faked is CONTINUITY: playback advances a fraction of a second per
  // tick, and a 1755-second leap in one tick is not playback. So we simply never
  // record such a leap — lastPlayedAbs is the furthest point we actually watched, and
  // a genuine end is one that gets there.
  //
  // The server-side fix (streamProgressive aborts the connection instead of ending it
  // cleanly) is what makes this rare; this is the belt to that pair of braces, and it
  // still covers a clean EOF arriving from anything between us and the browser.
  const ENDED_SLACK_SECS = 15;    // getting this close to the end is a real end
  const PLAYBACK_STEP_MAX = 5;    // seconds one timeupdate can plausibly advance
  video.addEventListener('timeupdate', () => {
    const t = currentAbsTime();
    if (Math.abs(t - lastPlayedAbs) <= PLAYBACK_STEP_MAX) lastPlayedAbs = t; // continuous → real
  });
  video.addEventListener('seeked', () => { lastPlayedAbs = currentAbsTime(); }); // a seek is a legitimate jump
  function endedIsGenuine() {
    if (!isProgressive || !(sessionDuration > 0)) return true; // HLS/direct: trust it
    return lastPlayedAbs >= sessionDuration - ENDED_SLACK_SECS;
  }
  video.addEventListener('ended', () => {
    if (!endedIsGenuine()) {
      const at = lastPlayedAbs; // the furthest point actually watched — not the snapped currentTime
      console.warn('hespera: progressive stream ended early at', at, 'of', sessionDuration, '— stream died, recovering');
      if (stallRetries >= STALL_RETRY_CAP) {
        modeLabel.textContent = 'Playback stopped — reload to continue';
        return;
      }
      stallRetries++;
      recoveredAnchor = at;
      loadFromSession(currentAud, currentSub, at); // re-anchor where it died
      return;
    }
    reportProgress(true);
    if (nextFile > 0) showUpNext();
  });
  video.addEventListener('play', () => hideUpNext());
  video.addEventListener('seeking', () => hideUpNext());
  // Named so the turbo:before-cache teardown can remove it — otherwise a fresh
  // listener (closing over a detached video) accumulates on every player visit.
  const onBeforeUnload = () => { lastReport = 0; reportProgress(false); };
  window.addEventListener('beforeunload', onBeforeUnload);

  // Turbo swaps the page without a full unload: send a final progress report,
  // stop playback, and tear down the hls.js worker before this page is
  // cached/replaced. Pause + clear the source so a direct/remux stream (no hls
  // instance to destroy) can't keep playing audio from a detached element. The
  // floating "resume" chip (tv_resume.js) then links back here. once:true so it
  // doesn't accumulate across repeat visits.
  document.addEventListener('turbo:before-cache', () => {
    window.hesperaMediaControl = null; // media keys go back to the music engine
    // Before anything else: mark the player dead so the stall watchdog can't read a
    // torn-down element (src removed below) as a stalled stream and "recover" it.
    destroyed = true;
    if (stallTimer) { clearInterval(stallTimer); stallTimer = null; }
    if (arrowTimer) { clearTimeout(arrowTimer); arrowTimer = null; }
    lastReport = 0;
    reportProgress(false);
    video.pause();
    teardownHLS();
    hideUpNext();
    if (audioCtx) { try { audioCtx.close(); } catch (e) {} }
    stopRVFC();
    window.removeEventListener('beforeunload', onBeforeUnload);
    video.removeAttribute('src');
    try { video.load(); } catch (e) {}
  }, { once: true });

  // --- OpenSubtitles search dialog (opened by the subtitles dropdown's
  //     leading "Search subtitles…" option, offered whenever a key is configured;
  //     TV only — movie pages don't set data-os-enabled) ---
  const subsModal = document.getElementById('subs-modal');
  const subsStatus = document.getElementById('subs-status');
  const subsResults = document.getElementById('subs-results');
  const subsCloseBtn = document.getElementById('subs-close-btn');

  // The control the dialog was opened from (the subtitles picker), so dismissing
  // it — Close, couch's Back, or picking a result — hands the ring back there
  // instead of dropping focus to <body>, where the next remote arrow would
  // restart from the top of the page and lose the user's place. Same pattern as
  // the "/" search palette (search.js). Remote only: a mouse user wants no ring
  // at all, and focus on a <select> is :focus-visible, which would re-pin the
  // auto-hiding overlay open (the very thing blurIfMouse exists to avoid).
  let subsReturnFocus = null;

  function openSubsModal() {
    if (!subsModal) return;
    subsReturnFocus = document.activeElement;
    subsModal.classList.remove('hidden');
    // Put the ring inside the dialog it just opened (the results arrive async, so
    // Close is the only control there yet) — otherwise it sits on the picker
    // behind the modal until an arrow drags it in.
    if (!usingMouse() && subsCloseBtn) subsCloseBtn.focus();
    runSubsSearch();
  }
  function closeSubsModal() {
    if (subsModal) subsModal.classList.add('hidden');
    // Restore only if it's still in the DOM; focus() on a since-removed element
    // no-ops, same as not restoring.
    if (!usingMouse() && subsReturnFocus && document.contains(subsReturnFocus)) subsReturnFocus.focus();
    subsReturnFocus = null;
  }

  async function runSubsSearch() {
    subsResults.innerHTML = '';
    subsStatus.textContent = 'Searching…';
    let data;
    try {
      const resp = await fetch(`${cfg.subtitleSearchURL}?file=${fileID}&lang=en`, { headers: { Accept: 'application/json' } });
      data = await resp.json();
    } catch (e) { subsStatus.textContent = 'Search failed.'; return; }
    if (!data || !data.ok) { subsStatus.textContent = (data && data.message) || 'Search failed.'; return; }
    const results = data.results || [];
    if (results.length === 0) { subsStatus.textContent = 'No subtitles found.'; return; }
    subsStatus.textContent = results.length + ' result' + (results.length === 1 ? '' : 's') + ':';
    results.forEach((rsub) => {
      const li = document.createElement('li');
      const label = document.createElement('span');
      label.textContent = [
        rsub.language,
        rsub.release || rsub.file_name,
        rsub.download_count ? rsub.download_count + ' dl' : '',
        rsub.hearing_impaired ? 'SDH' : '',
      ].filter(Boolean).join(' · ');
      const use = document.createElement('button');
      use.type = 'button';
      use.className = 'btn btn-sm';
      use.textContent = 'Use';
      use.addEventListener('click', () => useSubtitle(rsub, use));
      li.appendChild(label);
      li.appendChild(use);
      subsResults.appendChild(li);
    });
  }

  async function useSubtitle(rsub, btn) {
    btn.disabled = true; btn.textContent = 'Loading…';
    const body = new URLSearchParams({ file: String(fileID), file_id: String(rsub.file_id), lang: rsub.language || 'en' });
    let data;
    try {
      const resp = await fetch(cfg.subtitleFetchURL, {
        method: 'POST',
        headers: { 'Content-Type': 'application/x-www-form-urlencoded', Accept: 'application/json' },
        body,
      });
      data = await resp.json();
    } catch (e) { btn.disabled = false; btn.textContent = 'Use'; subsStatus.textContent = 'Download failed.'; return; }
    if (!data || !data.ok || !data.url) {
      btn.disabled = false; btn.textContent = 'Use';
      subsStatus.textContent = (data && data.message) || 'Download failed.';
      return;
    }
    attachExternalSubtitle(data.url);
    closeSubsModal();
  }

  if (subsCloseBtn) subsCloseBtn.addEventListener('click', closeSubsModal);

  // begin=1 (set by gotoFile / the Prev-Next anchors on an explicit advance) →
  // start at 0; otherwise seekTo=null lets loadFromSession resume the saved position.
  // paused=1 (the home "Continue Watching" cards) → resume the saved position but
  // start paused, so arriving from the dashboard doesn't blast audio.
  const bootParams = new URLSearchParams(location.search);
  const beginAtStart = bootParams.get('begin') === '1';
  const startPaused = bootParams.get('paused') === '1';
  loadFromSession(0, 0, beginAtStart ? 0 : null, false, startPaused);

  // Land keyboard/remote focus on play/pause, so the first Enter/OK (or Space)
  // after opening a player toggles playback instead of activating whatever
  // couch.js anchored (the header title link — which navigated BACK). The
  // overlay auto-hide exempts this button (see hideControls): it keeps focus
  // while the controls are hidden, so OK-to-pause works chrome-up or chrome-
  // down — the standard TV-app idiom.
  if (toggleBtn) toggleBtn.focus({ preventScroll: true });
}

// Run on every Turbo navigation (and the initial load — Turbo fires turbo:load
// then too). initMediaPlayer returns immediately on pages without a player.
document.addEventListener('turbo:load', initMediaPlayer);
