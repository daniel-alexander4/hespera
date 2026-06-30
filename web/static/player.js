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
  const onYTError = () => ytStopPoll();

  // Load+play a videoId on the hidden player, creating it on first use.
  const ytPlay = (videoId) => {
    ytPendingVideoId = videoId;
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
            if (ytPendingVideoId) yt.loadVideoById(ytPendingVideoId);
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
    if (hasMediaSession) {
      try {
        navigator.mediaSession.metadata = null;
        navigator.mediaSession.playbackState = 'none';
      } catch (_) {}
    }
    updateHeader(); // currentTrack() is null -> hides #np-cluster
    renderView(); // empty state if the now-playing page is open
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
      ytPlay(t.videoId);
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
        if (o.startTrackId > 0) {
          const at = queue.findIndex((idx) => tracks[idx].id === o.startTrackId);
          if (at >= 0) {
            if (o.shuffle) queue.unshift(queue.splice(at, 1)[0]);
            else queue = queue.slice(at);
          }
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
    });
  };

  // --- Now-playing view (/music/player) binding ---
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
        view.playlistModal.classList.add('hidden');
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
      return;
    }
    view.empty.classList.add('hidden');
    view.main.classList.remove('hidden');
    view.albumTitle.textContent = t.album;
    const artist = (t.artist || '').trim();
    view.trackTitle.textContent = artist ? t.title + ' — ' + artist : t.title;
    delete view.coverImg.dataset.fallbackApplied;
    view.coverImg.src = t.coverUrl || '/art/album/' + t.albumId;
    view.coverImg.classList.remove('hidden');
    view.coverPh.classList.add('hidden');
    renderPlaylist();
    renderKaraokeAt(curTime() || 0);
    updateSeek();
  };

  // Locate and wire the now-playing view if it's on the page. Runs per Turbo
  // visit; the view nodes are fresh each time, so listeners bind cleanly.
  const bindView = () => {
    const page = document.querySelector('.player-page');
    if (!page) {
      view = null;
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
      playlistModal: $('playlist-modal'),
      playlistList: $('playlist-list'),
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
      view.playlistOpenBtn.addEventListener('click', () => view.playlistModal.classList.remove('hidden'));
    if (view.playlistCloseBtn)
      view.playlistCloseBtn.addEventListener('click', () => view.playlistModal.classList.add('hidden'));
    view.playlistModal.addEventListener('click', (e) => {
      if (e.target === view.playlistModal) view.playlistModal.classList.add('hidden');
    });

    renderView();

    // Direct deep link / no-JS fallback: /music/player?album=N told us to load.
    const autoload = page.getAttribute('data-autoload');
    if (autoload && !queue.length) playFromHref('?' + autoload);
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

  // Intercept [data-play] clicks before Turbo sees them (capture phase) so play
  // happens in place instead of navigating; the href is the no-JS fallback.
  document.addEventListener(
    'click',
    (e) => {
      const el = e.target.closest && e.target.closest('[data-play]');
      if (!el) return;
      e.preventDefault();
      e.stopPropagation();
      playFromHref(el.getAttribute('href') || el.getAttribute('data-play') || '');
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

  // The era "play a year range" GET form → load that queue in place.
  document.addEventListener(
    'submit',
    (e) => {
      const form = e.target.closest && e.target.closest('[data-play-form]');
      if (!form) return;
      e.preventDefault();
      e.stopPropagation();
      const qs = new URLSearchParams(new FormData(form)).toString();
      loadQueue(qs, { shuffle: form.getAttribute('data-shuffle') === '1' });
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

  // Final listen report when the tab actually closes (rare under Turbo).
  window.addEventListener('beforeunload', () => reportCurrentTrack(false, { beacon: true }));

  // Re-bind the now-playing view + refresh the header on every Turbo visit
  // (turbo:load fires on the initial load too).
  document.addEventListener('turbo:load', () => {
    bindView();
    updateHeader();
  });
})();
