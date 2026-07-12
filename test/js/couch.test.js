// Tests for web/static/couch.js — the remote/keyboard navigation layer (always
// on; couch mode is display-only). jsdom drives the real IIFE through
// dispatched keydown events. These cover the Back button's staged walk-up
// (overlay/menu guards, subtab-panel → menu bar,
// then Home — the Back button is a Home shortcut, not a history-retrace; the
// breadcrumb is the way UP — typing guard, broadened keycodes) and always-on.
// EVERY back key reaches the Home stage, Escape included, so the remote behaves
// the same whatever keycode the dongle emits.
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

test('Back goes Home from a normal page (the Back button is a Home shortcut)', () => {
  const env = boot({ body: breadcrumb(['/', '/music', '/music/artist/1']) });
  pressKey(env, 'BrowserBack');
  assert.deepStrictEqual(env.visited, ['/'], 'Back navigates Home via Turbo');
  assert.strictEqual(env.backCalls.length, 0, 'no history.back — Back is Home, not a retrace');
});

test('every back keycode goes Home — Escape included (dongles differ)', () => {
  for (const key of ['Backspace', 'Escape', 'BrowserBack', 'GoBack']) {
    const env = boot({ body: breadcrumb(['/', '/music']) });
    pressKey(env, key);
    assert.deepStrictEqual(env.visited, ['/'], `key ${key}`);
    assert.strictEqual(env.backCalls.length, 0, `key ${key}`);
  }
});

test('a player page goes Home', () => {
  const env = boot({
    body: '<div class="tv-player-page"></div>',
    url: 'http://localhost/tv/player',
  });
  pressKey(env, 'BrowserBack');
  assert.deepStrictEqual(env.visited, ['/']);
  assert.strictEqual(env.backCalls.length, 0);
});

test('Back on the root page is a no-op (already Home — no duplicate entry, no navigation)', () => {
  const env = boot({ body: '<h1>Home</h1>', url: 'http://localhost/' });
  pressKey(env, 'BrowserBack');
  assert.strictEqual(env.visited.length, 0, 'already Home — no Turbo.visit');
  assert.strictEqual(env.backCalls.length, 0, 'and no history.back');
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
  // The next press (no longer typing, nothing to dismiss) reaches the Home stage.
  pressKey(env, 'Escape');
  assert.deepStrictEqual(env.visited, ['/']);
});

test('navigation is always on — Back works without the tv scale class', () => {
  const env = boot({ body: breadcrumb(['/', '/music']), couch: false });
  pressKey(env, 'BrowserBack');
  assert.deepStrictEqual(env.visited, ['/']);
});

test('Back from inside a subtab panel focuses the menu bar, second press goes back', () => {
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
  assert.strictEqual(env.backCalls.length, 0, 'first press must not navigate');
  assert.strictEqual(env.document.activeElement.className, 'subtab active');
  // The ladder's next rung: a second Back press (any keycode) goes Home.
  pressKey(env, 'Escape', env.document.activeElement);
  assert.deepStrictEqual(env.visited, ['/'], 'second Back press goes Home');
});

test('Back yields to native fullscreen (Escape exits fullscreen, no navigation)', () => {
  const env = boot({ body: breadcrumb(['/', '/music']) });
  Object.defineProperty(env.document, 'fullscreenElement', { configurable: true, value: env.document.documentElement });
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

// --- Engage protocol: Enter captures a select/range/[data-couch-capture]
// control's arrows, Enter/Back/blur release. Unengaged controls are transparent.

function pressOn(env, el, key) {
  const e = new env.window.KeyboardEvent('keydown', { key, bubbles: true, cancelable: true });
  el.dispatchEvent(e);
  return e;
}

test('an unengaged select is transparent: arrows are consumed for focus moves, not option cycling', () => {
  const env = boot({ body: '<select id="s"><option>a</option><option>b</option></select><a href="/x">x</a>' });
  const sel = env.document.getElementById('s');
  sel.focus();
  const e = pressOn(env, sel, 'ArrowDown');
  assert.strictEqual(e.defaultPrevented, true, 'couch takes the arrow (focus move), not the select');
  assert.strictEqual(sel.hasAttribute('data-couch-engaged'), false);
});

test('Enter toggles engagement on a select; engaged arrows stay native', () => {
  const env = boot({ body: '<select id="s"><option>a</option><option>b</option></select>' });
  const sel = env.document.getElementById('s');
  sel.focus();
  pressOn(env, sel, 'Enter');
  assert.strictEqual(sel.hasAttribute('data-couch-engaged'), true, 'Enter engages');
  const arrow = pressOn(env, sel, 'ArrowDown');
  assert.strictEqual(arrow.defaultPrevented, false, 'engaged: the control owns the arrows');
  pressOn(env, sel, 'Enter');
  assert.strictEqual(sel.hasAttribute('data-couch-engaged'), false, 'Enter again releases');
});

test('Back on an engaged control releases it without navigating; the next Back navigates', () => {
  const env = boot({ body: breadcrumb(['/', '/music']) + '<input id="v" type="range" min="0" max="1" />' });
  const range = env.document.getElementById('v');
  range.focus();
  pressOn(env, range, 'Enter');
  assert.strictEqual(range.hasAttribute('data-couch-engaged'), true, 'range engages via Enter');
  pressOn(env, range, 'Escape');
  assert.strictEqual(range.hasAttribute('data-couch-engaged'), false, 'Back releases');
  assert.strictEqual(env.backCalls.length, 0, 'the releasing press does not navigate');
  // Once released, the next Back press navigates Home as usual.
  pressOn(env, range, 'Escape');
  assert.deepStrictEqual(env.visited, ['/'], 'once released, Back goes Home as usual');
  assert.strictEqual(env.backCalls.length, 0);
});

test('a [data-couch-capture] widget joins the protocol', () => {
  const env = boot({ body: '<div id="w" tabindex="0" data-couch-capture></div>' });
  const w = env.document.getElementById('w');
  w.focus();
  const before = pressOn(env, w, 'ArrowLeft');
  assert.strictEqual(before.defaultPrevented, true, 'unengaged: couch takes the arrow');
  pressOn(env, w, 'Enter');
  assert.strictEqual(w.hasAttribute('data-couch-engaged'), true);
  const after = pressOn(env, w, 'ArrowLeft');
  assert.strictEqual(after.defaultPrevented, false, 'engaged: the widget owns the arrows');
});

test('leaving an engaged control releases it (focusout)', () => {
  const env = boot({ body: '<select id="s"><option>a</option></select><a id="x" href="/x">x</a>' });
  const sel = env.document.getElementById('s');
  sel.focus();
  pressOn(env, sel, 'Enter');
  assert.strictEqual(sel.hasAttribute('data-couch-engaged'), true);
  env.document.getElementById('x').focus();
  assert.strictEqual(sel.hasAttribute('data-couch-engaged'), false, 'blur released it');
});

test('Enter on a checkbox toggles it instead of submitting the enclosing form', () => {
  const env = boot({ body: '<form><input id="c" type="checkbox" /><button type="submit">Save</button></form>' });
  const box = env.document.getElementById('c');
  box.focus();
  const e = pressOn(env, box, 'Enter');
  assert.strictEqual(box.checked, true, 'Enter toggled the checkbox');
  assert.strictEqual(e.defaultPrevented, true, 'implicit form submission suppressed');
  pressOn(env, box, 'Enter');
  assert.strictEqual(box.checked, false, 'Enter toggles both ways');
});

test('arrows in a text input stay native (caret), and in a checkbox they move focus', () => {
  const env = boot({ body: '<input id="t" type="text" /><input id="c" type="checkbox" />' });
  const text = env.document.getElementById('t');
  text.focus();
  assert.strictEqual(pressOn(env, text, 'ArrowLeft').defaultPrevented, false, 'text input keeps its caret arrows');
  const box = env.document.getElementById('c');
  box.focus();
  assert.strictEqual(pressOn(env, box, 'ArrowLeft').defaultPrevented, true, 'checkbox arrows are focus moves, not dead keys');
});

test('focusFirst lands on the active subtab under remote/keyboard modality (Recent on arrival)', () => {
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

// jsdom reports no layout (getBoundingClientRect → 0, offsetParent → null), so
// couch's visible() filters every candidate out and the content-focus path can't
// run. Give the elements layout so the real focusFirst logic is exercised.
function stubLayout(env) {
  const proto = env.window.HTMLElement.prototype;
  Object.defineProperty(proto, 'offsetParent', { configurable: true, get() { return this.parentNode; } });
  proto.getBoundingClientRect = function () { return { width: 10, height: 10, top: 0, left: 0, right: 10, bottom: 10 }; };
}

test('focusFirst anchors the home dashboard on the Music card at any scale, modality-gated', () => {
  // Mirrors the real home.html: the utility cluster (clock, version pill,
  // search) precedes the cards inside <main>, so a generic first-in-content rule
  // would land the ring on the pill. The fixture carries it for that reason.
  const homeBody = '<main>' +
    '<div class="page-header home-header"><div class="home-utils">' +
    '<button id="update-pill">v0</button>' +
    '<button data-search-open>search</button>' +
    '</div></div>' +
    '<div class="card-grid">' +
    '<a href="/music" class="card"><h3>Music</h3></a>' +
    '<a href="/tv" class="card"><h3>TV Shows</h3></a></div></main>';

  // Desktop scale (no subtab bar) + keyboard modality → the Music card gets the
  // ring, even though non-home pages only auto-focus content at tv scale.
  const key = boot({ body: homeBody, url: 'http://localhost/', couch: false });
  stubLayout(key);
  key.document.dispatchEvent(new key.window.Event('turbo:load'));
  assert.strictEqual(key.document.activeElement.getAttribute('href'), '/music', 'Music card focused on a keyboard/remote home start');

  // A mouse-driven session returning home is never focus-stolen.
  const mouse = boot({ body: homeBody, url: 'http://localhost/', couch: false });
  stubLayout(mouse);
  mouse.document.dispatchEvent(new mouse.window.MouseEvent('mousemove', { bubbles: true }));
  mouse.document.dispatchEvent(new mouse.window.Event('turbo:load'));
  assert.strictEqual(mouse.document.activeElement, mouse.document.body, 'mouse modality never steals focus on home');

  // Regression: a NON-home page at desktop scale still auto-focuses nothing (the
  // home bypass must not leak to other routes).
  const other = boot({ body: '<main><a href="/x" id="x">x</a></main>', url: 'http://localhost/music/album/1', couch: false });
  stubLayout(other);
  other.document.dispatchEvent(new other.window.Event('turbo:load'));
  assert.strictEqual(other.document.activeElement, other.document.body, 'non-home desktop page is not focus-stolen');
});

// ---- Home key --------------------------------------------------------------
// The remote's Home button. The Flirc emits Alt+Home (key 'Home'); other dongles
// send BrowserHome/GoHome, so all three are handled, modifiers ignored.
const homeMarkup = '<main>' +
  '<div class="page-header home-header"><div class="home-utils"><button id="update-pill">v0</button></div></div>' +
  '<div class="card-grid"><a href="/music" class="card">Music</a><a href="/tv" class="card">TV</a></div></main>';

test('every home keycode goes Home in one press, from anywhere', () => {
  for (const key of ['Home', 'BrowserHome', 'GoHome']) {
    const env = boot({ body: breadcrumb(['/', '/music', '/music/artist/1']) });
    pressKey(env, key);
    assert.deepStrictEqual(env.visited, ['/'], `key ${key} navigates Home via Turbo`);
  }
});

test('Home skips the Back ladder: it leaves a subtab panel for home, not the menu bar', () => {
  const env = boot({
    body: '<div class="subtabs"><button class="subtab active">Recent</button></div>' +
      '<div class="subtab-panel"><a href="/x" id="inpanel">x</a></div>',
    url: 'http://localhost/music',
  });
  env.document.getElementById('inpanel').focus();
  pressKey(env, 'Home', env.document.getElementById('inpanel'));
  assert.deepStrictEqual(env.visited, ['/'], 'one press goes home from inside a panel');
});

test('Home already on the root page focuses the Music card without navigating', () => {
  const env = boot({ body: homeMarkup, url: 'http://localhost/' });
  env.document.getElementById('update-pill').focus();
  pressKey(env, 'Home', env.document.getElementById('update-pill'));
  assert.deepStrictEqual(env.visited, [], 'no duplicate history entry when already home');
  assert.strictEqual(env.document.activeElement.getAttribute('href'), '/music', 'ring lands on the Music card');
});

test('Home inside a text field moves the caret, never navigates', () => {
  const env = boot({ body: '<input type="text" id="q">', url: 'http://localhost/music' });
  const input = env.document.getElementById('q');
  input.focus();
  pressKey(env, 'Home', input);
  assert.deepStrictEqual(env.visited, [], 'Home stays native while typing');
  assert.strictEqual(env.document.activeElement.id, 'q', 'field keeps focus');
});

test('Home on an engaged control stays native (the control owns its keys)', () => {
  const env = boot({ body: '<input type="range" id="vol">', url: 'http://localhost/music' });
  const range = env.document.getElementById('vol');
  range.focus();
  pressKey(env, 'Enter', range); // engage
  assert.ok(range.hasAttribute('data-couch-engaged'), 'engaged');
  pressKey(env, 'Home', range);
  assert.deepStrictEqual(env.visited, [], 'engaged control keeps Home');
});

test('focusFirst yields to an anchor another controller already set', () => {
  const env = boot({
    body: '<button id="pre">preset</button>' +
      '<div class="subtabs"><button class="subtab active" data-tab="recent">Recent</button></div>',
    url: 'http://localhost/music',
    couch: false,
  });
  env.document.getElementById('pre').focus();
  env.document.dispatchEvent(new env.window.Event('turbo:load'));
  assert.strictEqual(env.document.activeElement.id, 'pre',
    'a pre-set anchor (e.g. the media player focusing play/pause) is never stolen');
});
