// Tests for web/static/subtabs.js — the shared sub-tab switcher, including the
// per-path tab memory (localStorage `iso_subtab:<pathname>`) that restores the
// last-clicked tab on the next visit.

const { test } = require('node:test');
const assert = require('node:assert');
const { loadController } = require('./harness');

const PAGE = `
<div class="subtabs">
  <button class="subtab active" data-tab="recent">Recent</button>
  <button class="subtab" data-tab="artists">Artists</button>
</div>
<div id="tab-recent" class="subtab-panel active">recent stuff</div>
<div id="tab-artists" class="subtab-panel">artist grid</div>`;

function boot({ url = 'http://localhost/music', storage = {} } = {}) {
  const env = loadController('subtabs.js', {
    html: `<!DOCTYPE html><html><body>${PAGE}</body></html>`,
    url,
    storage,
  });
  env.document.dispatchEvent(new env.window.Event('turbo:load', { bubbles: true }));
  return env;
}

const activeTab = (env) => env.document.querySelector('.subtab.active').getAttribute('data-tab');
const activePanel = (env) => env.document.querySelector('.subtab-panel.active').id;

test('clicking a tab switches panels and remembers it per path', () => {
  const env = boot();
  env.document.querySelector('[data-tab="artists"]').click();
  assert.strictEqual(activeTab(env), 'artists');
  assert.strictEqual(activePanel(env), 'tab-artists');
  assert.strictEqual(env.window.localStorage.getItem('iso_subtab:/music'), 'artists');
});

test('a remembered tab is restored on the next visit', () => {
  const env = boot({ storage: { 'iso_subtab:/music': 'artists' } });
  assert.strictEqual(activeTab(env), 'artists');
  assert.strictEqual(activePanel(env), 'tab-artists');
});

test('a ?page= deep link keeps the server-marked tab (no restore)', () => {
  const env = boot({ url: 'http://localhost/music?page=2', storage: { 'iso_subtab:/music': 'artists' } });
  assert.strictEqual(activeTab(env), 'recent');
});

test('a stored tab that no longer exists is ignored', () => {
  const env = boot({ storage: { 'iso_subtab:/music': 'playlists' } });
  assert.strictEqual(activeTab(env), 'recent');
});

test('the memory is per path — another page is unaffected', () => {
  const env = boot({ url: 'http://localhost/tv', storage: { 'iso_subtab:/music': 'artists' } });
  assert.strictEqual(activeTab(env), 'recent');
});

test('a ?tab= URL (server-tabbed page) keeps the server-marked tab (no restore)', () => {
  const env = boot({ url: 'http://localhost/music?tab=videos', storage: { 'iso_subtab:/music': 'artists' } });
  assert.strictEqual(activeTab(env), 'recent');
});

test('clicking a data-tab-less subtab (server-tab link) stores nothing', () => {
  const env = boot();
  const link = env.document.createElement('a');
  link.className = 'subtab';
  env.document.querySelector('.subtabs').appendChild(link);
  env.document.dispatchEvent(new env.window.Event('turbo:load', { bubbles: true }));
  link.click();
  assert.strictEqual(env.window.localStorage.getItem('iso_subtab:/music'), null, 'no "null" garbage written');
});
