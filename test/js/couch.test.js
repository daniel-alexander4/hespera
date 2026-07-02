// Tests for web/static/couch.js — the 10-foot "couch mode" remote layer. jsdom
// drives the real IIFE through dispatched keydown events. These cover the Back
// button's semantic-parent navigation (read the breadcrumb / data-couch-parent,
// history fallback, typing guard, broadened keycodes). What jsdom can't model —
// the visible()/overlay-dismiss path and spatial arrow focus both need real
// layout (getBoundingClientRect) — stays in the Playwright/manual smoke.

const { test } = require('node:test');
const assert = require('node:assert');
const { loadController } = require('./harness');

// Boot couch.js in a fresh window. `couch` toggles <html data-couch>, `body` is
// the page content, and we stub Turbo.visit + spy history.back to observe where
// a Back press sends the user.
function boot({ body = '', url = 'http://localhost/music/album/1', couch = true } = {}) {
  const html = `<!DOCTYPE html><html${couch ? ' data-couch="1"' : ''}><body>${body}</body></html>`;
  const visited = [];
  const env = loadController('couch.js', {
    html,
    url,
    stubs: { Turbo: { visit: (u) => visited.push(u) } },
  });
  const backCalls = [];
  Object.defineProperty(env.window.history, 'back', { configurable: true, value: () => backCalls.push(1) });
  env.visited = visited;
  env.backCalls = backCalls;
  return env;
}

function pressKey(env, key, target) {
  const t = target || env.document;
  t.dispatchEvent(new env.window.KeyboardEvent('keydown', { key, bubbles: true, cancelable: true }));
}

const breadcrumb = (hrefs) =>
  `<nav class="breadcrumb"><ol>${hrefs.map((h) => `<li><a href="${h}">x</a></li>`).join('')}</ol></nav>`;

test('Back navigates to the breadcrumb\'s last crumb (immediate parent)', () => {
  const env = boot({ body: breadcrumb(['/', '/music', '/music/artist/1']) });
  pressKey(env, 'Escape');
  assert.deepStrictEqual(env.visited, ['/music/artist/1']);
  assert.strictEqual(env.backCalls.length, 0);
});

test('every broadened back keycode triggers the parent climb', () => {
  for (const key of ['Backspace', 'Escape', 'BrowserBack', 'GoBack']) {
    const env = boot({ body: breadcrumb(['/', '/music']) });
    pressKey(env, key);
    assert.deepStrictEqual(env.visited, ['/music'], `key ${key}`);
  }
});

test('a breadcrumb-less player page climbs to its data-couch-parent', () => {
  const env = boot({
    body: '<div class="tv-player-page" data-couch-parent="/tv/series/42"></div>',
    url: 'http://localhost/tv/player',
  });
  pressKey(env, 'Escape');
  assert.deepStrictEqual(env.visited, ['/tv/series/42']);
});

test('a page with no parent falls back to history.back()', () => {
  const env = boot({ body: '<h1>Home</h1>', url: 'http://localhost/' });
  pressKey(env, 'Escape');
  assert.strictEqual(env.visited.length, 0);
  assert.strictEqual(env.backCalls.length, 1);
});

test('Backspace inside a text field edits text, never navigates', () => {
  const env = boot({ body: breadcrumb(['/', '/music']) + '<input id="q" />' });
  pressKey(env, 'Backspace', env.document.getElementById('q'));
  assert.strictEqual(env.visited.length, 0);
  assert.strictEqual(env.backCalls.length, 0);
});

test('couch.js is inert when data-couch is off', () => {
  const env = boot({ body: breadcrumb(['/', '/music']), couch: false });
  pressKey(env, 'Escape');
  assert.strictEqual(env.visited.length, 0);
  assert.strictEqual(env.backCalls.length, 0);
});
