// Tests for web/static/about_health.js — the About "System" health rows. jsdom
// drives the real controller: opening the About card fetches /update/check
// (Hespera row) and /about/health (ffmpeg + Browser rows) once and renders each
// row's badge, version, and detail from the JSON.

const { test } = require('node:test');
const assert = require('node:assert');
const { loadController, makeFetch, flush } = require('./harness');

const ROWS = ['hespera', 'ffmpeg', 'chrome'].map((k) => `
  <div class="about-health-row" data-health="${k}">
    <div class="about-health-line">
      <span class="about-health-label">${k}</span>
      <span class="badge about-health-badge">checking…</span>
      <span class="about-health-version"></span>
    </div>
    <p class="form-help about-health-detail"></p>
  </div>`).join('');

function boot({ open = true, routes = {} } = {}) {
  const html = `<!DOCTYPE html><html><body>
    <details ${open ? 'open' : ''}>
      <summary>About</summary>
      <div class="about-health" id="about-health" data-current="0.31.14">${ROWS}</div>
    </details>
  </body></html>`;
  const env = loadController('about_health.js', { html, stubs: { fetch: makeFetch(routes) } });
  env.document.dispatchEvent(new env.window.Event('turbo:load'));
  return env;
}

const badge = (env, k) => env.document.querySelector(`[data-health="${k}"] .about-health-badge`);
const ver = (env, k) => env.document.querySelector(`[data-health="${k}"] .about-health-version`).textContent;
const detail = (env, k) => env.document.querySelector(`[data-health="${k}"] .about-health-detail`).textContent;

test('open card renders all three rows from the two endpoints', async () => {
  const env = boot({ routes: {
    '/update/check': { current: '0.31.14', latest: '0.31.14', updateAvailable: false },
    '/about/health': {
      ffmpeg: { status: 'ok', version: '7.1.1', detail: 'ff detail' },
      chrome: { status: 'ok', name: 'Chromium', version: '149.0', detail: 'ch detail' },
    },
  } });
  await flush();
  assert.ok(badge(env, 'hespera').classList.contains('badge-done'), 'hespera green when up to date');
  assert.strictEqual(ver(env, 'hespera'), 'v0.31.14');
  assert.ok(badge(env, 'ffmpeg').classList.contains('badge-done'));
  assert.strictEqual(ver(env, 'ffmpeg'), '7.1.1');
  assert.strictEqual(detail(env, 'ffmpeg'), 'ff detail');
  assert.ok(badge(env, 'chrome').classList.contains('badge-done'));
  assert.strictEqual(ver(env, 'chrome'), 'Chromium 149.0', 'browser name + version rendered');
});

test('update available and outdated ffmpeg turn their rows yellow', async () => {
  const env = boot({ routes: {
    '/update/check': { current: '0.31.14', latest: '0.32.0', updateAvailable: true },
    '/about/health': {
      ffmpeg: { status: 'warn', version: '6.1.1', detail: 'upgrade for HEIC' },
      chrome: { status: 'na', detail: 'on your device' },
    },
  } });
  await flush();
  assert.ok(badge(env, 'hespera').classList.contains('badge-warn'));
  assert.match(detail(env, 'hespera'), /0\.32\.0/);
  assert.ok(badge(env, 'ffmpeg').classList.contains('badge-warn'));
  assert.ok(badge(env, 'chrome').classList.contains('badge-queued'), 'na → neutral badge');
});

test('missing ffmpeg / no browser render the failed badge', async () => {
  const env = boot({ routes: {
    '/update/check': { current: '0.31.14' },
    '/about/health': {
      ffmpeg: { status: 'missing', detail: 'install ffmpeg' },
      chrome: { status: 'missing', detail: 'install chromium' },
    },
  } });
  await flush();
  assert.ok(badge(env, 'ffmpeg').classList.contains('badge-failed'));
  assert.ok(badge(env, 'chrome').classList.contains('badge-failed'));
});

test('a closed card does not fetch until opened', async () => {
  const env = boot({ open: false, routes: {
    '/update/check': { current: '0.31.14' },
    '/about/health': { ffmpeg: { status: 'ok', detail: 'x' }, chrome: { status: 'na', detail: 'y' } },
  } });
  await flush();
  assert.strictEqual(env.fetch.calls.length, 0, 'no fetch while collapsed');
  const card = env.document.querySelector('details');
  card.open = true;
  card.dispatchEvent(new env.window.Event('toggle'));
  await flush();
  assert.ok(env.fetch.calls.length >= 2, 'opening the card triggers both fetches');
});
