// player.js — global music-player controller.
//
// Owns the single, Turbo-permanent <audio id="hespera-audio"> in the layout
// shell, so playback survives navigation (Turbo Drive swaps <body> but carries
// data-turbo-permanent elements across, uninterrupted). All play actions on
// album/artist/home pages are [data-play] controls this script intercepts: it
// fetches /music/queue (JSON) and plays in place — no page jump, no audio
// teardown. The compact header cluster (#np-cluster) shows what's playing with a
// play/pause toggle and a title that links to the now-playing view (/music/player).
// When that view is on the page, this script binds its full transport + cover +
// playlist + synced-lyrics display to the same state.
(() => {
  const audio = document.getElementById('hespera-audio');
  if (!audio) return; // shell not present (shouldn't happen)

  const npCluster = document.getElementById('np-cluster');
  const npToggle = document.getElementById('np-toggle');
  const npTitle = document.getElementById('np-title');

  const MIN_REPORT_MS = 15 * 1000;
  const hasMediaSession = 'mediaSession' in navigator;

  // Playback state (lives here, not in any page).
  let tracks = []; // [{id, albumId, album, title, artist}]
  let queue = []; // indices into tracks
  let currentPos = -1;
  let currentTrackReported = false;
  let karaokeLines = [];
  let karaokeToken = 0; // guards a stale lyrics fetch from overwriting a newer track
  let view = null; // now-playing view DOM refs, when that page is shown
  // The autoload query of the queue currently loaded into the player. Persists
  // across Turbo navigations (this script loads once in the shell), so returning
  // to /music/player — via the back button, a restoration visit, or a cached
  // preview re-render — doesn't reload-and-restart a queue that's already
  // playing. Only a *different* play request (new params) re-loads. Cleared on
  // stop so the same queue can be started again afterwards.
  let loadedAutoload = null;

  // --- Playback engine: 'local' (the <audio>) or 'yt' (a hidden YouTube IFrame
  // player, for un-owned year-journey songs). The transport reads/writes through
  // the cur*/do*/seekTo accessors so the same controls drive either engine; the
  // local code paths are unchanged when engine === 'local'.
  let engine = 'local';
  let yt = null; // YT.Player once created (reused across songs)
  let ytPlaying = false;
  let ytPoll = null; // drives timeupdate (YT has no timeupdate event)
  let ytPendingVideoId = '';
  let ytApiLoading = false;
  let ytReadyCbs = [];
  // Position (seconds) to resume the pending YT video at. A Turbo body swap
  // reparents the data-turbo-permanent #yt-host iframe, and an <iframe> reloads
  // its document when detached+reattached (a <video>/<audio> does not), which
  // re-fires onReady → loadVideoById from 0. Captured on turbo:before-render
  // while a YT track is live, then consumed once in onReady so the track resumes
  // where it was instead of restarting on every navigation.
  let ytResumeSeconds = 0;

  const curTime = () => (engine === 'yt' ? (yt && yt.getCurrentTime ? yt.getCurrentTime() : 0) : (audio.currentTime || 0));
  const curDur = () => (engine === 'yt' ? (yt && yt.getDuration ? yt.getDuration() : 0) : audio.duration);
  const curPaused = () => (engine === 'yt' ? !ytPlaying : audio.paused);
  const seekTo = (s) => {
    if (engine === 'yt') {
      if (yt && yt.seekTo) yt.seekTo(s, true);
    } else audio.currentTime = s;
  };
  const doPlay = () => {
    if (engine === 'yt') {
      if (yt && yt.playVideo) yt.playVideo();
    } else audio.play().catch(() => {});
  };
  const doPause = () => {
    if (engine === 'yt') {
      if (yt && yt.pauseVideo) yt.pauseVideo();
    } else audio.pause();
  };

  // Load the YouTube IFrame API once, then run cb when YT.Player is available.
  const ensureYTApi = (cb) => {
    if (window.YT && window.YT.Player) {
      cb();
      return;
    }
    ytReadyCbs.push(cb);
    if (ytApiLoading) return;
    ytApiLoading = true;
    const prev = window.onYouTubeIframeAPIReady;
    window.onYouTubeIframeAPIReady = () => {
      if (typeof prev === 'function') prev();
      const cbs = ytReadyCbs;
      ytReadyCbs = [];
      cbs.forEach((f) => f());
    };
    const s = document.createElement('script');
    s.src = 'https://www.youtube.com/iframe_api';
    document.head.appendChild(s);
  };

  const ytStartPoll = () => {
    if (ytPoll) return;
    ytPoll = setInterval(() => {
      if (engine !== 'yt') return;
      renderKaraokeAt(curTime());
      updateSeek();
    }, 250);
  };
  const ytStopPoll = () => {
    if (ytPoll) {
      clearInterval(ytPoll);
      ytPoll = null;
    }
  };

  const onYTState = (e) => {
    if (engine !== 'yt' || !window.YT) return;
    const S = window.YT.PlayerState;
    if (e.data === S.PLAYING) {
      ytPlaying = true;
      updateHeader();
      ytStartPoll();
      if (hasMediaSession) navigator.mediaSession.playbackState = 'playing';
    } else if (e.data === S.PAUSED) {
      ytPlaying = false;
      updateHeader();
      if (hasMediaSession) navigator.mediaSession.playbackState = 'paused';
    } else if (e.data === S.ENDED) {
      ytPlaying = false;
      ytStopPoll();
      updateHeader();
      reportCurrentTrack(true);
      playNext();
    }
  };
  // Most music videos are embeddable; a 101/150 (embedding disabled) or other
  // error just leaves the song idle (no gesture to recover to a tab with).
  const onYTError = () => {
    ytStopPoll();
    playNext(); // a non-embeddable video (101/150) shouldn't stall the journey queue
  };

  // Load+play a videoId on the hidden player, creating it on first use.
  const ytPlay = (videoId) => {
    ytPendingVideoId = videoId;
    ytResumeSeconds = 0; // a genuine (re)start begins at 0 — never inherit a nav resume offset

    ensureYTApi(() => {
      if (yt) {
        yt.loadVideoById(videoId);
        ytStartPoll();
        return;
      }
      yt = new window.YT.Player('yt-player', {
        height: '0',
        width: '0',
        playerVars: { autoplay: 1, controls: 0, disablekb: 1, playsinline: 1, rel: 0 },
        events: {
          onReady: () => {
            if (ytPendingVideoId) {
              // Resume where a nav-triggered iframe reload left off (see
              // ytResumeSeconds); a genuine new track loads via ytPlay (below),
              // not this path, so the offset only applies on reconnect.
              yt.loadVideoById(ytResumeSeconds > 0
                ? { videoId: ytPendingVideoId, startSeconds: ytResumeSeconds }
                : ytPendingVideoId);
              ytResumeSeconds = 0;
            }
            ytStartPoll();
          },
          onStateChange: onYTState,
          onError: onYTError,
        },
      });
    });
  };
  const ytStop = () => {
    ytStopPoll();
    ytPlaying = false;
    try {
      if (yt && yt.pauseVideo) yt.pauseVideo();
    } catch (_) {}
  };

  // Start an un-owned song as YouTube audio — a one-off takeover of the queue.
  const playYouTubeSong = (videoId, artist, song, coverUrl) => {
    reportCurrentTrack(false);
    tracks = [{ kind: 'yt', videoId, title: song, artist, album: '', albumId: 0, coverUrl: coverUrl || '' }];
    queue = [0];
    currentPos = -1;
    playAt(0);
  };

  const currentTrack = () =>
    currentPos >= 0 && currentPos < queue.length ? tracks[queue[currentPos]] : null;

  const shuffleArray = (xs) => {
    const copy = xs.slice();
    for (let i = copy.length - 1; i > 0; i--) {
      const j = Math.floor(Math.random() * (i + 1));
      [copy[i], copy[j]] = [copy[j], copy[i]];
    }
    return copy;
  };

  // --- Play-event reporting (listens / popularity feed) ---
  const reportPlayEvent = (payload, opts) => {
    if (!payload || !payload.track_id) return;
    const body = JSON.stringify(payload);
    if (opts && opts.beacon && navigator.sendBeacon) {
      navigator.sendBeacon('/music/play-event', new Blob([body], { type: 'application/json' }));
      return;
    }
    fetch('/music/play-event', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body,
      keepalive: true,
    }).catch(() => {});
  };

  const reportCurrentTrack = (completed, opts) => {
    if (currentTrackReported) return;
    const t = currentTrack();
    if (!t) return;
    if (!t.id) { // YouTube tracks have no local id — nothing to log
      currentTrackReported = true;
      return;
    }
    const playedMs = Math.max(0, Math.floor((curTime() || 0) * 1000));
    if (!completed && playedMs < MIN_REPORT_MS) return;
    currentTrackReported = true;
    reportPlayEvent({ track_id: t.id, played_ms: playedMs, completed: !!completed, source: 'page' }, opts);
  };

  // --- OS media controls ---
  const setMediaMetadata = (t) => {
    if (!hasMediaSession || !t) return;
    try {
      const meta = { title: t.title || '', artist: t.artist || '', album: t.album || '' };
      const artSrc = t.coverUrl || (t.albumId ? '/art/album/' + t.albumId : '');
      if (artSrc) meta.artwork = [{ src: artSrc, sizes: '512x512', type: 'image/jpeg' }];
      navigator.mediaSession.metadata = new MediaMetadata(meta);
    } catch (_) {}
  };

  // --- Synced lyrics (karaoke), rendered into the now-playing view if present ---
  const parseSyncedLyrics = (text) => {
    const out = [];
    const re = /^\[(\d{1,2}):(\d{2})(?:\.(\d{1,3}))?\]\s*(.*)$/;
    for (const raw of (text || '').split('\n')) {
      const m = raw.match(re);
      if (!m) continue;
      const frac = m[3] ? parseInt(m[3].padEnd(3, '0'), 10) / 1000 : 0;
      out.push({ start: parseInt(m[1], 10) * 60 + parseInt(m[2], 10) + frac, text: (m[4] || '').trim() });
    }
    out.sort((a, b) => a.start - b.start);
    return out;
  };

  const fmtTime = (s) => {
    if (!Number.isFinite(s) || s < 0) s = 0;
    const m = Math.floor(s / 60);
    const sec = Math.floor(s % 60);
    return m + ':' + (sec < 10 ? '0' : '') + sec;
  };

  // Reflect playback position into the now-playing view's scrubber + time label.
  const updateSeek = () => {
    if (!view || !view.seek) return;
    const d = curDur();
    const ct = curTime();
    if (!view.seeking) {
      view.seek.value = Number.isFinite(d) && d > 0 ? String((ct / d) * 1000) : '0';
    }
    view.timeLabel.textContent = fmtTime(ct) + ' / ' + (Number.isFinite(d) && d > 0 ? fmtTime(d) : '–:––');
  };

  const renderKaraokeAt = (pos) => {
    if (!view || !karaokeLines.length) return;
    let idx = -1;
    for (let i = 0; i < karaokeLines.length; i++) {
      if (karaokeLines[i].start <= pos + 0.02) idx = i; else break;
    }
    view.karaokeCurrent.textContent = idx >= 0 ? karaokeLines[idx].text : '';
    view.karaokeNext.textContent = idx + 1 < karaokeLines.length ? karaokeLines[idx + 1].text : '';
  };

  const loadKaraokeForTrack = (t) => {
    karaokeLines = [];
    const token = ++karaokeToken;
    if (view) {
      view.karaokeCurrent.textContent = 'Loading lyrics…';
      view.karaokeNext.textContent = '';
    }
    if (!t.id) {
      // A YouTube (no-local-id) track has no lyrics in lyrics_cache.
      if (view) {
        view.karaokeCurrent.textContent = '';
        view.karaokeNext.textContent = '';
      }
      return;
    }
    fetch('/music/lyrics/fetch', {
      method: 'POST',
      headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
      body: 'track_id=' + encodeURIComponent(t.id),
    })
      .then((r) => r.json())
      .then((payload) => {
        if (token !== karaokeToken) return; // a newer track took over
        const d = (payload && payload.data) || {};
        const synced = (d.synced_lyrics || '').trim();
        if (synced) {
          karaokeLines = parseSyncedLyrics(synced);
          renderKaraokeAt(audio.currentTime || 0);
        } else if (view) {
          view.karaokeCurrent.textContent = (d.lyrics || '').trim() ? 'Synced lyrics unavailable' : 'Lyrics unavailable';
          view.karaokeNext.textContent = '';
        }
      })
      .catch(() => {
        if (token !== karaokeToken || !view) return;
        view.karaokeCurrent.textContent = 'Lyrics unavailable';
        view.karaokeNext.textContent = '';
      });
  };

  // --- Header cluster ---
  const updateHeader = () => {
    const t = currentTrack();
    if (!npCluster) return;
    if (!t) {
      npCluster.classList.add('hidden');
      return;
    }
    npCluster.classList.remove('hidden');
    if (npTitle) npTitle.textContent = t.artist ? t.title + ' — ' + t.artist : t.title;
    npCluster.classList.toggle('np-paused', curPaused());
  };

  // Stop playback entirely and dismiss the now-playing cluster (the X control).
  const stopPlayback = () => {
    reportCurrentTrack(false);
    if (engine === 'yt') {
      ytStop();
    } else {
      audio.pause();
      audio.removeAttribute('src');
      audio.load(); // detach the source so it can't resume
    }
    tracks = [];
    queue = [];
    currentPos = -1;
    currentTrackReported = true;
    loadedAutoload = null; // a fresh start after stop should autoload again
    if (hasMediaSession) {
      try {
        navigator.mediaSession.metadata = null;
        navigator.mediaSession.playbackState = 'none';
      } catch (_) {}
    }
    updateHeader(); // currentTrack() is null -> hides #np-cluster
    renderView(); // empty state if the now-playing page is open
  };

  // Resolve a queued un-owned (yt-kind) track to a YouTube videoId on demand,
  // then play it. A miss / no key skips to the next track so the journey keeps
  // moving instead of stalling on a song with no playable video. Quota is spent
  // only on songs actually reached (one cached API call per song, ever).
  const resolveAndPlayYT = (t) => {
    fetch(
      '/music/youtube/resolve?artist=' + encodeURIComponent(t.artist || '') + '&song=' + encodeURIComponent(t.title || ''),
      { headers: { Accept: 'application/json' } },
    )
      .then((r) => (r.ok ? r.json() : Promise.reject(new Error('resolve'))))
      .then((d) => {
        if (currentTrack() !== t) return; // user advanced while resolving
        if (d.videoId) {
          t.videoId = d.videoId;
          if (!t.coverUrl) t.coverUrl = 'https://i.ytimg.com/vi/' + d.videoId + '/mqdefault.jpg';
          setMediaMetadata(t);
          updateHeader();
          renderView();
          ytPlay(d.videoId);
        } else {
          playNext(); // no embeddable video (no key / no match) — skip
        }
      })
      .catch(() => {
        if (currentTrack() === t) playNext();
      });
  };

  // --- Core transport ---
  const playAt = (pos) => {
    if (pos < 0 || pos >= queue.length) return;
    reportCurrentTrack(false);
    currentPos = pos;
    currentTrackReported = false;
    const t = tracks[queue[currentPos]];
    if (t.kind === 'yt') {
      engine = 'yt';
      audio.pause(); // gated local listeners ignore this; just silences local
      if (t.videoId) {
        ytPlay(t.videoId);
      } else {
        resolveAndPlayYT(t); // queued un-owned song: resolve a videoId, then play
      }
    } else {
      const wasYT = engine === 'yt';
      engine = 'local';
      if (wasYT) ytStop();
      audio.src = '/stream/track/' + t.id;
      audio.play().catch(() => {});
    }
    setMediaMetadata(t);
    updateHeader();
    loadKaraokeForTrack(t);
    renderView();
  };

  const playNext = () => {
    if (currentPos + 1 < queue.length) {
      playAt(currentPos + 1);
      return true;
    }
    return false;
  };

  const playPrev = () => {
    if (curTime() > 10 || currentPos <= 0) {
      seekTo(0);
      return;
    }
    playAt(currentPos - 1);
  };

  const toggle = () => {
    if (curPaused()) doPlay();
    else doPause();
  };

  // --- Load a queue from /music/queue and start it ---
  // queryString is the album/source/era params; shuffle + startTrackId are
  // applied client-side exactly as the old player page did.
  const loadQueue = (queryString, opts) => {
    const o = opts || {};
    fetch('/music/queue?' + queryString, { headers: { Accept: 'application/json' } })
      .then((r) => (r.ok ? r.json() : Promise.reject(new Error('queue ' + r.status))))
      .then((data) => {
        const next = (data && data.tracks) || [];
        if (!next.length) return;
        // Report the outgoing track against the CURRENT queue before swapping —
        // otherwise playAt(0)'s own report would read the old currentPos against
        // the new tracks/queue and mis-log (or drop) the play event. The
        // currentTrackReported guard then makes playAt(0)'s report a no-op.
        reportCurrentTrack(false);
        tracks = next;
        queue = tracks.map((_, i) => i);
        if (o.shuffle) queue = shuffleArray(queue);
        // Resolve a start position: by track id (owned) or by title+artist (for
        // mixed owned/YouTube journey queues, whose yt entries have no id).
        let at = -1;
        if (o.startTrackId > 0) {
          at = queue.findIndex((idx) => tracks[idx].id === o.startTrackId);
        } else if (o.startTitle) {
          const t0 = o.startTitle.trim().toLowerCase();
          const a0 = (o.startArtist || '').trim().toLowerCase();
          at = queue.findIndex(
            (idx) =>
              (tracks[idx].title || '').trim().toLowerCase() === t0 &&
              (tracks[idx].artist || '').trim().toLowerCase() === a0,
          );
        }
        if (at >= 0 && o.keepPrev) {
          // Start at the song but keep earlier ones queued as "previous" (journey).
          playAt(at);
          return;
        }
        if (at >= 0) {
          // "Play from here": drop earlier tracks (album semantics).
          if (o.shuffle) queue.unshift(queue.splice(at, 1)[0]);
          else queue = queue.slice(at);
        }
        if (!queue.length) return;
        playAt(0);
      })
      .catch(() => {});
  };

  // Parse the play params out of a /music/player(?...) or /music/queue(?...) href.
  const playFromHref = (href) => {
    const qs = (href || '').split('?')[1] || '';
    const params = new URLSearchParams(qs);
    loadQueue(qs, {
      shuffle: params.get('shuffle') === '1',
      startTrackId: parseInt(params.get('track') || '0', 10) || 0,
      startTitle: params.get('startTitle') || '',
      startArtist: params.get('startArtist') || '',
      keepPrev: params.get('keep') === '1',
    });
  };

  // --- Now-playing view (/music/player) binding ---
  // Open/close the right-side playlist drawer. The element stays in the DOM and
  // animates via a transform; on close we add .hidden only after the slide-out
  // finishes so couch mode (which reads any on-screen, sized overlay as open)
  // treats the parked, off-screen drawer as closed.
  const setPlaylistOpen = (open) => {
    if (!view || !view.playlistDrawer) return;
    const d = view.playlistDrawer;
    const s = view.playlistScrim;
    if (open) {
      d.classList.remove('hidden');
      if (s) s.classList.remove('hidden');
      void d.offsetWidth; // force reflow so the transform transition runs
      d.classList.add('open');
      if (s) s.classList.add('open');
    } else {
      d.classList.remove('open');
      if (s) s.classList.remove('open');
      // Hide after the slide-out completes; skip if it was reopened meanwhile.
      d.addEventListener('transitionend', () => {
        if (!d.classList.contains('open')) {
          d.classList.add('hidden');
          if (s) s.classList.add('hidden');
        }
      }, { once: true });
    }
  };

  const renderPlaylist = () => {
    if (!view) return;
    view.playlistList.innerHTML = '';
    queue.forEach((trackIdx, idx) => {
      const t = tracks[trackIdx];
      const li = document.createElement('li');
      li.className = 'track playlist-item';
      const btn = document.createElement('button');
      btn.type = 'button';
      btn.className = 'track-play-btn';
      let prefix = '';
      if (idx < currentPos) prefix = '✓ ';
      else if (idx === currentPos) prefix = '▶ ';
      btn.textContent = prefix + t.album + ' — ' + t.title;
      btn.addEventListener('click', () => {
        playAt(idx);
        setPlaylistOpen(false);
      });
      li.appendChild(btn);
      view.playlistList.appendChild(li);
    });
  };

  // Update the now-playing view's display from current state (cover/titles/playlist).
  const renderView = () => {
    if (!view) return;
    const t = currentTrack();
    if (!t) {
      view.empty.classList.remove('hidden');
      view.main.classList.add('hidden');
      if (view.transport) view.transport.classList.add('hidden');
      setPlaylistOpen(false);
      return;
    }
    view.empty.classList.add('hidden');
    view.main.classList.remove('hidden');
    if (view.transport) view.transport.classList.remove('hidden');
    view.albumTitle.textContent = t.album;
    const artist = (t.artist || '').trim();
    view.trackTitle.textContent = artist ? t.title + ' — ' + artist : t.title;
    delete view.coverImg.dataset.fallbackApplied;
    // YT tracks are album-less (albumId 0) — never build /art/album/0; use the
    // resolved YouTube thumbnail (coverUrl) if present, else the placeholder.
    const coverSrc = t.coverUrl || (t.albumId ? '/art/album/' + t.albumId : '');
    if (coverSrc) {
      view.coverImg.src = coverSrc;
      view.coverImg.classList.remove('hidden');
      view.coverPh.classList.add('hidden');
    } else {
      view.coverImg.classList.add('hidden');
      view.coverPh.classList.remove('hidden');
    }
    renderPlaylist();
    renderKaraokeAt(curTime() || 0);
    updateSeek();
  };

  // --- Auto-collapsing transport ---
  // The bottom transport bar fully slides off-screen after a few seconds of
  // inactivity and re-reveals on pointer/keyboard/touch activity, mirroring the TV
  // player's auto-hiding controls overlay (no grab tab — activity brings it back).
  // These helpers act on the current `view`, so the document-level activity
  // listeners (bound once below) and the per-bind wiring share one code path
  // without leaks.
  const TRANSPORT_AUTO_MS = 3000;
  let transportTimer = null, transportHover = false;
  const applyTransportCollapsed = (c) => {
    if (!view || !view.transport) return;
    view.transport.classList.toggle('collapsed', c);
    view.transport.setAttribute('aria-hidden', String(c));
  };
  const idleCollapseTransport = () => {
    if (!view || !view.transport) return;
    // Stay open while paused, mid seek-drag, hovering the bar, or a control in it
    // is keyboard-focused (couch nav) — the same guards as the TV overlay.
    const ae = document.activeElement;
    if (curPaused() || view.seeking || transportHover ||
        (ae && ae !== document.body && view.transport.contains(ae) && ae.matches && ae.matches(':focus-visible'))) return;
    applyTransportCollapsed(true);
  };
  const revealTransport = () => {
    if (!view || !view.transport) return;
    if (view.transport.classList.contains('collapsed')) applyTransportCollapsed(false);
    clearTimeout(transportTimer);
    transportTimer = setTimeout(idleCollapseTransport, TRANSPORT_AUTO_MS);
  };

  // Locate and wire the now-playing view if it's on the page. Runs per Turbo
  // visit; the view nodes are fresh each time, so listeners bind cleanly.
  const bindView = () => {
    const page = document.querySelector('.player-page');
    if (!page) {
      view = null;
      clearTimeout(transportTimer); // no player view → stop any pending auto-collapse
      return;
    }
    const $ = (id) => document.getElementById(id);
    view = {
      page,
      main: $('player-main'),
      empty: $('player-empty'),
      coverImg: $('player-cover-img'),
      coverPh: $('player-cover-ph'),
      albumTitle: $('player-album-title'),
      trackTitle: $('player-track-title'),
      seek: $('player-seek'),
      timeLabel: $('player-time'),
      seeking: false,
      karaokeCurrent: $('player-karaoke-current'),
      karaokeNext: $('player-karaoke-next'),
      playlistOpenBtn: $('playlist-open-btn'),
      playlistCloseBtn: $('playlist-close-btn'),
      playlistDrawer: $('playlist-drawer'),
      playlistScrim: $('playlist-scrim'),
      playlistList: $('playlist-list'),
      transport: $('player-transport'),
    };

    $('player-prev-btn').addEventListener('click', playPrev);
    $('player-rewind-btn').addEventListener('click', () => {
      seekTo(Math.max(0, curTime() - 10));
    });
    $('player-toggle-btn').addEventListener('click', toggle);
    $('player-forward-btn').addEventListener('click', () => {
      const d = curDur();
      seekTo(Number.isFinite(d) && d > 0 ? Math.min(d, curTime() + 10) : curTime() + 10);
    });
    $('player-next-btn').addEventListener('click', () => playNext());

    if (view.seek) {
      view.seek.addEventListener('input', () => {
        view.seeking = true;
      });
      view.seek.addEventListener('change', () => {
        const d = curDur();
        if (Number.isFinite(d) && d > 0) seekTo((Number(view.seek.value) / 1000) * d);
        view.seeking = false;
      });
    }

    view.coverImg.addEventListener('error', () => {
      if (view.coverImg.dataset.fallbackApplied === '1') {
        view.coverImg.classList.add('hidden');
        view.coverPh.classList.remove('hidden');
        return;
      }
      view.coverImg.dataset.fallbackApplied = '1';
      view.coverImg.src = '/static/missing.album.webp';
    });
    if (view.playlistOpenBtn)
      view.playlistOpenBtn.addEventListener('click', () => setPlaylistOpen(true));
    if (view.playlistCloseBtn)
      view.playlistCloseBtn.addEventListener('click', () => setPlaylistOpen(false));
    if (view.playlistScrim)
      view.playlistScrim.addEventListener('click', () => setPlaylistOpen(false));

    // Auto-collapsing transport: starts revealed (arming the idle countdown) and
    // re-reveals on activity (see the shared helpers above). No manual tab — it
    // slides fully off-screen and comes back on the next pointer/key/touch.
    if (view.transport) {
      revealTransport(); // visible → arm the idle countdown
      // Keep it open while the pointer rests on the bar; resume the countdown on leave.
      view.transport.addEventListener('pointerenter', () => { transportHover = true; applyTransportCollapsed(false); clearTimeout(transportTimer); });
      view.transport.addEventListener('pointerleave', () => { transportHover = false; revealTransport(); });
    }

    renderView();

    // Direct deep link / no-JS fallback: /music/player?album=N told us to load.
    // Autoload whenever the URL carries play params (a play-nav landed here) —
    // not only when idle, so starting a new song while one plays swaps the queue.
    // The bare /music/player (the breadcrumb back) has no params → no reload.
    // Guard against re-loading the queue that's already playing: a revisit to the
    // same params URL (back button, Turbo restoration/preview) carries the same
    // data-autoload, and reloading would interrupt the current track and restart
    // from the beginning. Only a genuinely new request (different params) loads.
    const autoload = page.getAttribute('data-autoload');
    if (autoload && autoload !== loadedAutoload) {
      loadedAutoload = autoload;
      playFromHref('?' + autoload);
    }
  };

  // --- One-time wiring on the permanent audio + header + document ---
  // These fire on the <audio> element, so they're only meaningful while the local
  // engine is active; the YouTube engine drives the same handlers from onYTState.
  audio.addEventListener('ended', () => {
    if (engine !== 'local') return;
    reportCurrentTrack(true);
    playNext();
  });
  audio.addEventListener('timeupdate', () => {
    if (engine !== 'local') return;
    renderKaraokeAt(audio.currentTime || 0);
    updateSeek();
  });
  audio.addEventListener('durationchange', () => {
    if (engine !== 'local') return;
    updateSeek();
  });
  audio.addEventListener('play', () => {
    if (engine !== 'local') return;
    updateHeader();
    if (hasMediaSession) navigator.mediaSession.playbackState = 'playing';
  });
  audio.addEventListener('pause', () => {
    if (engine !== 'local') return;
    updateHeader();
    if (hasMediaSession) navigator.mediaSession.playbackState = 'paused';
  });

  if (npToggle) npToggle.addEventListener('click', toggle);
  {
    const npClose = document.getElementById('np-close');
    if (npClose) npClose.addEventListener('click', stopPlayback);
  }

  if (hasMediaSession) {
    const setHandler = (action, handler) => {
      try {
        navigator.mediaSession.setActionHandler(action, handler);
      } catch (_) {}
    };
    setHandler('play', doPlay);
    setHandler('pause', doPause);
    setHandler('previoustrack', playPrev);
    setHandler('nexttrack', () => playNext());
    setHandler('seekbackward', (d) => {
      seekTo(Math.max(0, curTime() - ((d && d.seekOffset) || 10)));
    });
    setHandler('seekforward', (d) => {
      seekTo(curTime() + ((d && d.seekOffset) || 10));
    });
  }

  // A [data-play] click takes you to the now-playing page, which autoloads the
  // queue from the href's params (the Turbo-permanent <audio> survives the swap).
  // When you're ALREADY on the player page, navigating to itself would be wasteful
  // — load the new queue in place instead. Otherwise let Turbo navigate the link.
  document.addEventListener(
    'click',
    (e) => {
      const el = e.target.closest && e.target.closest('[data-play]');
      if (!el) return;
      if (location.pathname === '/music/player') {
        e.preventDefault();
        e.stopPropagation();
        playFromHref(el.getAttribute('href') || el.getAttribute('data-play') || '');
      }
      // else: fall through — Turbo navigates the <a href="/music/player?…">.
    },
    true,
  );

  // [data-yt] (un-owned year-journey song): with a key, resolve to a videoId and
  // play it as audio through this player; without a key, open a YouTube search
  // tab synchronously in the gesture (popup-safe). Bound once, document-level.
  document.addEventListener('click', (e) => {
    const el = e.target.closest && e.target.closest('.js-yt');
    if (!el) return;
    e.preventDefault();
    const artist = el.getAttribute('data-artist') || '';
    const song = el.getAttribute('data-song') || '';
    const artUrl = el.getAttribute('data-art') || '';
    if (el.getAttribute('data-haskey') === '1') {
      fetch('/music/youtube/resolve?artist=' + encodeURIComponent(artist) + '&song=' + encodeURIComponent(song), {
        headers: { Accept: 'application/json' },
      })
        .then((r) => (r.ok ? r.json() : Promise.reject(new Error('resolve failed'))))
        .then((d) => {
          if (d.videoId) playYouTubeSong(d.videoId, artist, song, artUrl);
          else if (d.searchUrl) window.open(d.searchUrl, '_blank');
        })
        .catch(() => {});
    } else {
      const q = encodeURIComponent((artist ? artist + ' ' : '') + song);
      window.open('https://www.youtube.com/results?search_query=' + q, '_blank');
    }
  });

  // The era "play a year range" GET form → go to the player page with that
  // range's queue (in place if already there), matching every other play control.
  document.addEventListener(
    'submit',
    (e) => {
      const form = e.target.closest && e.target.closest('[data-play-form]');
      if (!form) return;
      e.preventDefault();
      e.stopPropagation();
      const qs = new URLSearchParams(new FormData(form)).toString();
      if (location.pathname === '/music/player') {
        playFromHref('?' + qs);
      } else if (window.Turbo) {
        window.Turbo.visit('/music/player?' + qs);
      } else {
        location.assign('/music/player?' + qs);
      }
    },
    true,
  );

  // If any other media starts (e.g. a TV video), pause the music so they don't
  // play over each other. 'play' doesn't bubble, so listen in the capture phase.
  document.addEventListener(
    'play',
    (e) => {
      const el = e.target;
      if (el !== audio && (el.tagName === 'VIDEO' || el.tagName === 'AUDIO')) {
        if (!audio.paused) audio.pause();
        if (engine === 'yt' && ytPlaying) ytStop();
      }
    },
    true,
  );

  // Reveal the auto-collapsing transport on any activity (bound once, document-
  // level; a no-op when the now-playing view isn't on the page).
  ['pointermove', 'pointerdown', 'keydown', 'touchstart'].forEach((ev) =>
    document.addEventListener(ev, revealTransport, { passive: true }));

  // Final listen report when the tab actually closes (rare under Turbo).
  window.addEventListener('beforeunload', () => reportCurrentTrack(false, { beacon: true }));

  // Re-bind the now-playing view + refresh the header on every Turbo visit
  // (turbo:load fires on the initial load too).
  document.addEventListener('turbo:load', () => {
    bindView();
    updateHeader();
  });

  // Before Turbo swaps the body (which reparents the permanent #yt-host iframe
  // and forces it to reload), remember the live YT position so onReady can
  // resume there instead of restarting the track. Only capture an actively
  // playing/paused YT track — an ended/idle one should not resume near its end.
  document.addEventListener('turbo:before-render', () => {
    if (engine !== 'yt' || !yt || !yt.getPlayerState) return;
    const st = yt.getPlayerState(); // 1 = playing, 2 = paused
    ytResumeSeconds = (st === 1 || st === 2) ? (yt.getCurrentTime() || 0) : 0;
  });
})();
