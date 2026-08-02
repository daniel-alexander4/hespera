// app.js — the phone remote for hesplay.
//
// Every button is one POST to the binary serving this page; the audio comes out
// of that box's speakers, never the phone's. So there is no <audio> element
// here, no Media Session, and no background-playback problem to solve: locking
// the phone stops nothing, because the phone was never playing.
//
// State is polled rather than pushed. A websocket would be tidier, but polling
// survives the phone sleeping and waking with no reconnect logic, and the whole
// payload is one small JSON object.
(function () {
  'use strict';

  const $ = (id) => document.getElementById(id);
  const POLL_MS = 2000;

  // --- plumbing ----------------------------------------------------------

  async function api(path, opts) {
    const res = await fetch(path, Object.assign({ headers: { 'Content-Type': 'application/json' } }, opts));
    let body = null;
    try { body = await res.json(); } catch (_) { /* a proxy error page, not JSON */ }
    if (!res.ok || (body && body.ok === false)) {
      throw new Error((body && body.message) || ('HTTP ' + res.status));
    }
    return body || {};
  }

  let toastTimer = 0;
  function toast(msg, isErr) {
    const el = $('toast');
    el.textContent = msg;
    el.classList.toggle('err', !!isErr);
    el.hidden = false;
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => { el.hidden = true; }, 3200);
  }

  // --- playback ----------------------------------------------------------

  // id addresses a queue directly; name goes through the server's search. The
  // app always knows the id, so it always sends id — a playlist called "2016"
  // must not be resolved by searching for its own name.
  async function play(source, id, shuffle, startAt) {
    try {
      await api('/api/play', {
        method: 'POST',
        body: JSON.stringify({
          source: source, id: id || 0, shuffle: !!shuffle, startAt: startAt || 0,
        }),
      });
      refresh(); // don't wait for the next poll tick to show what started
    } catch (e) {
      toast(e.message, true);
    }
  }

  // Jump to a tapped row. Optimistic: move the highlight straight away so the
  // tap feels immediate, then let the next poll confirm (or correct) it.
  async function jump(index) {
    lastQueueKey = ''; // force a re-render even if the poll lands identical rows
    try {
      await api('/api/jump', { method: 'POST', body: JSON.stringify({ index: index }) });
      refresh();
    } catch (e) {
      toast(e.message, true);
    }
  }

  // Pause/resume the running engine. Explicit rather than a bare toggle so a
  // retry after a dropped response cannot flip it back the other way.
  async function setPaused(want) {
    try {
      const r = await api('/api/pause', { method: 'POST', body: JSON.stringify({ paused: want }) });
      applyPaused(!!r.paused);
      refresh();
    } catch (e) {
      toast(e.message, true);
    }
  }

  let isPaused = false;
  function applyPaused(p) {
    isPaused = p;
    const b = $('pause');
    b.textContent = p ? '▶' : '⏸';
    b.setAttribute('aria-label', p ? 'Resume' : 'Pause');
    const row = document.querySelector('.q-row.is-now');
    if (row) {
      row.querySelector('.q-n').textContent = p ? '⏸' : '▶';
      row.setAttribute('aria-label', (p ? 'Resume ' : 'Pause ') + (row.dataset.title || ''));
    }
  }

  async function action(path) {
    try {
      await api(path, { method: 'POST' });
      refresh();
    } catch (e) {
      toast(e.message, true);
    }
  }

  // --- rendering ---------------------------------------------------------

  // textContent only, never innerHTML: playlist names are library data and a
  // name containing markup must render as that name, not as markup.
  function playlistRow(p) {
    const wrap = document.createElement('div');
    wrap.className = 'pl';

    const name = document.createElement('div');
    name.className = 'name';
    const b = document.createElement('b');
    b.textContent = p.name;
    const count = document.createElement('span');
    count.className = 'count';
    count.textContent = p.count === 1 ? '1 track' : p.count + ' tracks';
    name.append(b, count);

    const playBtn = document.createElement('button');
    playBtn.className = 'btn go';
    playBtn.type = 'button';
    playBtn.textContent = '▶';
    playBtn.setAttribute('aria-label', 'Play ' + p.name);
    playBtn.addEventListener('click', () => play('playlist', p.id, false));

    const shufBtn = document.createElement('button');
    shufBtn.className = 'btn go';
    shufBtn.type = 'button';
    shufBtn.textContent = '🔀';
    shufBtn.setAttribute('aria-label', 'Shuffle ' + p.name);
    shufBtn.addEventListener('click', () => play('playlist', p.id, true));

    wrap.append(name, playBtn, shufBtn);
    return wrap;
  }

  async function loadPlaylists() {
    const box = $('playlists');
    try {
      const r = await api('/api/playlists');
      const rows = r.playlists || [];
      box.textContent = '';
      rows.forEach((p) => box.appendChild(playlistRow(p)));
      $('pl-empty').hidden = rows.length > 0;
    } catch (e) {
      box.textContent = '';
      $('pl-empty').hidden = false;
      $('pl-empty').textContent = 'Could not reach the server: ' + e.message;
    }
  }

  let lastTrackID = -1;
  function renderNow(st) {
    const now = $('now');
    if (!st.playing || !st.now || !st.now.title) {
      now.hidden = true;
      lastTrackID = -1;
      return;
    }
    const n = st.now;
    now.hidden = false;
    // canPause is false on an ffplay box, which has no IPC to pause through —
    // hide the control rather than offer one that can only fail.
    $('pause').hidden = !st.canPause;
    applyPaused(!!st.paused);
    $('np-title').textContent = n.title;
    const bits = [n.artist, n.album].filter(Boolean).join(' — ');
    const pos = n.total > 1 ? ' (' + n.index + '/' + n.total + ')' : '';
    $('np-sub').textContent = bits + pos;

    // Only swap the <img> when the track actually changed: reassigning src on
    // every 2s poll would restart the fetch and flicker the artwork.
    if (n.id !== lastTrackID) {
      lastTrackID = n.id;
      const art = $('art');
      if (n.albumId > 0) {
        art.src = '/art/album/' + n.albumId;
        art.hidden = false;
      } else {
        art.removeAttribute('src'); // a track with no album has no art to show
        art.hidden = true;
      }
    }
  }

  // The queue window. Re-rendered only when the rows actually change, because
  // this runs on every 2s poll and rebuilding a 20-row list each tick would
  // fight the user's scroll position.
  let lastQueueKey = '';
  function renderQueue(st) {
    const sec = $('queue-sec');
    const rows = (st && st.queue) || [];
    if (!st.playing || rows.length === 0) {
      sec.hidden = true;
      lastQueueKey = '';
      return;
    }
    sec.hidden = false;
    $('queue-h').textContent = st.now && st.now.queue
      ? st.now.queue + ' · ' + st.now.index + ' of ' + st.now.total
      : 'Playing';

    const key = rows.map((r) => r.index + (r.current ? '*' : '')).join(',');
    if (key === lastQueueKey) return;
    lastQueueKey = key;

    const list = $('queue');
    list.textContent = '';
    rows.forEach((r) => {
      const li = document.createElement('li');

      // A whole row is the touch target — a 44px-tall <button> spanning it,
      // rather than a tappable <li>, so it is reachable by keyboard and
      // announced as an action rather than as a list item.
      const btn = document.createElement('button');
      btn.type = 'button';
      btn.className = 'q-row' + (r.current ? ' is-now' : (st.now && r.index < st.now.index ? ' is-past' : ''));
      btn.setAttribute('aria-label', 'Play ' + r.title + ' by ' + r.artist);

      const n = document.createElement('span');
      n.className = 'q-n';
      n.textContent = r.current ? (isPaused ? '⏸' : '▶') : String(r.index);

      const t = document.createElement('span');
      t.className = 'q-t';
      t.textContent = r.title; // textContent: titles are library data, never markup
      const a = document.createElement('span');
      a.className = 'q-a';
      a.textContent = r.artist;
      t.appendChild(a);

      btn.append(n, t);
      btn.dataset.title = r.title;
      if (r.current) {
        // The playing row is the pause control. Tapping it to restart the track
        // read as a glitch, and leaving it inert wasted the one row you are
        // most likely to reach for.
        btn.setAttribute('aria-current', 'true');
        btn.addEventListener('click', () => setPaused(!isPaused));
      } else {
        btn.addEventListener('click', () => jump(r.index));
      }

      li.appendChild(btn);
      list.appendChild(li);
    });
  }

  async function refresh() {
    try {
      const st = await api('/api/state');
      renderNow(st);
      renderQueue(st);
    } catch (_) {
      // A failed poll is normal when the box is briefly unreachable; the next
      // tick recovers and an error toast every 2s would be unusable.
    }
  }

  // --- browse ------------------------------------------------------------
  //
  // A three-deep stack (browse → artist → album) rather than a router: there is
  // no URL to keep in sync, and Back only ever means "the screen I came from".

  const SCREENS = ['home', 'browse', 'artist', 'album'];
  let stack = ['home'];
  let current = { artistId: 0, artistName: '', albumId: 0, albumTitle: '' };

  function showScreen(name, title) {
    SCREENS.forEach((s) => {
      const el = s === 'home' ? document.querySelector('main:not([id])') : $('screen-' + s);
      if (el) el.hidden = s !== name;
    });
    $('bar-title').textContent = title || 'hesplay';
    $('back-btn').hidden = stack.length <= 1;
  }

  function push(name, title) { stack.push(name); showScreen(name, title); }
  function pop() {
    if (stack.length > 1) stack.pop();
    const top = stack[stack.length - 1];
    const titles = { home: 'hesplay', browse: 'Artists', artist: current.artistName, album: current.albumTitle };
    showScreen(top, titles[top]);
  }

  // One row: a label that opens the next level, plus play and shuffle for it.
  function itemRow(label, sub, onOpen, onPlay, onShuffle) {
    const wrap = document.createElement('div');
    wrap.className = 'row-item';

    const open = document.createElement('button');
    open.type = 'button';
    open.className = 'open';
    const b = document.createElement('b');
    b.textContent = label;
    open.appendChild(b);
    if (sub) {
      const s = document.createElement('span');
      s.className = 'sub';
      s.textContent = sub;
      open.appendChild(s);
    }
    open.addEventListener('click', onOpen);

    const p = document.createElement('button');
    p.type = 'button'; p.className = 'btn go'; p.textContent = '▶';
    p.setAttribute('aria-label', 'Play ' + label);
    p.addEventListener('click', onPlay);

    const s = document.createElement('button');
    s.type = 'button'; s.className = 'btn go'; s.textContent = '🔀';
    s.setAttribute('aria-label', 'Shuffle ' + label);
    s.addEventListener('click', onShuffle);

    wrap.append(open, p, s);
    return wrap;
  }

  const ALPHABET = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ'.split('').concat('#');

  // The grid is drawn immediately from the static alphabet, then refined once
  // the index answers — so the home screen never shows an empty hole while the
  // player reads the catalog, and a letter that turns out to hold nobody simply
  // goes dim a moment later.
  function renderLetterGrid(letters) {
    const strip = $('letters');
    strip.textContent = '';
    (letters || ALPHABET.map((l) => ({ letter: l, count: -1 }))).forEach((l) => {
      const b = document.createElement('button');
      b.type = 'button';
      b.textContent = l.letter;
      b.disabled = l.count === 0;
      if (l.count >= 0) b.title = l.count === 1 ? '1 artist' : l.count + ' artists';
      b.addEventListener('click', () => openLetter(l.letter));
      strip.appendChild(b);
    });
  }

  async function loadLetters() {
    renderLetterGrid(null);
    try {
      const r = await api('/api/letters');
      renderLetterGrid(r.letters);
      $('artists-h').textContent = 'Artists · ' + r.total;
    } catch (_) {
      // Leave the grid live: the letter tap will surface the real error, and a
      // dead grid on the home screen says less than one that tries.
    }
  }

  async function openLetter(letter) {
    push('browse', letter === '#' ? 'Artists · #' : 'Artists · ' + letter);
    const box = $('artists');
    box.textContent = '';
    $('browse-msg').classList.remove('err');
    $('browse-msg').textContent = 'Loading…';
    try {
      const r = await api('/api/artists?letter=' + encodeURIComponent(letter));
      if (!r.artists.length) {
        $('browse-msg').textContent = 'No artists under ' + letter + '.';
        return;
      }
      $('browse-msg').textContent = r.artists.length === 1 ? '1 artist' : r.artists.length + ' artists';
      r.artists.forEach((a) => box.appendChild(itemRow(
        a.name, null,
        () => openArtist(a.id, a.name),
        () => play('artist', a.id, false),
        () => play('artist', a.id, true),
      )));
    } catch (e) {
      $('browse-msg').classList.add('err');
      $('browse-msg').textContent = e.message;
    }
  }

  async function openArtist(id, name) {
    current.artistId = id; current.artistName = name;
    push('artist', name);
    const box = $('artist-albums');
    box.textContent = '';
    $('artist-albums-h').textContent = 'Albums';
    try {
      const r = await api('/api/artist?id=' + id);
      $('artist-albums-h').textContent = r.albums.length === 1 ? '1 album' : r.albums.length + ' albums';
      // "N by this artist", not "N tracks": the count is this artist's share of
      // the album, and on a compilation that is a fraction of it — The Rolling
      // Stones contribute 50 tracks to an "Essentials" album that holds 173.
      r.albums.forEach((al) => box.appendChild(itemRow(
        al.title, al.count + ' by this artist',
        () => openAlbum(al.id, al.title),
        () => play('album', al.id, false),
        () => play('album', al.id, true),
      )));
    } catch (e) {
      $('artist-albums-h').textContent = e.message;
    }
  }

  async function openAlbum(id, title) {
    current.albumId = id; current.albumTitle = title;
    push('album', title);
    const list = $('album-tracks');
    list.textContent = '';
    try {
      const r = await api('/api/album?id=' + id);
      r.tracks.forEach((t) => {
        const li = document.createElement('li');
        const btn = document.createElement('button');
        btn.type = 'button';
        btn.className = 'q-row';
        btn.setAttribute('aria-label', 'Play ' + t.title);
        const n = document.createElement('span');
        n.className = 'q-n';
        n.textContent = String(t.index);
        const tt = document.createElement('span');
        tt.className = 'q-t';
        tt.textContent = t.title;
        const ar = document.createElement('span');
        ar.className = 'q-a';
        ar.textContent = t.artist;
        tt.appendChild(ar);
        btn.append(n, tt);
        // Play the album from this song: the album queue, positioned. Keeps the
        // rest of the record queued behind it instead of playing one track and
        // falling silent.
        btn.addEventListener('click', () => play('album', id, false, t.index));
        li.appendChild(btn);
        list.appendChild(li);
      });
    } catch (e) {
      toast(e.message, true);
    }
  }

  // --- settings ----------------------------------------------------------

  async function loadServer() {
    try {
      const r = await api('/api/server');
      $('server').value = r.server || '';
    } catch (_) { /* leave the field empty; saving still works */ }
  }

  async function saveServer() {
    const msg = $('server-msg');
    const url = $('server').value.trim();
    msg.classList.remove('err');
    msg.textContent = 'Checking…';
    try {
      const r = await api('/api/server', { method: 'PUT', body: JSON.stringify({ server: url }) });
      $('server').value = r.server;
      msg.textContent = 'Connected to Hespera ' + (r.version || '');
      // A new server means a new library: rebuild both lists rather than
      // leaving the previous box's artists on screen.
      loadPlaylists();
      loadLetters();
    } catch (e) {
      msg.classList.add('err');
      msg.textContent = e.message;
    }
  }

  // --- boot --------------------------------------------------------------

  document.addEventListener('DOMContentLoaded', () => {
    $('settings-btn').addEventListener('click', () => {
      const p = $('settings');
      p.hidden = !p.hidden;
      $('settings-btn').setAttribute('aria-expanded', String(!p.hidden));
      if (!p.hidden) loadServer();
    });
    $('server-save').addEventListener('click', saveServer);

    document.querySelectorAll('#quick [data-source]').forEach((b) => {
      b.addEventListener('click', () => play(b.dataset.source, 0, b.dataset.shuffle === '1'));
    });

    $('back-btn').addEventListener('click', pop);
    document.querySelectorAll('[data-act]').forEach((b) => {
      b.addEventListener('click', () => {
        const a = b.dataset.act;
        if (a === 'artist-play') play('artist', current.artistId, false);
        else if (a === 'artist-shuffle') play('artist', current.artistId, true);
        // A mix is the server's own weighted draw, seed track first — so it
        // is NOT shuffled again here; that would throw the seed away.
        else if (a === 'artist-mix') play('mix', current.artistId, false);
        else if (a === 'album-play') play('album', current.albumId, false);
        else if (a === 'album-shuffle') play('album', current.albumId, true);
      });
    });

    $('pause').addEventListener('click', () => setPaused(!isPaused));
    $('prev').addEventListener('click', () => action('/api/prev'));
    $('next').addEventListener('click', () => action('/api/next'));
    $('stop').addEventListener('click', () => action('/api/stop'));

    loadPlaylists();
    loadLetters();
    refresh();
    setInterval(refresh, POLL_MS);
    // Coming back from a locked screen should show the truth immediately rather
    // than up to POLL_MS of stale now-playing.
    document.addEventListener('visibilitychange', () => { if (!document.hidden) refresh(); });

    if ('serviceWorker' in navigator) {
      navigator.serviceWorker.register('sw.js').catch(() => { /* http:// on some
        browsers refuses a worker; the app still works, it just won't install */ });
    }
  });
})();
