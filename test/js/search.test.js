// Tests for web/static/search.js — the "/" jump-to palette. jsdom drives the
// real controller: hotkey open, debounced fetch, section rendering, Enter =
// first result, Escape-in-input close.

const { test } = require('node:test');
const assert = require('node:assert');
const { loadController, makeFetch, flush } = require('./harness');

const PAGE = `
<button class="util-btn" data-search-open></button>
<input id="page-input" type="text" />
<div id="search-modal" class="playlist-modal hidden" data-couch-overlay>
  <div class="playlist-panel">
    <input id="search-input" type="search" />
    <button id="search-close" data-couch-dismiss>Close</button>
    <div id="search-results"></div>
  </div>
</div>`;

const RESULTS = {
  sections: [
    { label: 'Artists', rows: [{ href: '/music/artist/9', text: 'Rainbow' }] },
    { label: 'Songs', rows: [{ href: '/music/player?album=3&track=21', text: 'Stargazer', context: 'Rainbow' }] },
  ],
};

function boot() {
  const env = loadController('search.js', {
    html: `<!DOCTYPE html><html><body>${PAGE}</body></html>`,
    url: 'http://localhost/music',
    stubs: { fetch: makeFetch({ '/search': RESULTS }) },
  });
  env.document.dispatchEvent(new env.window.Event('turbo:load'));
  return env;
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

test('"/" opens the palette and focuses the input; typing in a field does not', () => {
  const env = boot();
  const modal = env.document.getElementById('search-modal');

  // "/" while a page text field is focused stays native.
  const pageInput = env.document.getElementById('page-input');
  pageInput.focus();
  pageInput.dispatchEvent(new env.window.KeyboardEvent('keydown', { key: '/', bubbles: true, cancelable: true }));
  assert.ok(modal.classList.contains('hidden'), 'typing guard holds');

  pageInput.blur();
  env.document.dispatchEvent(new env.window.KeyboardEvent('keydown', { key: '/', bubbles: true, cancelable: true }));
  assert.ok(!modal.classList.contains('hidden'), 'palette opened');
  assert.strictEqual(env.document.activeElement.id, 'search-input', 'input focused');
});

test('typing fetches once (debounced) and renders grouped rows via textContent', async () => {
  const env = boot();
  env.document.querySelector('[data-search-open]').click();
  const input = env.document.getElementById('search-input');

  // A quick burst of keystrokes → one request.
  for (const v of ['r', 'ra', 'rai', 'rain']) {
    input.value = v;
    input.dispatchEvent(new env.window.Event('input', { bubbles: true }));
  }
  await sleep(250);
  await flush();
  const calls = env.fetch.calls.filter((c) => c.url.indexOf('/search') >= 0);
  assert.strictEqual(calls.length, 1, `debounced to one fetch, got ${calls.length}`);
  assert.ok(calls[0].url.indexOf('q=rain') >= 0);

  const rows = env.document.querySelectorAll('#search-results a');
  assert.strictEqual(rows.length, 2);
  assert.strictEqual(rows[0].getAttribute('href'), '/music/artist/9');
  assert.strictEqual(rows[0].textContent, 'Rainbow');
  assert.strictEqual(env.document.querySelectorAll('.search-section-label').length, 2);
});

test('Enter activates the first result; Escape in the input closes the palette', async () => {
  const env = boot();
  env.document.querySelector('[data-search-open]').click();
  const modal = env.document.getElementById('search-modal');
  const input = env.document.getElementById('search-input');
  input.value = 'rain';
  input.dispatchEvent(new env.window.Event('input', { bubbles: true }));
  await sleep(250);
  await flush();

  let clickedHref = null;
  env.document.getElementById('search-results').addEventListener('click', (e) => {
    clickedHref = e.target.closest('a').getAttribute('href');
    e.preventDefault(); // jsdom can't navigate
  });
  input.dispatchEvent(new env.window.KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }));
  assert.strictEqual(clickedHref, '/music/artist/9', 'Enter clicked the first row');
  assert.ok(modal.classList.contains('hidden'), 'closed after activation');

  env.document.querySelector('[data-search-open]').click();
  assert.ok(!modal.classList.contains('hidden'));
  input.dispatchEvent(new env.window.KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }));
  assert.ok(modal.classList.contains('hidden'), 'Escape in the input closes');
});

test('closing restores focus to where it was before opening (no drop to <body>)', async () => {
  const env = boot();
  // A control on the page had the remote's focus before the palette opened.
  const opener = env.document.querySelector('[data-search-open]');
  opener.focus();
  assert.strictEqual(env.document.activeElement, opener, 'opener focused first');

  // Open via the "/" opener, which records the pre-open focus.
  env.document.dispatchEvent(new env.window.KeyboardEvent('keydown', { key: '/', bubbles: true, cancelable: true }));
  const input = env.document.getElementById('search-input');
  assert.strictEqual(env.document.activeElement, input, 'palette input takes focus');

  // Escape dismisses AND hands focus back — not to <body>, where the next
  // remote arrow would restart from the top-left logo.
  input.dispatchEvent(new env.window.KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }));
  assert.strictEqual(env.document.activeElement, opener, 'focus restored to the pre-open control');

  // A row click navigates (Turbo swaps the page), so it must NOT restore.
  opener.click(); // reopen
  opener.blur();  // simulate focus already moved by navigation intent
  const results = env.document.getElementById('search-results');
  results.innerHTML = '<a href="/x">r</a>';
  results.querySelector('a').click(); // row click → close(), no restore
  assert.ok(env.document.getElementById('search-modal').classList.contains('hidden'), 'row click closes');
  assert.notStrictEqual(env.document.activeElement, opener, 'row-click path does not restore (it navigates)');
});
