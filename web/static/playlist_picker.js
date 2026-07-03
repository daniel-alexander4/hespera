// playlist_picker.js — the add-to-playlist overlay (#plpick-modal in the
// layout shell). Two modes, one panel:
//   add  — opened by any [data-playlist-add] carrying data-track-id (album
//          track rows; the now-playing transport's Add button, whose track id
//          player.js keeps current): pick a playlist → POST add-track (fetch,
//          idempotent), or type a name → create-with-first-track.
//   save — opened by [data-playlist-save] (now-playing "Save"): snapshots the
//          current queue (player.js exposes window.hesperaPlayerQueueIDs) into
//          a new playlist via the same create endpoint.
// Same couch-overlay pattern as the search palette: the modal lives in the
// shell (re-rendered closed per Turbo swap), the document-level listeners are
// bound exactly once (window-guarded) and query the live DOM per event.
(() => {
  if (window.__hesperaPlaylistPicker) return;
  window.__hesperaPlaylistPicker = true;

  const $ = (id) => document.getElementById(id);
  const modal = () => $('plpick-modal');

  // Current mode: {trackId} for add, {queueIds} for save.
  let mode = null;
  let closeTimer = null;

  const setStatus = (text) => {
    const s = $('plpick-status');
    if (s) s.textContent = text || '';
  };

  const close = () => {
    const m = modal();
    if (m) m.classList.add('hidden');
    mode = null;
    clearTimeout(closeTimer);
  };

  const closeSoon = () => {
    clearTimeout(closeTimer);
    closeTimer = setTimeout(close, 700);
  };

  const renderPlaylists = (playlists) => {
    const list = $('plpick-list');
    if (!list) return;
    list.textContent = '';
    for (const p of playlists || []) {
      const li = document.createElement('li');
      li.className = 'track playlist-item';
      const btn = document.createElement('button');
      btn.type = 'button';
      btn.className = 'track-play-btn';
      btn.textContent = p.name + ' (' + p.count + ')';
      btn.dataset.playlistId = String(p.id);
      li.appendChild(btn);
      list.appendChild(li);
    }
  };

  const open = (nextMode) => {
    const m = modal();
    if (!m) return;
    mode = nextMode;
    clearTimeout(closeTimer);
    m.classList.remove('hidden');
    setStatus('');
    const title = $('plpick-title');
    const list = $('plpick-list');
    const name = $('plpick-name');
    if (name) name.value = '';
    if (mode.queueIds) {
      // Save mode: no existing-playlist list — it's a snapshot into a new one.
      if (title) title.textContent = 'Save queue as playlist';
      if (list) list.textContent = '';
      if (name) name.focus();
      return;
    }
    if (title) title.textContent = 'Add to playlist';
    renderPlaylists([]);
    fetch('/music/playlists', { headers: { Accept: 'application/json' } })
      .then((r) => (r.ok ? r.json() : Promise.reject(new Error('playlists ' + r.status))))
      .then((data) => {
        if (!mode || mode.queueIds) return; // closed or switched meanwhile
        renderPlaylists((data && data.playlists) || []);
        const first = $('plpick-list') && $('plpick-list').querySelector('button');
        if (first) first.focus();
        else if (name) name.focus();
      })
      .catch(() => setStatus('Could not load playlists.'));
  };

  const postForm = (url, params) =>
    fetch(url, { method: 'POST', body: new URLSearchParams(params) }).then((r) =>
      r.ok ? r.json() : Promise.reject(new Error(url + ' ' + r.status)),
    );

  document.addEventListener(
    'click',
    (e) => {
      const t = e.target;
      if (!t || !t.closest) return;

      const addBtn = t.closest('[data-playlist-add]');
      if (addBtn) {
        const trackId = parseInt(addBtn.getAttribute('data-track-id') || '0', 10);
        if (trackId > 0) open({ trackId });
        return;
      }

      const saveBtn = t.closest('[data-playlist-save]');
      if (saveBtn) {
        const ids = (window.hesperaPlayerQueueIDs && window.hesperaPlayerQueueIDs()) || [];
        if (!ids.length) return;
        open({ queueIds: ids });
        return;
      }

      const m = modal();
      if (!m || m.classList.contains('hidden')) return;

      if (t.closest('#plpick-close')) {
        close();
        return;
      }

      const row = t.closest('#plpick-list button[data-playlist-id]');
      if (row && mode && mode.trackId) {
        const playlistId = parseInt(row.dataset.playlistId, 10);
        postForm('/music/playlist/add-track', {
          playlist_id: String(playlistId),
          track_id: String(mode.trackId),
        })
          .then((res) => {
            setStatus(res && res.added ? 'Added ✓' : 'Already in that playlist');
            closeSoon();
          })
          .catch(() => setStatus('Could not add — try again.'));
      }
    },
    true,
  );

  document.addEventListener('submit', (e) => {
    const form = e.target;
    if (!form || form.id !== 'plpick-new') return;
    e.preventDefault();
    if (!mode) return;
    const name = ($('plpick-name') && $('plpick-name').value.trim()) || '';
    if (!name) return;
    const params = { name };
    if (mode.queueIds) params.track_ids = mode.queueIds.join(',');
    else params.track_id = String(mode.trackId);
    postForm('/music/playlist/create', params)
      .then(() => {
        setStatus('Created “' + name + '” ✓');
        closeSoon();
      })
      .catch(() => setStatus('Could not create — try again.'));
  });
})();
