// Tests for web/static/update_pill.js — the home-screen version pill. jsdom drives
// the real controller: turbo:load triggers the once-per-session auto check
// (server-gated by ?auto=1), a click always re-checks and navigates to the
// download URL when an update exists. The pill starts in the server-rendered
// yellow is-unknown state.

const { test } = require('node:test');
const assert = require('node:assert');
const { loadController, makeFetch, flush } = require('./harness');

const PILL = '<button type="button" id="update-pill" class="update-pill is-unknown" title="Installed version v0.25.5">v0.25.5</button>';

function boot(routes) {
  const env = loadController('update_pill.js', {
    html: `<!DOCTYPE html><html><body>${PILL}</body></html>`,
    stubs: { fetch: makeFetch(routes) },
  });
  env.pill = env.document.getElementById('update-pill');
  return env;
}

const turboLoad = (env) => env.document.dispatchEvent(new env.window.Event('turbo:load'));

test('auto check disabled: pill stays yellow and only the gated ?auto=1 request fires', async () => {
  const env = boot({ '/update/check': { enabled: false, current: '0.25.5' } });
  turboLoad(env);
  await flush();
  assert.ok(env.pill.classList.contains('is-unknown'), 'stays yellow');
  assert.strictEqual(env.fetch.calls.length, 1);
  assert.ok(env.fetch.calls[0].url.includes('auto=1'), 'the automatic path is server-gated via ?auto=1');
});

test('auto check, up to date: pill turns green', async () => {
  const env = boot({ '/update/check': { enabled: true, current: '0.25.5', latest: '0.25.5', updateAvailable: false } });
  turboLoad(env);
  await flush();
  assert.ok(env.pill.classList.contains('is-current'), 'green when confirmed latest');
  assert.strictEqual(env.pill.textContent, 'v0.25.5');
});

test('auto check, newer release: pill turns red', async () => {
  const env = boot({ '/update/check': { enabled: true, current: '0.25.5', latest: '9.9.9', updateAvailable: true, downloadUrl: 'https://example.test/x.deb' } });
  turboLoad(env);
  await flush();
  assert.ok(env.pill.classList.contains('is-outdated'), 'red when an update exists');
  assert.ok(env.pill.title.includes('9.9.9'), 'title names the newer version');
});

test('auto check runs once per session; a later turbo:load re-applies the cached state', async () => {
  const env = boot({ '/update/check': { enabled: true, current: '0.25.5', latest: '0.25.5', updateAvailable: false } });
  turboLoad(env);
  await flush();
  assert.strictEqual(env.fetch.calls.length, 1);
  // Simulate a Turbo body swap: fresh pill markup, same session.
  env.document.body.innerHTML = PILL;
  env.pill = env.document.getElementById('update-pill');
  turboLoad(env);
  await flush();
  assert.strictEqual(env.fetch.calls.length, 1, 'no second network check');
  assert.ok(env.pill.classList.contains('is-current'), 'cached state re-applied to the new pill');
});

test('no releases published: pill stays yellow with an explanatory title', async () => {
  const env = boot({ '/update/check': { enabled: true, current: '0.25.5', updateAvailable: false } });
  turboLoad(env);
  await flush();
  assert.ok(env.pill.classList.contains('is-unknown'));
  assert.ok(env.pill.title.includes('No releases'), env.pill.title);
});

test('click re-checks even when the toggle is off, and downloads when outdated', async () => {
  const env = boot({
    '/update/check': (url) => url.includes('auto=1')
      ? { enabled: false, current: '0.25.5' }
      : { enabled: true, current: '0.25.5', latest: '9.9.9', updateAvailable: true, downloadUrl: 'https://example.test/hespera_9.9.9_amd64.deb' },
  });
  turboLoad(env);
  await flush();
  assert.ok(env.pill.classList.contains('is-unknown'), 'toggle off: yellow after load');

  // jsdom can't intercept real navigation — the controller's seam can.
  let navigated = null;
  env.window.__updatePillGo = (u) => { navigated = String(u); };
  env.pill.click();
  await flush();
  assert.ok(env.fetch.calls.some((c) => !c.url.includes('auto=1')), 'click checks without the auto gate');
  assert.ok(env.pill.classList.contains('is-outdated'), 'pill turns red');
  assert.strictEqual(navigated, 'https://example.test/hespera_9.9.9_amd64.deb', 'navigates to the asset (browser download)');
});

test('click when up to date turns green and never navigates', async () => {
  const env = boot({ '/update/check': { enabled: true, current: '0.25.5', latest: '0.25.5', updateAvailable: false } });
  let navigated = null;
  env.window.__updatePillGo = (u) => { navigated = String(u); };
  env.pill.click();
  await flush();
  assert.ok(env.pill.classList.contains('is-current'));
  assert.strictEqual(navigated, null);
});

test('a non-https download URL is refused', async () => {
  const env = boot({ '/update/check': { enabled: true, current: '0.25.5', latest: '9.9.9', updateAvailable: true, downloadUrl: 'javascript:alert(1)' } });
  let navigated = null;
  env.window.__updatePillGo = (u) => { navigated = String(u); };
  env.pill.click();
  await flush();
  assert.ok(env.pill.classList.contains('is-outdated'), 'state still updates');
  assert.strictEqual(navigated, null, 'no navigation to a non-https URL');
});
