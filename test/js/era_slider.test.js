// Tests for web/static/era_slider.js — the "Shuffle Era" range window. jsdom
// drives the real controller through its keyboard effects (the remote/couch path)
// and asserts the shuffle href stays in sync. Pointer-drag geometry needs real
// layout, so it stays in the Playwright smoke.

const { test } = require('node:test');
const assert = require('node:assert');
const { loadController } = require('./harness');

const MIN = 1968;
const MAX = 2021;

function fixture() {
  return `<!DOCTYPE html><html><body>
    <div class="era-picker" data-min="${MIN}" data-max="${MAX}" data-lib="3">
      <div class="era-readout"><span class="era-from"></span> – <span class="era-to"></span></div>
      <div class="era-track" tabindex="0" role="slider" aria-valuemin="${MIN}" aria-valuemax="${MAX}">
        <div class="era-window">
          <span class="era-handle era-handle-l" data-edge="l"></span>
          <span class="era-handle era-handle-r" data-edge="r"></span>
        </div>
      </div>
      <div class="era-axis"></div>
      <div class="era-controls">
        <span class="era-hint"></span>
        <a class="era-shuffle" data-play href="#">Shuffle Era</a>
      </div>
    </div>
  </body></html>`;
}

function boot() {
  const env = loadController('era_slider.js', { html: fixture(), url: 'http://localhost/music' });
  // couch.js stand-in: a document-level keydown listener. era_slider must
  // stopPropagation the arrows/Enter so this (the focus mover) never sees them.
  let couchKeys = 0;
  env.document.addEventListener('keydown', () => { couchKeys += 1; });
  // Capture shuffle clicks without jsdom attempting a real navigation.
  let shuffleClicks = 0;
  env.document.querySelector('.era-shuffle').addEventListener('click', (e) => { e.preventDefault(); shuffleClicks += 1; });
  env.document.dispatchEvent(new env.window.Event('turbo:load'));
  return Object.assign(env, { couch: () => couchKeys, shuffleClicks: () => shuffleClicks });
}

const press = (env, k) =>
  env.document.querySelector('.era-track').dispatchEvent(new env.window.KeyboardEvent('keydown', { key: k, bubbles: true }));
const state = (env) => ({
  from: Number(env.document.querySelector('.era-from').textContent),
  to: Number(env.document.querySelector('.era-to').textContent),
  href: env.document.querySelector('.era-shuffle').getAttribute('href'),
});

test('inits to the most recent decade and syncs the shuffle href', () => {
  const s = state(boot());
  assert.strictEqual(s.to, MAX, 'to = latest year');
  assert.strictEqual(s.from, MAX - 9, 'from = a decade back');
  assert.ok(s.href.includes('source=era&from=' + (MAX - 9) + '&to=' + MAX + '&shuffle=1'), 'href reflects the range: ' + s.href);
  assert.ok(s.href.includes('library=3'), 'href carries the library');
});

test('ArrowLeft slides the window and couch never sees the arrow', () => {
  const env = boot();
  const before = env.couch();
  press(env, 'ArrowLeft');
  const s = state(env);
  assert.strictEqual(s.from, MAX - 10, 'from slid left one year');
  assert.strictEqual(s.to, MAX - 1, 'to slid left too (width preserved)');
  assert.ok(s.href.includes('from=' + (MAX - 10) + '&to=' + (MAX - 1)), 'href followed the slide');
  assert.strictEqual(env.couch(), before, 'arrow captured — couch focus-mover not invoked');
});

test('ArrowUp widens the span; ArrowDown narrows it', () => {
  const env = boot(); // from=MAX-9, to=MAX
  press(env, 'ArrowUp');
  let s = state(env);
  assert.strictEqual(s.from, MAX - 10, 'widened: from -1');
  assert.strictEqual(s.to, MAX, 'to already at max → clamped');
  press(env, 'ArrowDown');
  s = state(env);
  assert.strictEqual(s.from, MAX - 9, 'narrowed: from +1');
  assert.strictEqual(s.to, MAX - 1, 'narrowed: to -1');
});

test('sliding is clamped at the timeline edge', () => {
  const env = boot(); // to already at MAX
  press(env, 'ArrowRight');
  const s = state(env);
  assert.strictEqual(s.to, MAX, 'cannot slide past the latest year');
  assert.strictEqual(s.from, MAX - 9, 'from unchanged when clamped');
});

test('narrowing never collapses below a single year', () => {
  const env = boot();
  for (let i = 0; i < 20; i += 1) press(env, 'ArrowDown'); // over-narrow
  const s = state(env);
  assert.ok(s.to >= s.from, 'from never crosses to');
  assert.ok(s.to - s.from <= 1, 'collapses to a single year, not inverted: ' + JSON.stringify(s));
});

test('Enter shuffles; a non-arrow key passes through to couch', () => {
  const env = boot();
  press(env, 'Enter');
  assert.strictEqual(env.shuffleClicks(), 1, 'Enter clicked the shuffle link');
  const before = env.couch();
  press(env, 'a'); // an unhandled key must bubble (not captured)
  assert.strictEqual(env.couch(), before + 1, 'non-arrow keys still reach couch');
});
