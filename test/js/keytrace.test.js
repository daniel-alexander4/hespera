// Tests for web/static/keytrace.js — the remote-input diagnostic tracer. The
// real controller runs in jsdom: it probes GET /debug/keytrace once at load,
// arms only when the server says enabled, and beacons each keydown (capture
// phase, so stopPropagation can't hide a press) with the before/after focus
// snapshot plus each turbo:load navigation.

const { test } = require('node:test');
const assert = require('node:assert');
const { loadController, makeFetch, flush } = require('./harness');

function boot({ enabled }) {
  return loadController('keytrace.js', {
    html: `<!DOCTYPE html><html><body><button id="b1">one</button><button id="b2">two</button></body></html>`,
    url: 'http://localhost/tv',
    stubs: { fetch: makeFetch({ '/debug/keytrace': { ok: true, enabled } }) },
  });
}

// Beacons decoded from the harness's sendBeacon recorder (Blob bodies —
// jsdom's Blob has no .text(), so read through its FileReader).
function blobText(env, blob) {
  return new Promise((resolve, reject) => {
    const fr = new env.window.FileReader();
    fr.onload = () => resolve(fr.result);
    fr.onerror = () => reject(fr.error);
    fr.readAsText(blob);
  });
}
async function beaconEvents(env) {
  const out = [];
  for (const b of env.window.__beacons) out.push(JSON.parse(await blobText(env, b.data)));
  return out;
}

test('disabled: probes the flag once and never traces', async () => {
  const env = boot({ enabled: false });
  await flush();
  env.document.getElementById('b1').dispatchEvent(new env.window.KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }));
  await flush(5);
  assert.strictEqual(env.fetch.calls.length, 1); // the one GET probe
  assert.strictEqual(env.window.__beacons.length, 0);
});

test('enabled: arms, announces itself, and beacons keydowns with focus context', async () => {
  const env = boot({ enabled: true });
  await flush();
  const events = await beaconEvents(env);
  assert.strictEqual(events.length, 1);
  assert.strictEqual(events[0].type, 'trace-start');

  const b1 = env.document.getElementById('b1');
  b1.focus();
  b1.dispatchEvent(new env.window.KeyboardEvent('keydown', { key: 'ArrowRight', code: 'ArrowRight', bubbles: true }));
  await flush(5);
  const all = await beaconEvents(env);
  const key = all.find((e) => e.type === 'key');
  assert.ok(key, 'key event beaconed');
  assert.strictEqual(key.key, 'ArrowRight');
  assert.strictEqual(key.target, 'button#b1');
  assert.strictEqual(key.focusBefore, 'button#b1');
  assert.strictEqual(key.url, '/tv');
});

test('enabled: keydown is seen in capture phase even when a handler stops propagation', async () => {
  const env = boot({ enabled: true });
  await flush();
  const b1 = env.document.getElementById('b1');
  b1.addEventListener('keydown', (e) => e.stopPropagation()); // an era-slider-style consumer
  b1.dispatchEvent(new env.window.KeyboardEvent('keydown', { key: 'ArrowUp', bubbles: true }));
  await flush(5);
  const all = await beaconEvents(env);
  assert.ok(all.some((e) => e.type === 'key' && e.key === 'ArrowUp'), 'stopped key still traced');
});

test('enabled: turbo:load navigations are traced', async () => {
  const env = boot({ enabled: true });
  await flush();
  env.document.dispatchEvent(new env.window.Event('turbo:load'));
  await flush();
  const all = await beaconEvents(env);
  assert.ok(all.some((e) => e.type === 'nav' && e.url === '/tv'), 'nav event beaconed');
});
