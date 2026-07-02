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
  const subBurnIn = new Set(); // subtitle ordinals the server burns in (bitmap subs) — these change the video stream
  let recoverMediaDate = 0, recoverSwapDate = 0; // hls.js fatal-media-error recovery guards (anti-loop)

  const nativeHLS = video.canPlayType('application/vnd.apple.mpegurl') !== '';

  // currentAbsTime is the playback position on the real episode timeline. The
  // remux/burn-in streams are rebased to zero at their server-side start, so their
  // video.currentTime is relative — add the offset back to get the true position.
  const currentAbsTime = () => streamStartOffset + (video.currentTime || 0);

  function teardownHLS() {
    if (hls) { hls.destroy(); hls = null; }
  }

  // attachSource points the element (or hls.js) at the stream. seekTo is the
  // desired position on the real episode timeline. Direct-play and HLS are
  // byte-range/segment seekable, so we set video.currentTime. Remux and burn-in
  // are progressive (no random access), so instead we ask the server to begin the
  // stream at seekTo (?start=, an input -ss) and track the offset; the element's
  // own currentTime then runs from zero. This is what lets those paths resume.
  function attachSource(session, seekTo) {
    teardownHLS();
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
      hls = new Hls({ enableWorker: true, startPosition: clientSeek > 0 ? clientSeek : -1 });
      hls.loadSource(url);
      hls.attachMedia(video);
      hls.on(Hls.Events.MANIFEST_PARSED, onReady);
      // A fatal hls.js error (e.g. a transient MSE append/parse failure on a seek —
      // CHUNK_DEMUXER_ERROR_APPEND_FAILED) otherwise leaves the pipeline idle and the
      // screen black until a manual reload. Run the documented recovery: re-init the
      // media on a media error, restart loading on a network error. Guard the media
      // path against an infinite recover loop on a genuinely unplayable segment —
      // recover once, then swap-audio-codec + recover, then give up with a message.
      hls.on(Hls.Events.ERROR, (_evt, data) => {
        if (!data || !data.fatal) return;
        if (data.type === Hls.ErrorTypes.NETWORK_ERROR) {
          hls.startLoad();
          return;
        }
        if (data.type === Hls.ErrorTypes.MEDIA_ERROR) {
          const now = Date.now();
          if (now - recoverMediaDate > 3000) {
            recoverMediaDate = now;
            hls.recoverMediaError();
          } else if (now - recoverSwapDate > 3000) {
            recoverSwapDate = now;
            hls.swapAudioCodec();
            hls.recoverMediaError();
          } else {
            teardownHLS();
            modeLabel.textContent = 'Playback error — reload to continue';
          }
          return;
        }
        teardownHLS();
        modeLabel.textContent = 'Playback error — reload to continue';
      });
    } else {
      // Direct play, remux, burn-in, native-HLS (Safari): the element loads the URL directly.
      video.src = url;
      video.addEventListener('loadedmetadata', onReady, { once: true });
      video.load();
    }
    video.play().catch(() => {}); // autoplay may be blocked; user can press play
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
  // list — NOT read from captionTrack.activeCues. The clock is the frame's exact
  // presentation time (requestVideoFrameCallback's metadata.mediaTime) where
  // supported, else video.currentTime; both are the clock the audio/video ride
  // (HLS segments are stamped at true episode time via -output_ts_offset), so
  // anchoring display to it keeps subtitles locked to the dialogue. The browser's
  // own TextTrack cue scheduler (activeCues/cuechange) can drift ahead of that
  // clock over a long MSE/HLS session — subtitles creeping earlier and earlier
  // until you toggle them — so we never touch it. A linear scan of an episode's
  // cues per frame is trivial (a few µs).
  function computeActiveCues(t) {
    const all = captionTrack && captionTrack.cues;
    if (!all || !all.length) return [];
    if (typeof t !== 'number') t = video.currentTime; // event-handler arg / no-arg → currentTime
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
    renderActiveCues(meta && typeof meta.mediaTime === 'number' ? meta.mediaTime : undefined);
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
    const audio = session.audio_tracks || [];
    if (audio.length > 1) {
      audioSelect.innerHTML = '';
      audio.forEach((a) => {
        const o = document.createElement('option');
        o.value = a.ordinal;
        o.textContent = [a.language, a.title, a.codec].filter(Boolean).join(' · ') || ('Track ' + a.ordinal);
        if (a.default) o.selected = true;
        audioSelect.appendChild(o);
      });
      document.getElementById('audioPick').hidden = false;
    }
    // Text subtitles deliver as a WebVTT sidecar; bitmap subs (PGS/DVD/DVB) are
    // burned into the video by a continuous server-side transcode. Offer both,
    // marking bitmap tracks so the transcode cost is visible. Each track keeps its
    // original 1-based ordinal (what the server expects).
    const subs = session.subtitle_tracks || [];
    const textSubs = subs.filter((s) => s.text);
    if (subs.length > 0) {
      subSelect.innerHTML = '';
      const off = document.createElement('option');
      off.value = 0; off.textContent = 'Off'; off.selected = true;
      subSelect.appendChild(off);
      subs.forEach((s) => {
        const o = document.createElement('option');
        o.value = s.ordinal;
        const label = [s.language, s.title].filter(Boolean).join(' · ') || ('Subtitle ' + s.ordinal);
        o.textContent = s.text ? label : (label + ' · burn-in');
        subSelect.appendChild(o);
        if (!s.text) subBurnIn.add(Number(s.ordinal)); // bitmap → burned into the video stream
      });
      document.getElementById('subPick').hidden = false;
    }
    // No deliverable text track? Offer the OpenSubtitles search (when a key is
    // configured) — an external text sidecar is preferable to a burn-in transcode.
    const searchBtn = document.getElementById('subsSearchBtn');
    if (searchBtn && video.dataset.osEnabled === '1' && textSubs.length === 0) {
      searchBtn.hidden = false;
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

  async function loadFromSession(aud, sub, seekTo, subtitleOnly) {
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
    resetSkip();
    modeLabel.textContent = session.decision.replace('_', ' ');
    if (session.protocol === 'hls' && !nativeHLS && !(window.Hls && Hls.isSupported())) {
      modeLabel.textContent = 'This browser cannot play the transcoded stream';
    }
    const resume = (seekTo != null) ? seekTo : (session.completed ? 0 : (session.resume_position_seconds || 0));
    attachSource(session, resume);
    attachSubtitle(session);
  }

  // blur() after a pick so the <select> (which lives inside the auto-hiding
  // .media-overlay) doesn't keep :focus-visible and pin the controls open.
  if (audioSelect) audioSelect.addEventListener('change', () => { loadFromSession(parseInt(audioSelect.value, 10) || 0, currentSub, currentAbsTime()); audioSelect.blur(); });
  if (subSelect) subSelect.addEventListener('change', () => {
    const newSub = parseInt(subSelect.value, 10) || 0;
    // Reload the video stream only when burn-in is involved (entering or leaving a
    // bitmap sub changes the stream); a text/off↔text/off change is a sidecar swap.
    const reload = subBurnIn.has(newSub) || subBurnIn.has(currentSub);
    loadFromSession(currentAud, newSub, currentAbsTime(), !reload);
    subSelect.blur();
  });

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
  // resume -ss). The ±10s transport buttons drive it; the native <video> scrubber
  // can't (it can't address an unindexed stream).
  function seekProgressiveTo(targetAbs) {
    if (reloading) return;
    targetAbs = Math.max(0, targetAbs);
    if (sessionDuration > 1 && targetAbs > sessionDuration - 1) targetAbs = sessionDuration - 1;
    reloading = true;
    loadFromSession(currentAud, currentSub, targetAbs).finally(() => { reloading = false; });
  }

  const seekBy = (delta) => {
    if (isProgressive) { seekProgressiveTo(currentAbsTime() + delta); return; }
    let t = video.currentTime + delta;
    if (t < 0) t = 0;
    if (Number.isFinite(video.duration) && video.duration > 0 && t > video.duration) t = video.duration;
    video.currentTime = t;
  };

  const togglePlay = () => { if (video.paused) video.play().catch(() => {}); else video.pause(); };
  if (rewindBtn) rewindBtn.addEventListener('click', () => seekBy(-10));
  if (forwardBtn) forwardBtn.addEventListener('click', () => seekBy(10));
  if (toggleBtn) toggleBtn.addEventListener('click', togglePlay);

  // Reload: re-fetch the session and replay at the current position — recovers a
  // wedged/desynced stream in place (same path a progressive seek uses).
  const reloadBtn = document.getElementById('tvReloadBtn');
  if (reloadBtn) reloadBtn.addEventListener('click', () => loadFromSession(currentAud, currentSub, currentAbsTime()));

  // Prev/next EPISODE (TV only): shown when the page supplied an adjacent file id
  // (hidden on movies and at season boundaries), navigating to that episode.
  const gotoFile = (id) => { window.location.href = '/' + video.dataset.mediaKind + '/player?file=' + id; };
  const prevEpBtn = document.getElementById('tvPrevEpBtn');
  const nextEpBtn = document.getElementById('tvNextEpBtn');
  if (prevEpBtn && prevFile > 0) { prevEpBtn.hidden = false; prevEpBtn.addEventListener('click', () => gotoFile(prevFile)); }
  if (nextEpBtn && nextFile > 0) { nextEpBtn.hidden = false; nextEpBtn.addEventListener('click', () => gotoFile(nextFile)); }

  // Click the video frame to play/pause (standard player UX). The listener is on
  // the <video> itself, so it fires only on direct video clicks — the overlay
  // controls and the floating skip button are separate elements that never reach
  // it (and .media-captions is pointer-events:none, so a click through a caption
  // still toggles). When the controls are auto-hidden the overlay is
  // pointer-events:none, so the whole frame toggles; while visible its bottom strip
  // intercepts (the play button is right there). A click also bumps the controls
  // visible via the wrap's pointerdown listener.
  video.addEventListener('click', togglePlay);
  video.addEventListener('play', () => { if (transport) transport.classList.add('playing'); });
  video.addEventListener('pause', () => { if (transport) transport.classList.remove('playing'); });

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

  const seekToAbs = (target) => { if (isProgressive) { seekProgressiveTo(target); return; } try { video.currentTime = Math.max(0, target); } catch (e) {} };
  const segId = (s) => s.start + ':' + s.end;
  const skipLabel = (k) => (k === 'commercial' ? 'Skip commercial' : k === 'recap' ? 'Skip recap' : 'Skip intro');
  function segmentAt(t) { for (const s of skipSegments) { if (t >= s.start && t < s.end) return s; } return null; }
  function reflectSkipAuto() { if (skipAutoBtn) { skipAutoBtn.classList.toggle('is-on', skipAuto); skipAutoBtn.setAttribute('aria-pressed', skipAuto ? 'true' : 'false'); } }
  function resetSkip() {
    skippedAuto.clear();
    activeSkip = null;
    if (skipBtn) skipBtn.hidden = true;
    if (skipAutoBtn) skipAutoBtn.hidden = skipSegments.length === 0;
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
    if (dragging) return;
    const dur = sessionDuration || video.duration || 0;
    renderScrub(dur > 0 ? currentAbsTime() / dur : 0, currentAbsTime());
  }
  video.addEventListener('timeupdate', updateScrubFromPlayback);
  // timeupdate (~4×/s) is the portable floor for the caption render — it keeps cues
  // locked to currentTime everywhere (and is the sole driver on browsers without
  // requestVideoFrameCallback), and it covers paused-seek (seeked fires timeupdate).
  // The rVFC loop (addCaptionTrack) sharpens boundaries to ~1 frame where supported.
  // renderActiveCues no-ops when the active set is unchanged, so the overlap is free.
  video.addEventListener('timeupdate', renderActiveCues);
  video.addEventListener('loadedmetadata', updateScrubFromPlayback);

  // --- ?subdebug=1: opt-in subtitle-sync diagnostics (off by default, no overhead
  //     when absent). Logs, once a second, the media clock vs our currentTime-
  //     computed active cue vs the browser's own activeCues — so `drift` (browser −
  //     ours) reveals the TextTrack scheduler creeping ahead over a long session.
  let subDebugTimer = null;
  if (new URLSearchParams(location.search).get('subdebug') === '1') {
    subDebugTimer = setInterval(() => {
      const comp = computeActiveCues();
      const compStart = comp.length ? +comp[0].startTime.toFixed(3) : null;
      const ac = captionTrack && captionTrack.activeCues;
      const browserStart = (ac && ac.length) ? +ac[0].startTime.toFixed(3) : null;
      const bufEnd = video.buffered.length ? +video.buffered.end(video.buffered.length - 1).toFixed(2) : null;
      console.log('[subdebug] ' + JSON.stringify({
        t: +video.currentTime.toFixed(3), computedCueStart: compStart, browserCueStart: browserStart,
        drift: (compStart != null && browserStart != null) ? +(browserStart - compStart).toFixed(3) : null,
        bufEnd: bufEnd, paused: video.paused,
      }));
    }, 1000);
  }

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
    scrubber.addEventListener('keydown', (e) => {
      if (e.key === 'ArrowRight') { e.preventDefault(); e.stopPropagation(); seekBy(10); }
      else if (e.key === 'ArrowLeft') { e.preventDefault(); e.stopPropagation(); seekBy(-10); }
    });
  }

  // Fullscreen the video-wrap (which holds the auto-hiding controls overlay), so
  // the custom transport + scrubber stay reachable in fullscreen — there are no
  // native controls to fall back on.
  const fsBtn = document.getElementById('tvFullscreenBtn');
  if (fsBtn) {
    const fsTarget = video.closest('.tv-player-video-wrap') || video;
    fsBtn.addEventListener('click', () => {
      if (document.fullscreenElement) {
        document.exitFullscreen().catch(() => {});
      } else if (fsTarget.requestFullscreen) {
        fsTarget.requestFullscreen().catch(() => {});
      }
    });
  }

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
    // Keep couch.js directional nav from also consuming Left/Right on the slider.
    volSlider.addEventListener('keydown', (e) => {
      if (e.key === 'ArrowLeft' || e.key === 'ArrowRight') e.stopPropagation();
    });
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
      const ae = document.activeElement;
      if (video.paused || dragging || (overlay.contains(ae) && ae.matches(':focus-visible'))) return;
      // Reaching here, a focused overlay control is mouse-focused (not
      // :focus-visible). The hidden overlay is opacity:0 + pointer-events:none —
      // which blocks the pointer but NOT keyboard activation, so a button the
      // mouse last clicked would re-fire on Space/Enter while invisible. Drop its
      // focus before hiding. <button> only: never a <select> (blur would close an
      // open popup; selects blur themselves on change) and not the scrubber/volume
      // (their arrow behavior is harmless). Blur before the class-add so the
      // synchronous focusout→bump re-show is immediately overridden — overlay still
      // ends hidden.
      if (overlay.contains(ae) && ae.tagName === 'BUTTON') ae.blur();
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
  video.addEventListener('ended', () => {
    reportProgress(true);
    if (nextFile > 0) window.location.href = cfg.playerURL + '?file=' + nextFile;
  });
  // Named so the turbo:before-cache teardown can remove it — otherwise a fresh
  // listener (closing over a detached video) accumulates on every player visit.
  const onBeforeUnload = () => { lastReport = 0; reportProgress(false); };
  window.addEventListener('beforeunload', onBeforeUnload);

  // Turbo swaps the page without a full unload: send a final progress report,
  // stop playback, and tear down the hls.js worker before this page is
  // cached/replaced. Pause + clear the source so a direct/remux stream (no hls
  // instance to destroy) can't keep playing audio from a detached element. The
  // topbar "resume" chip (tv_resume.js) then links back here. once:true so it
  // doesn't accumulate across repeat visits.
  document.addEventListener('turbo:before-cache', () => {
    lastReport = 0;
    reportProgress(false);
    video.pause();
    teardownHLS();
    if (audioCtx) { try { audioCtx.close(); } catch (e) {} }
    if (subDebugTimer) clearInterval(subDebugTimer);
    stopRVFC();
    window.removeEventListener('beforeunload', onBeforeUnload);
    video.removeAttribute('src');
    try { video.load(); } catch (e) {}
  }, { once: true });

  // --- OpenSubtitles search dialog (shown only when no text track is offered
  //     and a key is configured; TV only — movie pages don't set the endpoints) ---
  const subsModal = document.getElementById('subs-modal');
  const subsStatus = document.getElementById('subs-status');
  const subsResults = document.getElementById('subs-results');
  const subsSearchBtn = document.getElementById('subsSearchBtn');
  const subsCloseBtn = document.getElementById('subs-close-btn');

  function openSubsModal() {
    if (!subsModal) return;
    subsModal.classList.remove('hidden');
    runSubsSearch();
  }
  function closeSubsModal() {
    if (subsModal) subsModal.classList.add('hidden');
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

  if (subsSearchBtn) subsSearchBtn.addEventListener('click', openSubsModal);
  if (subsCloseBtn) subsCloseBtn.addEventListener('click', closeSubsModal);

  loadFromSession(0, 0, null);
}

// Run on every Turbo navigation (and the initial load — Turbo fires turbo:load
// then too). initMediaPlayer returns immediately on pages without a player.
document.addEventListener('turbo:load', initMediaPlayer);
