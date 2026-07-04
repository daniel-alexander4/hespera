// Tests for web/static/photo_view.js — the photo viewer's arrow navigation.
// jsdom drives the real controller: ←/→ navigate to the server-computed
// prev/next hrefs via Turbo, consuming the key before couch.js's document
// listener; edge photos consume without navigating.

const { test } = require('node:test');
const assert = require('node:assert');
const { loadController } = require('./harness');

function boot({ prev = '', next = '', usingMouse = false } = {}) {
  const visited = [];
  const actions = [];
  const env = loadController('photo_view.js', {
    html: `<!DOCTYPE html><html${usingMouse ? ' class="using-mouse"' : ''}><body>
      <div class="photo-view" id="photoView" tabindex="0" data-prev="${prev}" data-next="${next}"></div>
    </body></html>`,
    url: 'http://localhost/photos/view?id=5',
    stubs: { Turbo: { visit: (u, o) => { visited.push(u); actions.push(o && o.action); } } },
  });
  env.visited = visited;
  env.actions = actions;
  env.document.dispatchEvent(new env.window.Event('turbo:load'));
  return env;
}

function press(env, key) {
  const e = new env.window.KeyboardEvent('keydown', { key, bubbles: true, cancelable: true });
  env.document.getElementById('photoView').dispatchEvent(e);
  return e;
}

test('arrows navigate to the prev/next photo and are consumed before couch', () => {
  const env = boot({ prev: '/photos/view?id=4&year=2019', next: '/photos/view?id=6&year=2019' });
  assert.strictEqual(env.document.activeElement.id, 'photoView', 'viewer takes focus on load');
  let e = press(env, 'ArrowRight');
  assert.strictEqual(e.defaultPrevented, true);
  assert.deepStrictEqual(env.visited, ['/photos/view?id=6&year=2019']);
  e = press(env, 'ArrowLeft');
  assert.deepStrictEqual(env.visited[1], '/photos/view?id=4&year=2019');
});

test('arrow visits REPLACE history so couch Back returns to the grid, not the photo trail', () => {
  const env = boot({ prev: '/p', next: '/n' });
  press(env, 'ArrowRight');
  press(env, 'ArrowRight');
  assert.deepStrictEqual(env.actions, ['replace', 'replace'], 'every arrow visit uses the replace action');
});

test('a mouse-driven visit is never focus-stolen (using-mouse modality)', () => {
  const env = boot({ prev: '/p', next: '/n', usingMouse: true });
  assert.notStrictEqual(env.document.activeElement.id, 'photoView', 'no focus steal under using-mouse');
});

test('at the first/last photo the arrow is consumed but does not navigate', () => {
  const env = boot({ prev: '', next: '/photos/view?id=6' });
  const e = press(env, 'ArrowLeft');
  assert.strictEqual(e.defaultPrevented, true, 'consumed — focus must not wander');
  assert.strictEqual(env.visited.length, 0);
});

test('non-arrow keys pass through untouched (Back stays couch\'s)', () => {
  const env = boot({ prev: '/p', next: '/n' });
  const e = press(env, 'Escape');
  assert.strictEqual(e.defaultPrevented, false);
  assert.strictEqual(env.visited.length, 0);
});
