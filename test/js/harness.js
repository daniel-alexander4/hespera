// Shared jsdom harness for Hespera's browser JS (web/static/*.js).
//
// The Go binary embeds web/ and never runs Node — this harness is dev-only. It
// loads a real controller file into a jsdom window and exercises it through its
// DOM effects (dispatch turbo:load, click buttons, change selects, fire
// timeupdate), so the tests run the actual shipped code, not a copy.
//
// jsdom implements the DOM but not the media engine, so we stub what the player
// touches and jsdom lacks: HTMLMediaElement.play/pause/load/canPlayType, a mock
// hls.js, fetch, and navigator.sendBeacon. What jsdom genuinely can't model
// (VTT cue parsing, real MSE/DTS ordering, actual video decode, iframe reload)
// stays the province of the Playwright smoke — see the media-player notes.

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { JSDOM } = require('jsdom');

const STATIC_DIR = path.join(__dirname, '..', '..', 'web', 'static');

// A mock hls.js: records what the controller drives it with (startPosition,
// loadSource, the registered event handlers) so a test can assert the pipeline
// wiring and fire a fatal error to check recovery.
function makeMockHls() {
  const instances = [];
  class MockHls {
    constructor(cfg) {
      this.cfg = cfg || {};
      this.handlers = {};
      this.loadedUrl = null;
      this.attached = null;
      this.destroyed = false;
      this.startLoadCount = 0;
      this.recoverMediaCount = 0;
      this.swapAudioCount = 0;
      instances.push(this);
    }
    loadSource(u) { this.loadedUrl = u; }
    attachMedia(v) { this.attached = v; }
    on(evt, cb) { this.handlers[evt] = cb; }
    destroy() { this.destroyed = true; }
    startLoad() { this.startLoadCount++; }
    recoverMediaError() { this.recoverMediaCount++; }
    swapAudioCodec() { this.swapAudioCount++; }
    // test helper: fire a registered event
    emit(evt, data) { if (this.handlers[evt]) this.handlers[evt](evt, data); }
  }
  MockHls.isSupported = () => true;
  MockHls.Events = { MANIFEST_PARSED: 'hlsManifestParsed', ERROR: 'hlsError' };
  MockHls.ErrorTypes = { NETWORK_ERROR: 'networkError', MEDIA_ERROR: 'mediaError', OTHER_ERROR: 'otherError' };
  MockHls.instances = instances;
  return MockHls;
}

// A fetch stub driven by a { 'substring': responseObject | fn(url,opts) } router.
// The first key that is a substring of the URL wins; the value is returned as the
// parsed JSON body. Records every call for assertions.
function makeFetch(routes) {
  const calls = [];
  const fn = async (url, opts) => {
    calls.push({ url: String(url), opts: opts || {} });
    for (const key of Object.keys(routes || {})) {
      if (String(url).indexOf(key) >= 0) {
        const v = routes[key];
        const body = typeof v === 'function' ? v(String(url), opts) : v;
        return { ok: true, json: async () => body, text: async () => JSON.stringify(body) };
      }
    }
    return { ok: true, json: async () => ({ ok: false }), text: async () => '{}' };
  };
  fn.calls = calls;
  return fn;
}

// Load a controller file into a fresh jsdom window built around `html`, with the
// browser-API stubs injected, then return handles for assertions. `stubs` lets a
// test add/override globals (e.g. window.YT) before the script runs.
function loadController(file, { html, url = 'http://localhost/', stubs = {}, storage = {} } = {}) {
  const dom = new JSDOM(html, { url, runScripts: 'outside-only', pretendToBeVisual: true });
  const { window } = dom;

  // Seed localStorage before the script runs — the players read persisted prefs
  // (volume, boost, skip-auto) at init.
  for (const k of Object.keys(storage)) window.localStorage.setItem(k, storage[k]);

  // jsdom leaves these unimplemented on HTMLMediaElement — stub so the player's
  // play/load/canPlayType calls don't throw. Track load/pause for assertions.
  const mediaProto = window.HTMLMediaElement.prototype;
  Object.defineProperty(mediaProto, 'play', { configurable: true, value: function () { this.paused = false; this.dispatchEvent(new window.Event('play')); return Promise.resolve(); } });
  Object.defineProperty(mediaProto, 'pause', { configurable: true, value: function () { this.paused = true; this.dispatchEvent(new window.Event('pause')); } });
  Object.defineProperty(mediaProto, 'load', { configurable: true, value: function () {} });
  Object.defineProperty(mediaProto, 'canPlayType', { configurable: true, value: function () { return ''; } });

  // Media Session: jsdom has no navigator.mediaSession, so provide a recording
  // stub — registered action handlers land in window.__mediaSessionHandlers so a
  // test can invoke them like Chrome would on a hardware media key. MediaMetadata
  // is stubbed alongside (player.js constructs it whenever mediaSession exists).
  const msHandlers = {};
  window.navigator.mediaSession = stubs.mediaSession || {
    playbackState: 'none',
    metadata: null,
    setActionHandler: (action, handler) => { msHandlers[action] = handler; },
  };
  window.__mediaSessionHandlers = msHandlers;
  if (!window.MediaMetadata) {
    window.MediaMetadata = class { constructor(m) { Object.assign(this, m || {}); } };
  }

  // fetch + sendBeacon (network); default no-op fetch if a test doesn't set one.
  window.fetch = stubs.fetch || makeFetch({});
  const beacons = [];
  window.navigator.sendBeacon = (u, data) => { beacons.push({ url: String(u), data }); return true; };
  window.__beacons = beacons;

  // Apply any extra per-test stubs (Hls, YT, ...).
  for (const k of Object.keys(stubs)) { if (k !== 'fetch') window[k] = stubs[k]; }

  // Run the real controller source in the window's context.
  const src = fs.readFileSync(path.join(STATIC_DIR, file), 'utf8');
  vm.runInContext(src, dom.getInternalVMContext());

  return { dom, window, document: window.document, fetch: window.fetch, beacons };
}

// Flush pending microtasks/timers so an awaited fetch chain inside the controller
// settles before assertions. A couple of macrotask turns covers fetch().then().
const tick = () => new Promise((r) => setTimeout(r, 0));
async function flush(n = 3) { for (let i = 0; i < n; i++) await tick(); }

module.exports = { loadController, makeMockHls, makeFetch, flush, STATIC_DIR };
