'use strict';
// book_reader.js — the CBZ page-stepping + progress-beacon wiring. The EPUB
// iframe mechanics (same-origin scroll restore) are outside jsdom's ceiling
// and stay in live verification.
const test = require('node:test');
const assert = require('node:assert');
const { loadController, makeFetch, flush } = require('./harness');

const readBlob = (env, blob) =>
  new Promise((res) => {
    const r = new env.window.FileReader();
    r.onload = () => res(String(r.result));
    r.readAsText(blob);
  });
const beaconBodies = (env) =>
  Promise.all((env.window.__beacons || []).map((b) => (typeof b.data === 'string' ? b.data : readBlob(env, b.data))));

function fixture({ kind = 'cbz', entries = ['p1.jpg', 'p2.jpg', 'p3.jpg'], startIndex = 0, startFraction = 0 } = {}) {
  const data = JSON.stringify(entries).replace(/"/g, '&quot;');
  return `
  <main>
    <div id="bookReader" data-book-id="7" data-kind="${kind}" data-entry-count="${entries.length}"
         data-start-index="${startIndex}" data-start-fraction="${startFraction}" data-entries="${data}">
      <div class="book-reader-bar">
        <button id="bookPrevBtn"></button><span id="bookPos"></span><button id="bookNextBtn"></button>
      </div>
      <div id="bookStage" tabindex="0"><img id="bookPage" /></div>
    </div>
  </main>`;
}

async function boot(opts) {
  const env = loadController('book_reader.js', {
    html: fixture(opts),
    url: 'http://localhost/book/reader?id=7',
    stubs: { fetch: makeFetch({}) },
  });
  env.window.document.dispatchEvent(new env.window.Event('turbo:load'));
  await flush();
  return env;
}

test('boots at the stored page and steps with the buttons', async () => {
  const env = await boot({ startIndex: 1 });
  const page = env.document.getElementById('bookPage');
  assert.ok(page.getAttribute('src').indexOf('/book/asset/7/p2.jpg') >= 0, `resumes at page 2: ${page.getAttribute('src')}`);
  assert.strictEqual(env.document.getElementById('bookPos').textContent, '2 / 3');

  env.document.getElementById('bookNextBtn').dispatchEvent(new env.window.Event('click'));
  assert.ok(page.getAttribute('src').indexOf('p3.jpg') >= 0, 'next steps forward');
  assert.strictEqual(env.document.getElementById('bookNextBtn').disabled, true, 'next disabled at the last page');

  env.document.getElementById('bookPrevBtn').dispatchEvent(new env.window.Event('click'));
  assert.ok(page.getAttribute('src').indexOf('p2.jpg') >= 0, 'prev steps back');
});

test('a step beacons the position; the last page earns completed', async () => {
  const env = await boot({ startIndex: 1 });
  env.document.getElementById('bookNextBtn').dispatchEvent(new env.window.Event('click'));
  await flush();
  const bodies = (await beaconBodies(env)).map((b) => JSON.parse(b));
  assert.ok(bodies.length >= 1, 'a step flushes a beacon');
  assert.ok(bodies.every((b) => b.book_id === 7), 'beacons carry the book id');
  const last = bodies[bodies.length - 1];
  assert.strictEqual(last.spine_index, 2, 'the landing page is reported');
  assert.strictEqual(last.completed, true, 'reaching the final page reports completed');
  // An earlier beacon (leaving page 2) must NOT claim completion.
  assert.strictEqual(bodies[0].completed, false, 'mid-book beacons never claim completed');
});

test('arrow keys on the focused stage page the comic', async () => {
  const env = await boot({ startIndex: 0 });
  const stage = env.document.getElementById('bookStage');
  const page = env.document.getElementById('bookPage');
  stage.dispatchEvent(new env.window.KeyboardEvent('keydown', { key: 'ArrowRight', bubbles: true, cancelable: true }));
  assert.ok(page.getAttribute('src').indexOf('p2.jpg') >= 0, '→ pages forward');
  stage.dispatchEvent(new env.window.KeyboardEvent('keydown', { key: 'ArrowLeft', bubbles: true, cancelable: true }));
  assert.ok(page.getAttribute('src').indexOf('p1.jpg') >= 0, '← pages back');
  assert.strictEqual(env.document.getElementById('bookPrevBtn').disabled, true, 'prev disabled at page 1');
});

test('a PDF reader page boots no JS machinery', async () => {
  const env = loadController('book_reader.js', {
    html: `<main><div id="bookReader" data-book-id="7" data-kind="pdf"><embed src="/book/file/7"></div></main>`,
    url: 'http://localhost/book/reader?id=7',
    stubs: { fetch: makeFetch({}) },
  });
  env.window.document.dispatchEvent(new env.window.Event('turbo:load'));
  await flush();
  assert.strictEqual(env.window.__beacons.length, 0, 'no beacons for the native viewer');
});
