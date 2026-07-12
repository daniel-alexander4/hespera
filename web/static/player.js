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

  // Hardware media keys (a Flirc/BT remote's play-pause/FF/RW): Chrome consumes
  // them at the browser level and routes them to the page-global Media Session,
  // whose handlers are registered ONCE here (player.js is the session owner —
  // handlers are global, so per-player register/unregister would let a video
  // visit clobber music control). While a TV/movie player page is active it
  // installs window.hesperaMediaControl (media_player.js, cleared on teardown);
  // every media action dispatches there first — video page → video control
  // (FF/RW = the DVR scan), else the music behavior below. videoActive() guards
  // the music playbackState writes the same way, so the paused music engine
  // can't skew Chrome's play-vs-pause key translation while a video plays.
  const videoBridge = (action) => {
    const mc = window.hesperaMediaControl;
    return !!(mc && mc(action));
  };
  const videoActive = () => !!document.querySelector('video[data-media-kind]');

  // Playback state (lives here, not in any page).
  let tracks = []; // [{id, albumId, album, title, artist}]
  let queue = []; // indices into tracks
  let currentPos = -1;
  let currentTrackReported = false;
  let karaokeLines = [];
  let karaokeToken = 0; // guards a stale lyrics fetch from overwriting a newer track
  // Per-song lyrics overrides (track id → on/off), flipped by the transport's
  // Lyrics button. In-memory for the session: the global lyrics_enabled setting
  // stays the default for every track without an entry here. An explicit "on"
  // sends force=1 so one track can opt in even while the global default is off.
  const lyricsOverride = new Map();
  let view = null; // now-playing view DOM refs, when that page is shown
  // The autoload query of the queue currently loaded into the player. Persists
  // across Turbo navigations (this script loads once in the shell), so returning
  // to /music/player — via the back button, a restoration visit, or a cached
  // preview re-render — doesn't reload-and-restart a queue that's already
  // playing. Only a *different* play request (new params) re-loads. Cleared on
  // stop so the same queue can be started again afterwards.
  let loadedAutoload = null;

  const curTime = () => audio.currentTime || 0;
  const curDur = () => audio.duration;
  const curPaused = () => audio.paused;
  const seekTo = (s) => {
    audio.currentTime = s;
  };
  const doPlay = () => {
    audio.play().catch(() => {});
  };
  const doPause = () => {
    audio.pause();
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
    if (!t.id) { // defensive: a track with no local id has nothing to log
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

  // setNoLyrics toggles the layout state: when on, the lyrics card is hidden and
  // the cover/info expand into the freed space (see .player-now.no-lyrics in app.css).
  const setNoLyrics = (on) => {
    if (view && view.main) view.main.classList.toggle('no-lyrics', on);
  };

  // Effective lyrics state for a track: its per-song override when set, else
  // the global default.
  const lyricsOnFor = (t) =>
    t && t.id && lyricsOverride.has(t.id) ? lyricsOverride.get(t.id) : !!(view && view.lyricsEnabled);

  // Reflect the current track's lyrics state on the transport's toggle button.
  const updateLyricsBtn = () => {
    if (!view || !view.lyricsBtn) return;
    const t = currentTrack();
    const on = !!(t && t.id) && lyricsOnFor(t);
    view.lyricsBtn.classList.toggle('is-on', on);
    view.lyricsBtn.setAttribute('aria-pressed', on ? 'true' : 'false');
  };

  const loadKaraokeForTrack = (t) => {
    karaokeLines = [];
    const token = ++karaokeToken;
    if (!view) return;
    updateLyricsBtn();
    // Lyrics off for this track (per-song override, else the global default),
    // or a track with no local id (nothing cached) → hide the card so the
    // cover/info expand.
    if (!t.id || !lyricsOnFor(t)) {
      setNoLyrics(true);
      return;
    }
    // Keep the card hidden until synced lyrics are confirmed to exist — reveal it
    // only when the fetch returns some (setNoLyrics(false) below). This avoids
    // flashing an empty "Loading…" card for the many tracks with no synced lyrics.
    setNoLyrics(true);
    view.karaokeCurrent.textContent = '';
    view.karaokeNext.textContent = '';
    fetch('/music/lyrics/fetch', {
      method: 'POST',
      headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
      // An explicit per-song "on" carries force=1 past the endpoint's global
      // lyrics_enabled gate — a deliberate user gesture, not an automatic fetch.
      body: 'track_id=' + encodeURIComponent(t.id) + (lyricsOverride.get(t.id) === true ? '&force=1' : ''),
    })
      .then((r) => r.json())
      .then((payload) => {
        if (token !== karaokeToken) return; // a newer track took over
        const synced = ((((payload && payload.data) || {}).synced_lyrics) || '').trim();
        if (synced) {
          karaokeLines = parseSyncedLyrics(synced);
          setNoLyrics(false);
          renderKaraokeAt(audio.currentTime || 0);
        } else {
          setNoLyrics(true); // no synced lyrics to scroll → hide the card, cover expands
        }
      })
      .catch(() => {
        if (token === karaokeToken) setNoLyrics(true);
      });
  };

  // --- Header cluster ---
  // Show the glyph for the action a press performs: pause while playing, play
  // while paused. The now-playing chip and the now-playing page's transport toggle
  // share the .np-paused convention, so one reflect point drives both.
  const reflectPaused = () => {
    const paused = curPaused();
    if (npCluster) npCluster.classList.toggle('np-paused', paused);
    if (view && view.toggleBtn) view.toggleBtn.classList.toggle('np-paused', paused);
  };
  const updateHeader = () => {
    const t = currentTrack();
    if (!npCluster) return;
    if (!t) {
      npCluster.classList.add('hidden');
      return;
    }
    npCluster.classList.remove('hidden');
    if (npTitle) npTitle.textContent = t.artist ? t.title + ' — ' + t.artist : t.title;
    reflectPaused();
  };

  // Stop playback entirely and dismiss the now-playing cluster (the X control).
  const stopPlayback = () => {
    reportCurrentTrack(false);
    audio.pause();
    audio.removeAttribute('src');
    audio.load(); // detach the source so it can't resume
    tracks = [];
    queue = [];
    currentPos = -1;
    currentTrackReported = true;
    loadedAutoload = null; // a fresh start after stop should autoload again
    if (hasMediaSession) {
      try {
        navigator.mediaSession.metadata = null;
        // Don't clobber an active video player's state (its keys, its state).
        if (!videoActive()) navigator.mediaSession.playbackState = 'none';
      } catch (_) {}
    }
    updateHeader(); // currentTrack() is null -> hides #np-cluster
    renderView(); // empty state if the now-playing page is open
  };

  // --- Volume leveling --- each queue track carries gainDb (server-computed
  // from its measured LUFS toward the -18 target; 0 until analyzed). A Web
  // Audio GainNode on the permanent element applies it per track — the
  // media_player.js boost pattern: createMediaElementSource once, and the
  // whole graph is source→gain→destination from creation, so a suspended
  // context passes audio through untouched rather than silencing it.
  let levelCtx = null, levelGain = null;
  const levelSetup = () => {
    const AC = window.AudioContext || window.webkitAudioContext;
    if (!AC || levelCtx) return;
    try {
      levelCtx = new AC();
      const srcNode = levelCtx.createMediaElementSource(audio);
      levelGain = levelCtx.createGain();
      srcNode.connect(levelGain);
      levelGain.connect(levelCtx.destination);
      levelCtx.addEventListener('statechange', applyLevelGain);
    } catch (e) { levelCtx = null; levelGain = null; }
  };
  function applyLevelGain() {
    if (!levelGain || currentPos < 0 || currentPos >= queue.length) return;
    const db = (tracks[queue[currentPos]] || {}).gainDb || 0;
    levelGain.gain.value = Math.pow(10, db / 20);
  }

  // --- Near-gapless --- warm the next track's stream into the browser cache
  // while the current one plays, so the src-swap at 'ended' doesn't wait on a
  // cold request. (True sample-accurate gapless would need Web Audio buffer
  // scheduling — a different player; this removes the network share of the gap.)
  let preloader = null;
  const preloadNext = () => {
    const nextIdx = currentPos + 1;
    if (nextIdx >= queue.length) { preloader = null; return; }
    const t = tracks[queue[nextIdx]];
    if (!t) return;
    try {
      preloader = new Audio();
      preloader.preload = 'auto';
      preloader.src = '/stream/track/' + t.id;
    } catch (e) { preloader = null; }
  };

  // --- Core transport ---
  const playAt = (pos) => {
    if (pos < 0 || pos >= queue.length) return;
    reportCurrentTrack(false);
    currentPos = pos;
    currentTrackReported = false;
    const t = tracks[queue[currentPos]];
    audio.src = '/stream/track/' + t.id;
    audio.play().catch(() => {});
    levelSetup();
    applyLevelGain();
    preloadNext();
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
        // Resolve a start position: by track id, else by title+artist.
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
          // Start at the song but keep earlier ones queued as "previous".
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

  // The playlist dropdown (native <details>) closes on any pick, on Back (whose
  // hidden [data-couch-dismiss] control lives in the panel), and on an outside
  // click. It never needs opening from JS — the summary does that natively.
  const closePlaylistMenu = () => {
    if (view && view.playlistMenu) view.playlistMenu.open = false;
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

  // Fill one of the now-playing metadata lines, as a link when there's somewhere
  // to go. textContent only — the queue's strings are user data, never markup.
  const setMetaLine = (host, text, href) => {
    if (!host) return;
    host.textContent = '';
    if (!text) return;
    if (!href) {
      host.textContent = text;
      return;
    }
    const a = document.createElement('a');
    a.href = href;
    a.className = 'player-meta-link';
    a.textContent = text;
    host.appendChild(a);
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
    // Keep the transport's Add-to-playlist button pointed at the current track
    // (playlist_picker.js reads the data-track-id like an album-row button).
    if (view.addBtn) view.addBtn.setAttribute('data-track-id', String(t.id || 0));
    view.trackTitle.textContent = t.title || '';
    // Artist and album are links to their pages when the queue carries an id
    // (Turbo navigates; the permanent <audio> keeps playing). A queue row with
    // no id — an album-less track — degrades to plain text.
    setMetaLine(view.artist, (t.artist || '').trim(), t.artistId ? '/music/artist/' + t.artistId : '');
    setMetaLine(view.albumTitle, t.album || '', t.albumId ? '/music/album/' + t.albumId : '');
    delete view.coverImg.dataset.fallbackApplied;
    // Guard an album-less track (albumId 0) — never build /art/album/0.
    const coverSrc = t.albumId ? '/art/album/' + t.albumId : '';
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
    reflectPaused(); // the transport toggle was just (re)shown — match its glyph
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
    // The floating Back control is overlay chrome too — one idle system.
    if (view.backBtn) {
      view.backBtn.classList.toggle('collapsed', c);
      view.backBtn.setAttribute('aria-hidden', String(c));
    }
  };
  const idleCollapseTransport = () => {
    if (!view || !view.transport) return;
    // Stay open while paused, mid seek-drag, hovering the bar, or a control in it
    // is keyboard-focused (couch nav) — the same guards as the TV overlay.
    const ae = document.activeElement;
    if (curPaused() || view.seeking || transportHover ||
        (view.playlistMenu && view.playlistMenu.open) || // an open dropdown would slide away with the bar
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
      artist: $('player-artist'),
      seek: $('player-seek'),
      timeLabel: $('player-time'),
      seeking: false,
      karaoke: $('player-karaoke'),
      karaokeCurrent: $('player-karaoke-current'),
      karaokeNext: $('player-karaoke-next'),
      lyricsEnabled: page.dataset.lyricsEnabled === '1',
      lyricsBtn: $('player-lyrics-btn'),
      playlistMenu: $('player-playlist-menu'),
      playlistOpenBtn: $('playlist-open-btn'),
      playlistCloseBtn: $('playlist-close-btn'),
      playlistDrawer: $('playlist-drawer'),
      playlistScrim: $('playlist-scrim'),
      playlistList: $('playlist-list'),
      transport: $('player-transport'),
      backBtn: $('player-back'),
      addBtn: $('player-add-btn'),
      toggleBtn: $('player-toggle-btn'),
    };

    // Start with the card hidden (cover expanded) always — loadKaraokeForTrack
    // reveals it only once a track's synced lyrics are confirmed to exist, so it
    // never appears empty (whether lyrics are off, or on but the track has none).
    if (view.main) view.main.classList.add('no-lyrics');

    if (view.lyricsBtn) {
      view.lyricsBtn.addEventListener('click', () => {
        const t = currentTrack();
        if (!t || !t.id) return;
        lyricsOverride.set(t.id, !lyricsOnFor(t));
        loadKaraokeForTrack(t); // re-evaluates: fetch+reveal on, or hide off
      });
      updateLyricsBtn();
    }

    $('player-prev-btn').addEventListener('click', playPrev);
    $('player-rewind-btn').addEventListener('click', () => {
      seekTo(Math.max(0, curTime() - 10));
    });
    view.toggleBtn.addEventListener('click', toggle);
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
    // Any pick inside the dropdown's panel closes it: Add/Save hand off to the
    // picker modal (playlist_picker.js, delegated on the document), Show playlist
    // opens the drawer below, and the hidden dismiss button is Back's target.
    if (view.playlistMenu)
      view.playlistMenu.addEventListener('click', (e) => {
        if (e.target.closest && e.target.closest('.player-menu-panel')) closePlaylistMenu();
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
  audio.addEventListener('ended', () => {
    reportCurrentTrack(true);
    playNext();
  });
  audio.addEventListener('timeupdate', () => {
    renderKaraokeAt(audio.currentTime || 0);
    updateSeek();
  });
  audio.addEventListener('durationchange', () => {
    updateSeek();
  });
  audio.addEventListener('play', () => {
    updateHeader();
    if (hasMediaSession && !videoActive()) navigator.mediaSession.playbackState = 'playing';
  });
  audio.addEventListener('pause', () => {
    updateHeader();
    // On a video page the video owns playbackState — the music pausing (which
    // starting a video does) must not flip it to 'paused' under the video.
    if (hasMediaSession && !videoActive()) navigator.mediaSession.playbackState = 'paused';
  });

  if (npToggle) npToggle.addEventListener('click', toggle);
  {
    const npClose = document.getElementById('np-close');
    if (npClose) npClose.addEventListener('click', stopPlayback);
  }

  if (hasMediaSession) {
    // Each handler defers to an active video player first (videoBridge above).
    const setHandler = (action, handler) => {
      try {
        navigator.mediaSession.setActionHandler(action, (d) => {
          if (videoBridge(action)) return;
          handler(d);
        });
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

  // Keydown fallback for media keys delivered as real key events (Chrome
  // normally consumes them for the Media Session above; other environments —
  // or Chrome with hardware-media-key handling off — deliver keydown instead).
  // Same dispatch: an active video player first, else the music engine.
  const MEDIA_KEY_ACTIONS = {
    MediaPlayPause: 'playpause',
    MediaPlay: 'play',
    MediaPause: 'pause',
    MediaStop: 'pause',
    MediaFastForward: 'seekforward',
    MediaRewind: 'seekbackward',
    MediaTrackNext: 'nexttrack',
    MediaTrackPrevious: 'previoustrack',
  };
  document.addEventListener('keydown', (e) => {
    const action = MEDIA_KEY_ACTIONS[e.key];
    if (!action) return;
    e.preventDefault();
    if (videoBridge(action)) return;
    switch (action) {
      case 'playpause': toggle(); break;
      case 'play': doPlay(); break;
      case 'pause': doPause(); break;
      case 'seekforward': seekTo(curTime() + 10); break;
      case 'seekbackward': seekTo(Math.max(0, curTime() - 10)); break;
      case 'nexttrack': playNext(); break;
      case 'previoustrack': playPrev(); break;
    }
  });

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

  // If any other media starts (e.g. a TV video), pause the music so they don't
  // play over each other. 'play' doesn't bubble, so listen in the capture phase.
  document.addEventListener(
    'play',
    (e) => {
      const el = e.target;
      if (el !== audio && (el.tagName === 'VIDEO' || el.tagName === 'AUDIO')) {
        if (!audio.paused) audio.pause();
      }
    },
    true,
  );

  // A click anywhere outside the playlist dropdown closes it (bound once,
  // document-level; a no-op when it's closed or the view isn't on the page).
  document.addEventListener('click', (e) => {
    if (!view || !view.playlistMenu || !view.playlistMenu.open) return;
    if (!view.playlistMenu.contains(e.target)) closePlaylistMenu();
  });

  // Reveal the auto-collapsing transport on any activity (bound once, document-
  // level; a no-op when the now-playing view isn't on the page).
  ['pointermove', 'pointerdown', 'keydown', 'touchstart'].forEach((ev) =>
    document.addEventListener(ev, revealTransport, { passive: true }));

  // Final listen report when the tab actually closes (rare under Turbo).
  window.addEventListener('beforeunload', () => reportCurrentTrack(false, { beacon: true }));

  // The queue snapshot for "Save as playlist" (playlist_picker.js) — the
  // current play order's track ids. A tiny read-only bridge; this script is
  // the queue's single owner.
  window.hesperaPlayerQueueIDs = () => queue.map((i) => tracks[i].id).filter((id) => id > 0);

  // Re-bind the now-playing view + refresh the header on every Turbo visit
  // (turbo:load fires on the initial load too).
  document.addEventListener('turbo:load', () => {
    bindView();
    updateHeader();
  });

})();
