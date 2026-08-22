// hesplay's phone remote (cmd/hesplay/web/app.js) — the sign-in state machine.
//
// This file exists because a passphrase gate put a state machine into a
// 1,254-line front end that had ZERO automated coverage. It loads the real
// index.html and the real app.js, so it exercises shipped code rather than a
// copy of its logic.
//
// Ceiling, stated rather than implied: jsdom is not a phone. Whether an iOS
// keyboard honours autocapitalize="none", whether the cookie survives a PWA
// relaunch, and whether the TV's arrow-key ring behaves are Playwright/live
// territory. What is testable here is the machine: who shows the card, what it
// says, what it stops, and what it restores.

const test = require('node:test');
const assert = require('node:assert');
const fs = require('node:fs');
const path = require('node:path');
const { loadController, flush, HESPLAY_DIR } = require('./harness.js');

const INDEX = fs.readFileSync(path.join(HESPLAY_DIR, 'index.html'), 'utf8');

// boot loads the real app.js against the real markup with a scripted fetch.
// `router` maps a URL substring to { status, body } (or a function).
function boot(router, beforeBoot) {
  const calls = [];
  const fetchStub = async (url, opts) => {
    calls.push({ url: String(url), opts: opts || {} });
    for (const key of Object.keys(router)) {
      if (String(url).includes(key)) {
        const r = typeof router[key] === 'function' ? router[key](String(url), opts) : router[key];
        const status = r.status === undefined ? 200 : r.status;
        return {
          ok: status >= 200 && status < 300,
          status,
          json: async () => (r.body === undefined ? {} : r.body),
        };
      }
    }
    return { ok: false, status: 404, json: async () => ({ ok: false, message: 'no route' }) };
  };
  const { window } = loadController('app.js', {
    dir: HESPLAY_DIR,
    html: INDEX,
    stubs: { fetch: fetchStub },
  });
  window.fetch = fetchStub;
  if (beforeBoot) beforeBoot(window);
  window.document.dispatchEvent(new window.Event('DOMContentLoaded'));
  return { window, calls };
}

const GATED = { status: 401, body: { ok: false, message: 'sign in required' } };

test('a 401 anywhere raises the sign-in card and hides the app', async () => {
  const { window } = boot({ '/api/': GATED });
  await flush();
  const doc = window.document;
  assert.strictEqual(doc.getElementById('signin').hidden, false, 'the card should be up');
  // Hidden, not merely covered: display:none is what removes these from the
  // focus order, which is what stops the TV ring walking onto live controls.
  for (const m of doc.querySelectorAll('main')) {
    assert.strictEqual(m.hidden, true, 'every <main> should be hidden behind the card');
  }
  assert.strictEqual(doc.querySelector('.bar').hidden, true, 'the top bar should be hidden');
  assert.strictEqual(doc.getElementById('now').hidden, true, 'the transport should be hidden');
  assert.ok(doc.body.classList.contains('signing-in'));
  // It names the box — the real support call is "wrong Pi", not "wrong words".
  assert.ok(doc.getElementById('signin-host').textContent.length > 0, 'the card should name the host');
});

test('the card is raised once, not once per failing call', async () => {
  // Several calls fail at boot (three loaders plus refresh). A re-entrant
  // showSignIn would re-focus the field on each, re-popping the phone keyboard
  // over and over.
  //
  // The stub must be installed BEFORE DOMContentLoaded: an earlier version of
  // this test stubbed focus afterwards, so the boot storm had already run and
  // it passed with the guard deleted — mutation-proven useless.
  let focusCount = 0;
  const { window, calls } = boot({ '/api/': GATED }, (w) => {
    w.HTMLElement.prototype.focus = function () { focusCount++; };
  });
  await flush();
  const failed = calls.filter((c) => c.url.includes('/api/')).length;
  assert.ok(failed >= 3, `stimulus: expected several failing boot calls, saw ${failed}`);
  assert.strictEqual(window.document.getElementById('signin').hidden, false, 'stimulus: the card must be up');
  assert.strictEqual(focusCount, 1,
    `showSignIn re-entered: the field was focused ${focusCount} times across ${failed} failing calls`);
});

test('polling stops while signed out', async () => {
  const { window, calls } = boot({ '/api/': GATED });
  await flush();
  const before = calls.filter((c) => c.url.includes('/api/state')).length;
  assert.ok(before > 0, 'stimulus: at least one state poll must have happened');
  // Advance well past POLL_MS (2000ms).
  await new Promise((r) => setTimeout(r, 250));
  window.document.dispatchEvent(new window.Event('visibilitychange'));
  await flush();
  const after = calls.filter((c) => c.url.includes('/api/state')).length;
  assert.strictEqual(after, before, 'a signed-out visibilitychange re-polled');
});

test('503 says the box has no passphrase and disables the field', async () => {
  const { window } = boot({
    '/api/': { status: 503, body: { ok: false, message: 'no passphrase is configured on this box — see the server log' } },
  });
  await flush();
  const doc = window.document;
  assert.strictEqual(doc.getElementById('signin').hidden, false);
  assert.match(doc.getElementById('signin-msg').textContent, /no passphrase/i);
  assert.strictEqual(doc.getElementById('passphrase').disabled, true,
    'a box with no passphrase must not offer a field that can never work');
});

test('a wrong passphrase reports inline and never via the toast', async () => {
  const { window } = boot({
    '/api/login': { status: 401, body: { ok: false, message: 'that is not the passphrase' } },
    '/api/': GATED,
  });
  await flush();
  const doc = window.document;
  doc.getElementById('passphrase').value = 'wrong wrong wrong';
  doc.getElementById('signin-go').click();
  await flush();
  assert.match(doc.getElementById('signin-msg').textContent, /not the passphrase/i);
  // The toast is silenced while signed out, so a login error routed through it
  // would be INVISIBLE. That is the whole reason the card owns its own line.
  assert.strictEqual(doc.getElementById('toast').hidden, true, 'login errors must not use the toast');
});

test('429 is visible — the common case after a few wrong tries', async () => {
  const { window } = boot({ '/api/login': { status: 429, body: { ok: false, message: 'too many attempts' } }, '/api/': GATED });
  await flush();
  const doc = window.document;
  doc.getElementById('passphrase').value = 'correct horse battery';
  doc.getElementById('signin-go').click();
  await flush();
  assert.match(doc.getElementById('signin-msg').textContent, /too many attempts/i);
  assert.strictEqual(doc.getElementById('signin-go').disabled, true, 'the button should back off after a 429');
});

test('a successful sign-in tears the card down and resumes the app', async () => {
  let signedIn = false;
  const { window } = boot({
    '/api/login': () => { signedIn = true; return { status: 200, body: { ok: true } }; },
    '/api/state': () => (signedIn
      ? { status: 200, body: { ok: true, playing: false, now: {}, queue: [], auth: {} } }
      : GATED),
    '/api/': () => (signedIn ? { status: 200, body: { ok: true } } : GATED),
  });
  await flush();
  const doc = window.document;
  assert.strictEqual(doc.getElementById('signin').hidden, false, 'stimulus: the card must be up first');
  doc.getElementById('passphrase').value = 'correct horse battery';
  doc.getElementById('signin-go').click();
  await flush();
  await flush();
  assert.strictEqual(doc.getElementById('signin').hidden, true, 'the card should be gone');
  assert.strictEqual(doc.querySelector('.bar').hidden, false, 'the bar should be back');
  assert.strictEqual(doc.body.classList.contains('signing-in'), false);
  assert.strictEqual(doc.getElementById('passphrase').value, '', 'the passphrase must not linger in the field');
});

test('a login that succeeds but whose cookie is dropped does not loop', async () => {
  // The browser accepts the login and then refuses to keep the cookie (private
  // tab, lockdown mode). Without the probe this hides the card, 401s, and shows
  // it again — indistinguishable from a wrong passphrase, forever.
  const { window } = boot({
    '/api/login': { status: 200, body: { ok: true } },
    '/api/': GATED,
  });
  await flush();
  const doc = window.document;
  doc.getElementById('passphrase').value = 'correct horse battery';
  doc.getElementById('signin-go').click();
  await flush();
  await flush();
  assert.strictEqual(doc.getElementById('signin').hidden, false, 'the card must stay up');
  assert.match(doc.getElementById('signin-msg').textContent, /did not keep the session cookie/i);
});

test('503 at the login endpoint does not claim a wrong passphrase', async () => {
  // The phase review found submitSignIn had grown a SECOND taxonomy for one
  // status: api() routed 503 to the no-passphrase card while a 503 from
  // /api/login printed "That is not the passphrase" — two contradictory answers
  // to the same server state.
  const { window } = boot({
    '/api/login': { status: 503, body: { ok: false, message: 'no passphrase is configured on this box — see the server log' } },
    '/api/': GATED,
  });
  await flush();
  const doc = window.document;
  doc.getElementById('passphrase').value = 'correct horse battery';
  doc.getElementById('signin-go').click();
  await flush();
  const msg = doc.getElementById('signin-msg').textContent;
  assert.match(msg, /no passphrase/i, `503 reported as: ${msg}`);
  assert.doesNotMatch(msg, /not the passphrase/i, 'a 503 was reported as a wrong passphrase');
});
