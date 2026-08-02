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
  async function play(source, id, shuffle) {
    try {
      await api('/api/play', {
        method: 'POST',
        body: JSON.stringify({ source: source, id: id || 0, shuffle: !!shuffle }),
      });
      refresh(); // don't wait for the next poll tick to show what started
    } catch (e) {
      toast(e.message, true);
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

  async function refresh() {
    try {
      renderNow(await api('/api/state'));
    } catch (_) {
      // A failed poll is normal when the box is briefly unreachable; the next
      // tick recovers and an error toast every 2s would be unusable.
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
      loadPlaylists();
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

    $('prev').addEventListener('click', () => action('/api/prev'));
    $('next').addEventListener('click', () => action('/api/next'));
    $('stop').addEventListener('click', () => action('/api/stop'));

    loadPlaylists();
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
