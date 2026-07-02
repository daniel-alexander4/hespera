// Tests for web/static/grid_pager.js — in-place paging for the browse grids.
// jsdom drives the real controller through DOM effects (chevron clicks, edge
// keydowns) with a stub fetch returning per-page card HTML. The geometry edge
// test (atEdge via getBoundingClientRect) is degenerate in jsdom — every rect is
// 0×0, so a card never has a neighbour "to the right" and every press reads as an
// edge; that's enough to prove the keydown→advance wiring, but the real
// rightmost-column detection stays in the Playwright/manual smoke.

const { test } = require('node:test');
const assert = require('node:assert');
const { loadController, flush } = require('./harness');

function cards(n, page) {
  let h = '';
  for (let i = 0; i < n; i++) h += `<a class="band-album-card" href="/x/${page}/${i}">p${page}c${i}</a>`;
  return h;
}

function fixture({ page = 1, total = 3, q = '' } = {}) {
  return `<!DOCTYPE html><html data-couch="1"><body>
    <div class="subtab-panel">
      <div class="band-albums-grid" data-grid-pager data-page="${page}" data-total-pages="${total}" data-q="${q}">
        ${cards(4, page)}
      </div>
      <nav class="grid-pager" data-total-pages="${total}">
        <a class="grid-pager-btn grid-pager-prev${page <= 1 ? ' is-hidden' : ''}" href="/x?page=${page - 1}">L</a>
        <a class="grid-pager-btn grid-pager-next${page >= total ? ' is-hidden' : ''}" href="/x?page=${page + 1}">R</a>
        <span class="grid-pager-info">Page ${page} of ${total}</span>
      </nav>
    </div>
  </body></html>`;
}

function boot(opts = {}) {
  const calls = [];
  const fetch = async (url) => {
    calls.push(String(url));
    const m = String(url).match(/page=(\d+)/);
    const p = m ? parseInt(m[1], 10) : 1;
    return { ok: true, text: async () => cards(4, p) };
  };
  const env = loadController('grid_pager.js', {
    html: fixture(opts),
    url: 'http://localhost/music',
    stubs: { fetch },
  });
  env.calls = calls;
  env.window.document.dispatchEvent(new env.window.Event('turbo:load'));
  return env;
}

const grid = (env) => env.document.querySelector('.band-albums-grid');
const info = (env) => env.document.querySelector('.grid-pager-info').textContent;
const hidden = (env, which) => env.document.querySelector('.grid-pager-' + which).classList.contains('is-hidden');
const clickChevron = (env, which) => env.document.querySelector('.grid-pager-' + which).click();

test('prefetches the next page on init', async () => {
  const env = boot({ page: 1, total: 3 });
  await flush();
  assert.ok(env.calls.some((u) => u.includes('grid=1') && u.includes('page=2')), 'prefetched page 2');
});

test('the next chevron swaps the grid in place and updates the indicator', async () => {
  const env = boot({ page: 1, total: 3 });
  clickChevron(env, 'next');
  await flush();
  assert.ok(grid(env).innerHTML.includes('p2c0'), 'grid shows page-2 cards');
  assert.strictEqual(grid(env).dataset.page, '2');
  assert.strictEqual(info(env), 'Page 2 of 3');
  assert.strictEqual(hidden(env, 'prev'), false);
  assert.strictEqual(hidden(env, 'next'), false);
});

test('first page hides prev; last page hides next', async () => {
  const env = boot({ page: 1, total: 2 });
  assert.strictEqual(hidden(env, 'prev'), true);
  clickChevron(env, 'next');
  await flush();
  assert.strictEqual(info(env), 'Page 2 of 2');
  assert.strictEqual(hidden(env, 'next'), true);
  assert.strictEqual(hidden(env, 'prev'), false);
});

test('a page is fetched once then served from cache', async () => {
  const env = boot({ page: 1, total: 3 });
  await flush(); // init prefetch of page 2
  clickChevron(env, 'next'); // → 2 (cache hit)
  await flush();
  clickChevron(env, 'prev'); // → 1
  await flush();
  clickChevron(env, 'next'); // → 2 again (cache hit)
  await flush();
  const page2Fetches = env.calls.filter((u) => u.includes('page=2')).length;
  assert.strictEqual(page2Fetches, 1, 'page 2 fetched exactly once');
});

test('an edge ArrowRight advances the grid', async () => {
  const env = boot({ page: 1, total: 3 });
  grid(env).querySelector('.band-album-card').focus();
  grid(env).dispatchEvent(new env.window.KeyboardEvent('keydown', { key: 'ArrowRight', bubbles: true, cancelable: true }));
  await flush();
  assert.strictEqual(grid(env).dataset.page, '2');
});

test('carries the search query into fragment fetches', async () => {
  const env = boot({ page: 1, total: 3, q: 'sab' });
  clickChevron(env, 'next');
  await flush();
  assert.ok(env.calls.some((u) => u.includes('q=sab')), 'q carried into the fetch');
});

test('single-page grids wire nothing', async () => {
  const env = boot({ page: 1, total: 1 });
  await flush();
  assert.strictEqual(env.calls.length, 0, 'no prefetch when there is only one page');
});
