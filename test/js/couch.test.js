// Tests for web/static/couch.js — the remote/keyboard navigation layer (always
// on; couch mode is display-only). jsdom drives the real IIFE through
// dispatched keydown events. These cover the Back button's staged walk-up
// (overlay/menu guards, subtab-panel → menu bar, semantic parent, history
// fallback, typing guard, broadened keycodes) and the always-on behavior.
// What jsdom can't model — the visible()/overlay-dismiss path and spatial
// arrow focus both need real layout (getBoundingClientRect) — stays in the
// Playwright/manual smoke.

const { test } = require('node:test');
const assert = require('node:assert');
const { loadController } = require('./harness');

// Boot couch.js in a fresh window. `couch` toggles the tv scale class (the
// focusFirst gate), `body` is
// the page content, and we stub Turbo.visit + spy history.back to observe where
// a Back press sends the user.
function boot({ body = '', url = 'http://localhost/music/album/1', couch = true } = {}) {
  const html = `<!DOCTYPE html><html${couch ? ' data-scale="tv"' : ''}><body>${body}</body></html>`;
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

test('Escape inside a text field blurs it (exits the trap) without navigating', () => {
  const env = boot({ body: breadcrumb(['/', '/music']) + '<input id="q" />' });
  const input = env.document.getElementById('q');
  input.focus();
  assert.strictEqual(env.document.activeElement, input);
  pressKey(env, 'Escape', input);
  assert.notStrictEqual(env.document.activeElement, input); // focus left the field
  assert.strictEqual(env.visited.length, 0); // first press only exits — no navigation
  assert.strictEqual(env.backCalls.length, 0);
  // The next Escape (no longer typing) climbs to the parent as usual.
  pressKey(env, 'Escape');
  assert.deepStrictEqual(env.visited, ['/music']);
});

test('navigation is always on — Back works without the tv scale class', () => {
  const env = boot({ body: breadcrumb(['/', '/music']), couch: false });
  pressKey(env, 'Escape');
  assert.deepStrictEqual(env.visited, ['/music']);
});

test('Back from inside a subtab panel focuses the menu bar, second press climbs', () => {
  const env = boot({
    body:
      breadcrumb(['/']) +
      '<div class="subtabs"><button class="subtab active" data-tab="artists">Artists</button></div>' +
      '<div id="tab-artists" class="subtab-panel active"><a id="card" href="/music/artist/1">card</a></div>',
    url: 'http://localhost/music',
  });
  const card = env.document.getElementById('card');
  card.focus();
  pressKey(env, 'Escape', card);
  assert.strictEqual(env.visited.length, 0, 'first press must not navigate');
  assert.strictEqual(env.document.activeElement.className, 'subtab active');
  pressKey(env, 'Escape', env.document.activeElement);
  assert.deepStrictEqual(env.visited, ['/'], 'second press climbs to the parent');
});

test('Back yields to native fullscreen (Escape exits fullscreen, no navigation)', () => {
  const env = boot({ body: breadcrumb(['/', '/music']) });
  Object.defineProperty(env.document, 'fullscreenElement', { configurable: true, value: env.document.documentElement });
  pressKey(env, 'Escape');
  assert.strictEqual(env.visited.length, 0);
  assert.strictEqual(env.backCalls.length, 0);
});

test('Back yields to an open topbar dropdown menu', () => {
  const env = boot({ body: breadcrumb(['/', '/music']) + '<div data-menu data-open="1"><a href="/settings">s</a></div>' });
  pressKey(env, 'Escape');
  assert.strictEqual(env.visited.length, 0);
  assert.strictEqual(env.backCalls.length, 0);
});

test('mouse movement sets using-mouse; a handled key clears it', () => {
  const env = boot({ body: breadcrumb(['/', '/music']) });
  env.document.dispatchEvent(new env.window.MouseEvent('mousemove', { bubbles: true }));
  assert.ok(env.document.documentElement.classList.contains('using-mouse'));
  pressKey(env, 'Escape');
  assert.ok(!env.document.documentElement.classList.contains('using-mouse'));
});

test('arrows inside a select stay native (no preventDefault)', () => {
  const env = boot({ body: '<select id="s"><option>a</option><option>b</option></select><a href="/x">x</a>' });
  const sel = env.document.getElementById('s');
  sel.focus();
  const e = new env.window.KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true, cancelable: true });
  sel.dispatchEvent(e);
  assert.strictEqual(e.defaultPrevented, false);
});

test('focusFirst lands on the active subtab under remote/keyboard modality (Recent from the topbar)', () => {
  const env = boot({
    body:
      breadcrumb(['/']) +
      '<a id="early" href="/x">earlier control</a>' +
      '<div class="subtabs"><button class="subtab active" data-tab="recent">Recent</button><button class="subtab" data-tab="artists">Artists</button></div>' +
      '<div id="tab-recent" class="subtab-panel active"></div>',
    url: 'http://localhost/music',
  });
  env.document.dispatchEvent(new env.window.Event('turbo:load'));
  assert.strictEqual(env.document.activeElement.textContent, 'Recent', 'ring lands on the active subtab, not the first control');

  // Modality, not scale: a remote on a `large` (or desktop) display still gets
  // the subtab anchor — using-mouse is absent when the visit was key-driven.
  const remote = boot({
    body: '<div class="subtabs"><button class="subtab active" data-tab="recent">Recent</button></div>',
    url: 'http://localhost/music',
    couch: false,
  });
  remote.document.dispatchEvent(new remote.window.Event('turbo:load'));
  assert.strictEqual(remote.document.activeElement.textContent, 'Recent', 'keyboard modality gets the anchor at any scale');

  // A mouse-driven visit (using-mouse persists on <html> across body swaps) is
  // never focus-stolen below tv scale.
  const mouse = boot({
    body: '<div class="subtabs"><button class="subtab active" data-tab="recent">Recent</button></div>',
    url: 'http://localhost/music',
    couch: false,
  });
  mouse.document.dispatchEvent(new mouse.window.MouseEvent('mousemove', { bubbles: true }));
  mouse.document.dispatchEvent(new mouse.window.Event('turbo:load'));
  assert.strictEqual(mouse.document.activeElement, mouse.document.body, 'mouse modality never steals focus');
});
