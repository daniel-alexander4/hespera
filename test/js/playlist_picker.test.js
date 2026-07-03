// Tests for web/static/playlist_picker.js — the add-to-playlist overlay.
// jsdom drives the real controller: open-on-[data-playlist-add], playlist list
// fetch + pick → add-track POST, create-with-first-track, and the save-queue
// mode fed by the player.js window bridge.

const { test } = require('node:test');
const assert = require('node:assert');
const { loadController, makeFetch, flush } = require('./harness');

const PAGE = `
<ol class="tracks">
  <li class="track"><button data-playlist-add data-track-id="21">+</button></li>
</ol>
<button id="player-save-btn" data-playlist-save>Save</button>
<div id="plpick-modal" class="playlist-modal hidden" data-couch-overlay>
  <div class="playlist-panel">
    <div class="plpick-head">
      <strong id="plpick-title">Add to playlist</strong>
      <button id="plpick-close" data-couch-dismiss>Close</button>
    </div>
    <ol id="plpick-list" class="tracks"></ol>
    <form id="plpick-new"><input id="plpick-name" type="text" /><button type="submit">Create</button></form>
    <p id="plpick-status"></p>
  </div>
</div>`;

function boot(routes) {
  const env = loadController('playlist_picker.js', {
    html: `<!DOCTYPE html><html><body>${PAGE}</body></html>`,
    url: 'http://localhost/music/album/3',
    stubs: {
      fetch: makeFetch(
        Object.assign(
          {
            '/music/playlists': { playlists: [{ id: 1, name: 'Road Trip', count: 4 }, { id: 2, name: 'Chill', count: 9 }] },
            '/music/playlist/add-track': { ok: true, added: true },
            '/music/playlist/create': { id: 3, name: 'New One', count: 1 },
          },
          routes || {},
        ),
      ),
    },
  });
  return env;
}

test('clicking a track row + opens the picker and lists playlists', async () => {
  const env = boot();
  env.document.querySelector('[data-playlist-add]').click();
  await flush();
  const modal = env.document.getElementById('plpick-modal');
  assert.ok(!modal.classList.contains('hidden'), 'modal opened');
  const rows = env.document.querySelectorAll('#plpick-list button[data-playlist-id]');
  assert.strictEqual(rows.length, 2);
  assert.match(rows[0].textContent, /Road Trip \(4\)/);
});

test('picking a playlist POSTs add-track with the track id', async () => {
  const env = boot();
  env.document.querySelector('[data-playlist-add]').click();
  await flush();
  env.document.querySelector('#plpick-list button[data-playlist-id="2"]').click();
  await flush();
  const call = env.fetch.calls.find((c) => c.url.indexOf('/music/playlist/add-track') >= 0);
  assert.ok(call, 'add-track POSTed');
  const body = String(call.opts.body);
  assert.match(body, /playlist_id=2/);
  assert.match(body, /track_id=21/);
  assert.match(env.document.getElementById('plpick-status').textContent, /Added/);
});

test('the New playlist form creates with the picked track', async () => {
  const env = boot();
  env.document.querySelector('[data-playlist-add]').click();
  await flush();
  env.document.getElementById('plpick-name').value = 'New One';
  env.document.getElementById('plpick-new').dispatchEvent(new env.window.Event('submit', { bubbles: true, cancelable: true }));
  await flush();
  const call = env.fetch.calls.find((c) => c.url.indexOf('/music/playlist/create') >= 0);
  assert.ok(call, 'create POSTed');
  const body = String(call.opts.body);
  assert.match(body, /name=New\+One/);
  assert.match(body, /track_id=21/);
});

test('save mode snapshots the player queue via the window bridge', async () => {
  const env = boot();
  env.window.hesperaPlayerQueueIDs = () => [5, 6, 7];
  env.document.getElementById('player-save-btn').click();
  await flush();
  assert.match(env.document.getElementById('plpick-title').textContent, /Save queue/);
  assert.strictEqual(env.document.querySelectorAll('#plpick-list button').length, 0, 'no list in save mode');
  env.document.getElementById('plpick-name').value = 'Snapshot';
  env.document.getElementById('plpick-new').dispatchEvent(new env.window.Event('submit', { bubbles: true, cancelable: true }));
  await flush();
  const call = env.fetch.calls.find((c) => c.url.indexOf('/music/playlist/create') >= 0);
  assert.ok(call, 'create POSTed');
  assert.match(String(call.opts.body), /track_ids=5%2C6%2C7/);
});

test('an empty queue makes Save a no-op; Close dismisses', async () => {
  const env = boot();
  env.window.hesperaPlayerQueueIDs = () => [];
  env.document.getElementById('player-save-btn').click();
  await flush();
  const modal = env.document.getElementById('plpick-modal');
  assert.ok(modal.classList.contains('hidden'), 'stays closed with nothing playing');

  env.document.querySelector('[data-playlist-add]').click();
  await flush();
  assert.ok(!modal.classList.contains('hidden'));
  env.document.getElementById('plpick-close').click();
  assert.ok(modal.classList.contains('hidden'), 'Close dismisses');
});
