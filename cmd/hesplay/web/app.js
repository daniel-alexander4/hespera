// app.js — the phone remote for hesplay.
//
// Every button is one POST to the binary serving this page; the audio comes out
// of that box's speakers, never the phone's. So there is no <audio> element
// here, no Media Session, and no background-playback problem to solve: locking
// the phone stops nothing, because the phone was never playing.
//
// State is polled rather than pushed. A websocket would be tidier, but polling
// survives the phone sleeping and waking with no reconnect logic, and the whole
// payload is one small JSON object.
(function () {
  'use strict';

  const $ = (id) => document.getElementById(id);
  const POLL_MS = 2000;

  // --- plumbing ----------------------------------------------------------

  async function api(path, opts) {
    const res = await fetch(path, Object.assign({ headers: { 'Content-Type': 'application/json' } }, opts));
    let body = null;
    try { body = await res.json(); } catch (_) { /* a proxy error page, not JSON */ }
    if (!res.ok || (body && body.ok === false)) {
      // The status rides along. Without it 401 "sign in", 503 "this box has no
      // passphrase" and a 502 upstream failure are one indistinguishable
      // string, and they need three different screens.
      const e = new Error((body && body.message) || ('HTTP ' + res.status));
      e.status = res.status;
      e.body = body;
      // One choke point for every one of the callers: the gate states raise the
      // overlay from here rather than from sixteen catch blocks.
      if (res.status === 401) showSignIn();
      else if (res.status === 503) showSignIn({ noPassphrase: true, detail: e.message });
      throw e;
    }
    return body || {};
  }

  // isGateError marks the two states the overlay owns. Callers use it to stay
  // quiet: their inline "could not reach the server" surfaces would otherwise
  // paint "sign in required" behind the sign-in card.
  function isGateError(e) { return e && (e.status === 401 || e.status === 503); }

  // Icons are <use> references into the sprite in index.html — the same Lucide
  // paths Hespera uses. createElementNS is required: SVG elements built with
  // createElement land in the HTML namespace and render as nothing.
  const SVG_NS = 'http://www.w3.org/2000/svg';
  function icon(name) {
    const svg = document.createElementNS(SVG_NS, 'svg');
    svg.setAttribute('class', 'icon');
    svg.setAttribute('aria-hidden', 'true');
    const use = document.createElementNS(SVG_NS, 'use');
    use.setAttribute('href', '#i-' + name);
    svg.appendChild(use);
    return svg;
  }

  // Repoint an existing icon rather than rebuilding the button.
  function setIcon(el, name) {
    const use = el && el.querySelector('use');
    if (use) use.setAttribute('href', '#i-' + name);
  }

  let toastTimer = 0;
  function toast(msg, isErr) {
    // While signed out every in-flight call fails at once; four stacked error
    // toasts behind the sign-in card help nobody. Sign-in's OWN errors never
    // come through here — they use the card's inline #signin-msg, which is why
    // silencing this cannot hide them (a 429 after eight wrong tries is the
    // common case and must be visible).
    if (signedOut) return;
    const el = $('toast');
    el.textContent = msg;
    el.classList.toggle('err', !!isErr);
    el.hidden = false;
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => { el.hidden = true; }, 3200);
  }

  // --- playback ----------------------------------------------------------

  // id addresses a queue directly; name goes through the server's search. The
  // app always knows the id, so it always sends id — a playlist called "2016"
  // must not be resolved by searching for its own name.
  async function play(source, id, shuffle, startAt) {
    try {
      await api('/api/play', {
        method: 'POST',
        body: JSON.stringify({
          source: source, id: id || 0, shuffle: !!shuffle, startAt: startAt || 0,
        }),
      });
      toQueueView();
      refresh(); // don't wait for the next poll tick to show what started
    } catch (e) {
      toast(e.message, true);
    }
  }

  // Jump to a tapped row. Optimistic: move the highlight straight away so the
  // tap feels immediate, then let the next poll confirm (or correct) it.
  async function jump(index) {
    lastQueueKey = ''; // force a re-render even if the poll lands identical rows
    try {
      await api('/api/jump', { method: 'POST', body: JSON.stringify({ index: index }) });
      refresh();
    } catch (e) {
      toast(e.message, true);
    }
  }

  // Pause/resume the running engine. Explicit rather than a bare toggle so a
  // retry after a dropped response cannot flip it back the other way.
  async function setPaused(want) {
    try {
      const r = await api('/api/pause', { method: 'POST', body: JSON.stringify({ paused: want }) });
      applyPaused(!!r.paused);
      refresh();
    } catch (e) {
      toast(e.message, true);
    }
  }

  let isPaused = false;
  function applyPaused(p) {
    isPaused = p;
    // Either or both may be on screen: the card's button on home, the footer's
    // while browsing.
    ['row-pause', 'pause'].forEach((id) => {
      const b = $(id);
      if (!b) return;
      setIcon(b, p ? 'play' : 'pause');
      b.setAttribute('aria-label', p ? 'Resume' : 'Pause');
    });
  }

  async function action(path) {
    try {
      await api(path, { method: 'POST' });
      refresh();
    } catch (e) {
      toast(e.message, true);
    }
  }

  // --- rendering ---------------------------------------------------------

  // textContent only, never innerHTML: playlist names are library data and a
  // name containing markup must render as that name, not as markup.
  function playlistRow(p) {
    const wrap = document.createElement('div');
    wrap.className = 'pl';

    const name = document.createElement('div');
    name.className = 'name';
    const b = document.createElement('b');
    b.textContent = p.name;
    const count = document.createElement('span');
    count.className = 'count';
    count.textContent = p.count === 1 ? '1 track' : p.count + ' tracks';
    name.append(b, count);

    const playBtn = document.createElement('button');
    playBtn.className = 'btn go';
    playBtn.type = 'button';
    playBtn.appendChild(icon('play'));
    playBtn.setAttribute('aria-label', 'Play ' + p.name);
    playBtn.addEventListener('click', () => play('playlist', p.id, false));

    const shufBtn = document.createElement('button');
    shufBtn.className = 'btn go';
    shufBtn.type = 'button';
    shufBtn.appendChild(icon('shuffle'));
    shufBtn.setAttribute('aria-label', 'Shuffle ' + p.name);
    shufBtn.addEventListener('click', () => play('playlist', p.id, true));

    wrap.append(name, playBtn, shufBtn);
    return wrap;
  }

  async function loadPlaylists() {
    const box = $('playlists');
    try {
      const r = await api('/api/playlists');
      const rows = r.playlists || [];
      box.textContent = '';
      rows.forEach((p) => box.appendChild(playlistRow(p)));
      $('pl-empty').hidden = rows.length > 0;
    } catch (e) {
      box.textContent = '';
      if (isGateError(e)) return; // the sign-in card is up; do not paint behind it
      $('pl-empty').hidden = false;
      $('pl-empty').textContent = 'Could not reach the server: ' + e.message;
    }
  }

  let lastTrackID = -1;
  function renderNow(st) {
    const now = $('now');
    if (!st.playing || !st.now || !st.now.title) {
      now.hidden = true;
      lastTrackID = -1;
      return;
    }
    const n = st.now;
    const isNoise = st.kind === 'noise';
    // The footer exists for the screens that have no queue list. On home the
    // playing row IS the transport for a queue, so a second copy below the fold
    // would be two sets of controls for one song — but noise has no queue card,
    // so its footer is the only transport and shows everywhere, home included.
    now.hidden = stack[stack.length - 1] === 'home' && !isNoise;
    // Noise is one endless sound: there is nothing to skip to in either direction.
    $('prev').hidden = isNoise;
    $('next').hidden = isNoise;
    // canPause is false on an ffplay box, which has no IPC to pause through —
    // hide the control rather than offer one that can only fail. (For noise the
    // server answers it from the signal seam instead.)
    $('pause').hidden = !st.canPause;
    applyPaused(!!st.paused);
    $('np-title').textContent = n.title;
    const bits = [n.artist, n.album].filter(Boolean).join(' — ');
    const pos = n.total > 1 ? ' (' + n.index + '/' + n.total + ')' : '';
    $('np-sub').textContent = bits + pos;

    // Only swap the <img> when the track actually changed: reassigning src on
    // every 2s poll would restart the fetch and flicker the artwork.
    if (n.id !== lastTrackID) {
      lastTrackID = n.id;
      const art = $('art');
      // /art/ is gated now, so a stale or signed-out fetch 401s; without this
      // the element shows a broken-image glyph and the JS still believes there
      // is artwork.
      art.onerror = () => { art.hidden = true; };
      if (n.albumId > 0) {
        art.src = '/art/album/' + n.albumId;
        art.hidden = false;
      } else {
        art.removeAttribute('src'); // a track with no album has no art to show
        art.hidden = true;
      }
    }
  }

  // The queue window. Re-rendered only when the rows actually change, because
  // this runs on every 2s poll and rebuilding a 20-row list each tick would
  // fight the user's scroll position.
  let lastQueueKey = '';
  function renderQueue(st) {
    const sec = $('queue-sec');
    const rows = (st && st.queue) || [];
    if (!st.playing || rows.length === 0) {
      sec.hidden = true;
      lastQueueKey = '';
      return;
    }
    sec.hidden = false;
    $('queue-h').textContent = st.now && st.now.queue
      ? st.now.queue + ' · ' + st.now.index + ' of ' + st.now.total
      : 'Playing';

    const key = rows.map((r) => r.index + (r.current ? '*' : '')).join(',');
    if (key === lastQueueKey) return;
    lastQueueKey = key;

    const list = $('queue');
    list.textContent = '';
    rows.forEach((r) => {
      const li = document.createElement('li');

      // A whole row is the touch target — a 44px-tall <button> spanning it,
      // rather than a tappable <li>, so it is reachable by keyboard and
      // announced as an action rather than as a list item.
      const btn = document.createElement('button');
      btn.type = 'button';
      btn.className = 'q-row' + (r.current ? ' is-now' : (st.now && r.index < st.now.index ? ' is-past' : ''));
      btn.setAttribute('aria-label', 'Play ' + r.title + ' by ' + r.artist);

      const n = document.createElement('span');
      n.className = 'q-n';
      n.textContent = String(r.index); // the playing row is the card, never this

      const t = document.createElement('span');
      t.className = 'q-t';
      t.textContent = r.title; // textContent: titles are library data, never markup
      const a = document.createElement('span');
      a.className = 'q-a';
      a.textContent = r.artist;
      t.appendChild(a);

      btn.append(n, t);
      btn.dataset.title = r.title;
      if (r.current) {
        // The playing row is a card: cover, title, progress and the transport,
        // sitting where the song is rather than in a bar below the whole list.
        li.appendChild(nowCard(st, r));
      } else {
        btn.addEventListener('click', () => jump(r.index));
        li.appendChild(btn);
      }
      list.appendChild(li);
    });
  }

  // nowCard renders the playing song with its own controls. Rebuilt only when
  // the row set changes; the progress bar and pause glyph are updated in place
  // on every poll so a 2s tick never rebuilds the DOM under a finger.
  function nowCard(st, r) {
    const n = st.now || {};
    const card = document.createElement('div');
    card.className = 'np-card';
    card.setAttribute('aria-current', 'true');

    const head = document.createElement('div');
    head.className = 'np-head';
    const cover = document.createElement('img');
    cover.className = 'np-cover';
    cover.id = 'np-cover';
    cover.alt = '';
    cover.onerror = () => { cover.hidden = true; };
    if (n.albumId > 0) cover.src = '/art/album/' + n.albumId; else cover.hidden = true;
    const text = document.createElement('div');
    text.className = 'np-text';
    const t = document.createElement('div');
    t.className = 'np-t';
    t.textContent = r.title;
    const a = document.createElement('div');
    a.className = 'np-a';
    a.textContent = [n.artist, n.album].filter(Boolean).join(' — ');
    text.append(t, a);
    head.append(cover, text);

    const bar = document.createElement('div');
    bar.className = 'np-bar';
    bar.id = 'np-bar';
    bar.appendChild(document.createElement('i'));

    const ctl = document.createElement('div');
    ctl.className = 'np-controls';
    const mk = (id, label, glyph, fn) => {
      const b = document.createElement('button');
      b.type = 'button';
      b.className = 'icon-btn';
      b.id = id;
      b.setAttribute('aria-label', label);
      b.appendChild(icon(glyph));
      b.addEventListener('click', fn);
      return b;
    };
    ctl.append(
      mk('row-prev', 'Previous', 'skip-back', () => action('/api/prev')),
      mk('row-pause', isPaused ? 'Resume' : 'Pause', isPaused ? 'play' : 'pause', () => setPaused(!isPaused)),
      mk('row-next', 'Next', 'skip-forward', () => action('/api/next')),
      mk('row-stop', 'Stop', 'square', () => action('/api/stop')),
    );
    if (!st.canPause) ctl.querySelector('#row-pause').hidden = true;

    card.append(head, bar, ctl);
    return card;
  }

  // Update the bar in place — no re-render, so it can tick every poll.
  function applyProgress(p) {
    const bar = $('np-bar');
    if (!bar) return;
    const known = typeof p === 'number' && p >= 0;
    bar.classList.toggle('is-unknown', !known);
    bar.firstChild.style.width = known ? (Math.min(1, p) * 100).toFixed(1) + '%' : '0%';
  }

  // --- sign-in -----------------------------------------------------------

  let signedOut = false;
  let pollTimer = 0;

  // startPoll always clears first: hideSignIn can be reached twice (a second
  // login landing while the first is settling), and a second interval would be
  // unstoppable — nothing holds its handle.
  function startPoll() {
    clearInterval(pollTimer);
    pollTimer = setInterval(refresh, POLL_MS);
  }

  function signinMsg(text, isErr) {
    const el = $('signin-msg');
    el.textContent = text || '';
    el.classList.toggle('err', !!isErr);
  }

  function showSignIn(opts) {
    const o = opts || {};
    // Re-entrant by construction: the four boot loaders plus refresh() all fail
    // at once, and visibilitychange fires refresh() unconditionally. Without
    // this guard the field is re-focused — re-popping the phone keyboard — on
    // every one of them.
    if (signedOut) {
      if (o.noPassphrase) signinMsg(o.detail || 'This box has no passphrase set.', true);
      return;
    }
    signedOut = true;
    clearInterval(pollTimer);
    pollTimer = 0;

    // Hide the app, do not merely cover it. `hidden` is display:none (app.css's
    // [hidden] rule), which takes these out of the FOCUS ORDER — otherwise the
    // TV's arrow-key ring walks behind the card onto live play buttons. This is
    // the same reason the video player's fullscreen header uses display:none
    // and never visibility:hidden.
    document.querySelectorAll('main').forEach((m) => { m.hidden = true; });
    document.querySelector('.bar').hidden = true;
    $('now').hidden = true;
    $('settings').hidden = true;
    document.body.classList.add('signing-in');

    // Forget what was painted. lastQueueKey is indices-only, so a different
    // album at the same position would not repaint; lastTrackID pins #art to a
    // URL that just 401'd and got onerror-hidden.
    lastTrackID = -1;
    lastQueueKey = '';

    $('signin-host').textContent = location.host;
    signinMsg(o.noPassphrase ? (o.detail || 'This box has no passphrase set.') : '', !!o.noPassphrase);
    $('passphrase').disabled = !!o.noPassphrase;
    $('signin-go').disabled = !!o.noPassphrase;
    $('signin').hidden = false;
    try { $('passphrase').focus(); } catch (_) { /* not focusable yet; harmless */ }
  }

  function hideSignIn() {
    if (!signedOut) return;
    signedOut = false;
    $('signin').hidden = true;
    document.body.classList.remove('signing-in');
    document.querySelector('.bar').hidden = false;
    $('passphrase').value = '';
    signinMsg('');
    // showScreen restores the right <main> AND derives #back-btn from the stack
    // depth — un-hiding the button directly would show a Back that does nothing.
    showScreen(stack[stack.length - 1], $('bar-title').textContent);
    loadPlaylists();
    loadNoiseQuick();
    loadLetters();
    refresh();
    startPoll();
  }

  let signingIn = false;
  async function submitSignIn() {
    if (signingIn) return;
    const phrase = $('passphrase').value;
    if (!phrase.trim()) return;
    signingIn = true;
    $('signin-go').disabled = true;
    signinMsg('Signing in…');
    try {
      const res = await fetch('/api/login', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ passphrase: phrase }),
      });
      if (res.status === 429) {
        signinMsg('Too many attempts — wait a few seconds.', true);
        setTimeout(() => { $('signin-go').disabled = false; }, 4000);
        signingIn = false;
        return;
      }
      if (!res.ok) {
        signinMsg('That is not the passphrase.', true);
        $('signin-go').disabled = false;
        signingIn = false;
        return;
      }
      // Prove the cookie STUCK before tearing the card down. A browser that
      // refused to keep it (private tab, lockdown mode, a profile that drops
      // cookies on a non-secure origin) would otherwise loop: hide, reload,
      // 401, show — indistinguishable to the user from a wrong passphrase.
      const probe = await fetch('/api/state');
      if (!probe.ok) {
        signinMsg('Signed in, but this browser did not keep the session cookie.', true);
        $('signin-go').disabled = false;
        signingIn = false;
        return;
      }
      signingIn = false;
      hideSignIn();
    } catch (e) {
      signinMsg('Could not reach the box: ' + e.message, true);
      $('signin-go').disabled = false;
      signingIn = false;
    }
  }

  async function refresh() {
    // Nothing to poll while the card is up, and visibilitychange calls this
    // unconditionally.
    if (signedOut) return;
    try {
      const st = await api('/api/state');
      renderNow(st);
      renderQueue(st);
      applyPaused(!!st.paused);
      applyProgress(st.progress);
    } catch (_) {
      // A failed poll is normal when the box is briefly unreachable; the next
      // tick recovers and an error toast every 2s would be unusable.
    }
  }


  // --- noise -------------------------------------------------------------
  //
  // The noise engine runs on this box, so this screen edits a config file that
  // lives here too — nothing about it depends on the Hespera server being up,
  // which is the point of a thing that has to make sound at 3am.

  // Mirrors noiseTypes in noise.go. The last three are generated by ffmpeg and
  // piped into SoX — no SoX version can synthesize them — but that is invisible
  // here: the server decides how a colour is produced.
  const NOISE_TYPES = ['brownnoise', 'pinknoise', 'whitenoise', 'tpdfnoise',
    'bluenoise', 'violetnoise', 'velvetnoise'];
  const DAY_NAMES = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];
  // The tuning knobs, in the order they matter to the ear. `wave` is the swell
  // rate — the thing that makes it sound like surf rather than a fan — and
  // `depth` is how far it swells, which is what "amplitude" meant in the script
  // this replaces.
  const NOISE_FIELDS = [
    ['waveSpeed', 'Swell rate (Hz)', 0.001, 0.2, 0.001],
    ['waveDepth', 'Swell depth (%)', 0, 100, 1],
    // The second, slower swell — the incoming larger wave. 0 = off. Its colour
    // (same stream vs its own layer) is the select above the fields.
    ['wave2Speed', '2nd swell rate (Hz, 0=off)', 0, 0.2, 0.001],
    ['wave2Depth', '2nd swell depth (%)', 0, 100, 1],
    ['wave2Gain', '2nd layer gain (dB)', -60, 20, 0.5],
    ['wave2Offset', '2nd swell offset (s)', 0, 300, 0.25],
    ['centerHz', 'Band centre (Hz)', 20, 20000, 1],
    ['widthHz', 'Band width (Hz)', 1, 20000, 1],
    ['reverb', 'Reverb (%)', 0, 100, 1],
    ['gainDb', 'Gain (dB)', -60, 20, 0.5],
    ['fadeIn', 'Fade in (s)', 0, 300, 1],
    // The entrainment pulse: a throb of the whole mix, fast enough to be a
    // rhythm rather than a wave. 0 = off, which is every coast preset; theta
    // (4-7 Hz) is the hypnagogic band. Depth is deliberately shallow — deep
    // enough to follow, not deep enough to read as a beep.
    ['pulseHz', 'Pulse rate (Hz, 0=off)', 0, 40, 0.5],
    ['pulseDepth', 'Pulse depth (%)', 0, 100, 1],
  ];
  // An extra layer's knobs — a wave of its own, in its own process, so it has
  // its own everything. Size is the SWELL: a 0.02 Hz swell is one wave every 50
  // seconds (large), 0.08 Hz one every 12.5 (small). Band left at 0 means "the
  // same sea as the body", which the server fills in.
  // A carrier pair's knobs. Shown instead of the noise fields, because band and
  // swell do nothing on a pure tone — the stage skips them.
  const TONE_FIELDS = [
    ['carrierHz', 'Carrier (Hz, left ear)', 40, 1000, 1],
    ['beatHz', 'Beat (Hz)', 0.5, 40, 0.5],
    ['gain', 'Gain (dB)', -60, 20, 0.5],
  ];
  // An event layer's knobs: how often a transient fires and how fast it decays.
  // Rate is a TEXTURE knob, not a volume one — the generator scales each event by
  // the expected overlap, so denser rain is not louder rain.
  const EVENT_FIELDS = [
    ['eventsPerSec', 'Events per second', 0.1, 2000, 1],
    ['decayMs', 'Decay (ms)', 1, 2000, 1],
    ['gain', 'Gain (dB)', -60, 20, 0.5],
    ['centerHz', 'Band centre (Hz, 0=same)', 0, 20000, 1],
    ['widthHz', 'Band width (Hz, 0=same)', 0, 20000, 1],
  ];
  // `swell` turns `speed` from a surf swell into a fast SWING riding a slow
  // envelope, and the server clamps it to 0.5-40 Hz on that basis — so the rate
  // field has to reach 40, not 0.2, or the editor could not express a pulse
  // layer it is perfectly happy to play. Both swell fields are listed even
  // though most layers leave them at 0: a field on screen at 0 is a knob, a
  // field absent while the value is set is a mystery.
  const LAYER_FIELDS = [
    ['speed', 'Rate (Hz, 0=still)', 0, 40, 0.001],
    ['depth', 'Depth (%)', 0, 100, 1],
    ['swell', 'Rides a slow swell (Hz, 0=none)', 0, 0.2, 0.001],
    ['swellDepth', 'Slow swell depth (%)', 0, 100, 1],
    ['gain', 'Gain (dB)', -60, 20, 0.5],
    ['offset', 'Phase offset (s)', 0, 300, 0.0833],
    ['centerHz', 'Band centre (Hz, 0=same)', 0, 20000, 1],
    ['widthHz', 'Band width (Hz, 0=same)', 0, 20000, 1],
  ];
  // The three cards, in the server's order (noiseCategories in noise.go).
  const NOISE_CARDS = [
    ['nature', 'Nature', 'noise-nature', 'nature-card', 'nature-h'],
    ['noise', 'Noise', 'noise-noise', 'noise-card', 'noise-h'],
    ['pulse', 'Pulse', 'noise-pulse', 'pulse-card', 'pulse-h'],
    ['binaural', 'Binaural', 'noise-binaural', 'binaural-card', 'binaural-h'],
    ['monaural', 'Monaural', 'noise-monaural', 'monaural-card', 'monaural-h'],
  ];
  // Mirrors deriveCategory in noise.go, for a config saved before `category`
  // existed: the server fills the field on its next write, but the phone has to
  // render what is on the box right now.
  function presetCard(p) {
    if (NOISE_CARDS.some((c) => c[0] === p.category)) return p.category;
    // Mechanism, and binaural first: a preset mixing both forms files under the
    // stricter requirement, since one layer needing headphones makes the whole
    // preset need them. Mirrors deriveCategory in noise.go.
    if (needsHeadphones(p)) return 'binaural';
    if ((p.layers || []).some((l) => (l.beatHz || 0) > 0 && l.monaural)) return 'monaural';
    if ((p.pulseHz || 0) > 0 || (p.phases || []).length) return 'pulse';
    if ((p.layers || []).length || (p.wave2Speed || 0) > 0) return 'nature';
    return 'noise';
  }

  // Only a binaural preset actually requires headphones; a pulse one does not,
  // and neither does a MONAURAL pair — both its tones go to both ears and the
  // beat happens in the air. Mirrors needsHeadphones in noise.go, and the
  // monaural clause is why this pair has to be kept in step: without it the phone
  // told you to fetch headphones for the one preset built for speakers.
  function needsHeadphones(p) {
    return (p.layers || []).some((l) => (l.beatHz || 0) > 0 && !l.monaural);
  }

  // Mirrors noiseMaxLayers in noise.go: each layer is a process and a buffered
  // loop, so the Add button stops rather than letting a phone tap build a fleet.
  const MAX_LAYERS = 4;

  let noiseCfg = null;

  function noiseStart(preset) {
    api('/api/noise/start', { method: 'POST', body: JSON.stringify({ preset: preset }) })
      .then(() => { toast(preset + ' noise'); toQueueView(); })
      .catch((e) => toast(e.message, true));
  }

  // The home preset cards: collapsed, Play one tap away in the summary, the
  // full editor a tap-to-expand away (Dan, 2026-08-07 — the tiles used to be
  // bare play buttons with the editor a screen deeper). Rebuilt whenever the
  // config is (re)read so a renamed preset can't leave a stale card behind.
  function renderNoiseQuick() {
    const presets = noiseCfg.presets || [];
    NOISE_CARDS.forEach(([key, label, boxId, cardId, headId]) => {
      const box = $(boxId);
      if (!box) return;
      box.textContent = '';
      const mine = presets.filter((p) => presetCard(p) === key);
      mine.forEach((p) => box.appendChild(presetRow(p)));
      // The count in the summary is the point of a collapsed card: it says
      // whether opening it is worth a tap.
      const head = $(headId);
      if (head) head.textContent = mine.length ? label + ' \u00b7 ' + mine.length : label;
      // An empty card is noise on a phone screen, so it goes away entirely —
      // a box with no binaural preset shouldn't advertise a binaural card.
      const card = $(cardId);
      if (card) card.hidden = mine.length === 0;
    });
  }

  // The front page's schedule card: a readable view of the saved windows —
  // days, times, preset — with editing a screen deeper behind the card's
  // button. The summary carries the window count so the collapsed card says
  // whether a schedule exists at all.
  function renderScheduleCard() {
    const box = $('schedule-view');
    if (!box) return;
    box.textContent = '';
    const wins = noiseCfg.schedule || [];
    $('schedule-h').textContent = wins.length
      ? 'Noise schedule \u00b7 ' + wins.length
      : 'Noise schedule';
    if (!wins.length) {
      const pmsg = document.createElement('p');
      pmsg.className = 'msg';
      pmsg.textContent = 'No windows yet.';
      box.appendChild(pmsg);
      return;
    }
    wins.forEach((w) => {
      const row = document.createElement('div');
      row.className = 'win';
      const days = (w.days && w.days.length)
        ? w.days.map((d) => DAY_NAMES[d] || '?').join(' ')
        : 'Every day';
      const b = document.createElement('b');
      b.textContent = (w.start || '?') + ' \u2013 ' + (w.end || '?');
      const sub = document.createElement('span');
      sub.className = 'sub';
      sub.textContent = ' ' + days + ' \u00b7 ' + (w.preset || 'default preset');
      row.append(b, sub);
      box.appendChild(row);
    });
  }

  function numberField(label, value, min, max, step, onInput) {
    const wrap = document.createElement('label');
    wrap.className = 'field';
    const span = document.createElement('span');
    span.textContent = label;
    const input = document.createElement('input');
    input.type = 'number';
    input.value = value;
    input.min = min;
    input.max = max;
    input.step = step;
    input.addEventListener('input', () => onInput(input.value));
    wrap.appendChild(span);
    wrap.appendChild(input);
    return wrap;
  }

  // One preset: a Play button and a disclosure holding its knobs. A <details>
  // rather than a modal so the whole set stays scannable and nothing traps you.
  function presetRow(p) {
    const det = document.createElement('details');
    det.className = 'preset';

    const sum = document.createElement('summary');
    const name = document.createElement('b');
    name.textContent = p.name;
    sum.appendChild(name);
    const sub = document.createElement('span');
    sub.className = 'sub';
    sub.textContent = p.type.replace('noise', '');
    sum.appendChild(sub);
    if (needsHeadphones(p)) {
      const hp = document.createElement('span');
      hp.className = 'hp';
      hp.textContent = 'headphones';
      sum.appendChild(hp);
    }
    // Play lives IN the summary so a collapsed card is still one tap at
    // bedtime — the card replaced the old quick tile, and the tile's whole
    // point was one press. preventDefault keeps the tap from also toggling
    // the details open.
    const play = document.createElement('button');
    play.type = 'button';
    play.className = 'btn go';
    play.appendChild(icon('play'));
    play.setAttribute('aria-label', 'Play ' + p.name);
    play.addEventListener('click', (e) => {
      e.preventDefault();
      e.stopPropagation();
      noiseStart(p.name);
    });
    sum.appendChild(play);
    det.appendChild(sum);

    const type = document.createElement('label');
    type.className = 'field';
    const tspan = document.createElement('span');
    tspan.textContent = 'Colour';
    const sel = document.createElement('select');
    NOISE_TYPES.forEach((t) => {
      const o = document.createElement('option');
      o.value = t;
      o.textContent = t.replace('noise', '');
      if (t === p.type) o.selected = true;
      sel.appendChild(o);
    });
    sel.addEventListener('change', () => { p.type = sel.value; sub.textContent = sel.value.replace('noise', ''); });
    type.appendChild(tspan);
    type.appendChild(sel);
    det.appendChild(type);

    // The 2nd swell's colour: 'same' keeps the slow swell on the one stream
    // (chained tremolo); a colour gives the incoming wave its own noise layer,
    // enveloped alone over the base bed. SoX-native colours only — the server
    // rejects the ffmpeg-generated ones for the layer, so they are not offered.
    const l2 = document.createElement('label');
    l2.className = 'field';
    const lspan = document.createElement('span');
    lspan.textContent = '2nd swell colour';
    const lsel = document.createElement('select');
    ['', 'brownnoise', 'pinknoise', 'whitenoise', 'tpdfnoise'].forEach((t) => {
      const o = document.createElement('option');
      o.value = t;
      o.textContent = t ? t.replace('noise', '') : 'same stream';
      if (t === (p.wave2Type || '')) o.selected = true;
      lsel.appendChild(o);
    });
    lsel.addEventListener('change', () => { p.wave2Type = lsel.value; });
    l2.appendChild(lspan);
    l2.appendChild(lsel);
    det.appendChild(l2);

    // Sine or isochronic. Shown always rather than appearing and vanishing as the
    // pulse rate crosses zero.
    const ps = document.createElement('label');
    ps.className = 'field';
    const pspan = document.createElement('span');
    pspan.textContent = 'Pulse shape';
    const psel = document.createElement('select');
    [['', 'smooth (sine)'], ['square', 'isochronic (hard gate)']].forEach(([v, t]) => {
      const o = document.createElement('option');
      o.value = v;
      o.textContent = t;
      if (v === (p.pulseShape || '')) o.selected = true;
      psel.appendChild(o);
    });
    psel.addEventListener('change', () => { p.pulseShape = psel.value; });
    ps.append(pspan, psel);
    det.appendChild(ps);

    // A program's phases are deliberately not editable here — a phase list is a
    // table, and a table on a phone is worse than a text editor. Saying so beats
    // leaving someone hunting for a control that isn't there.
    if ((p.phases || []).length) {
      const prog = document.createElement('p');
      prog.className = 'msg';
      const mins = p.phases.reduce((t, ph) => t + (ph.minutes || 0), 0);
      prog.textContent = 'Program: ' + p.phases.length + ' phases over ' + mins
        + ' min, then holds. Rates walk '
        + p.phases.map((ph) => (ph.beats && ph.beats[0]) || ph.pulseHz || '?').join(' \u2192 ')
        + ' Hz. Edit phases in hesplay.json.';
      det.appendChild(prog);
    }

    NOISE_FIELDS.forEach(([key, label, min, max, step]) => {
      det.appendChild(numberField(label, p[key], min, max, step, (v) => { p[key] = parseFloat(v); }));
    });

    // Which card this preset is filed under. Re-rendering on change moves the
    // card immediately, which is the only way the control reads as doing
    // something — the preset physically leaves the card you are looking at.
    const cat = document.createElement('label');
    cat.className = 'field';
    const cspan = document.createElement('span');
    cspan.textContent = 'Card';
    const csel = document.createElement('select');
    NOISE_CARDS.forEach(([key, label]) => {
      const o = document.createElement('option');
      o.value = key;
      o.textContent = label;
      if (key === presetCard(p)) o.selected = true;
      csel.appendChild(o);
    });
    csel.addEventListener('change', () => { p.category = csel.value; renderNoiseQuick(); });
    cat.append(cspan, csel);
    det.appendChild(cat);

    // The extra layers: waves of their own, each rendered by its own process and
    // summed with the body, which is why every one of them can have its own
    // colour (including the ffmpeg-generated ones, unlike the 2nd-swell layer),
    // its own band and its own swell. Adding or removing one rebuilds just this
    // box, so the rest of the card's edits stay put.
    const layersBox = document.createElement('div');
    det.appendChild(layersBox);
    const addBtn = document.createElement('button');
    addBtn.type = 'button';
    addBtn.className = 'btn';

    function renderLayers() {
      layersBox.textContent = '';
      const layers = p.layers || [];
      layers.forEach((l, i) => layersBox.appendChild(layerBox(l, i)));
      addBtn.disabled = layers.length >= MAX_LAYERS;
      addBtn.textContent = layers.length >= MAX_LAYERS
        ? 'Layer limit reached'
        : 'Add layer';
    }

    function layerBox(l, i) {
      const box = document.createElement('details');
      box.className = 'preset';
      const sum = document.createElement('summary');
      const b = document.createElement('b');
      b.textContent = 'Layer ' + (i + 1);
      const sub2 = document.createElement('span');
      sub2.className = 'sub';
      // The period is what the ear hears as size, so show it rather than the Hz.
      if ((l.beatHz || 0) > 0) {
        sub2.textContent = (l.monaural ? 'monaural' : 'binaural')
          + ' · ' + l.beatHz + ' Hz on ' + (l.carrierHz || 100);
      } else if ((l.eventsPerSec || 0) > 0) {
        sub2.textContent = 'events · ' + l.eventsPerSec + '/s · ' + (l.decayMs || 12) + 'ms';
      } else {
        // A pulse layer swings faster than once a second, where "Ns wave" rounds
        // to "0s wave" and says nothing. Read it out in Hz instead, and name the
        // slow envelope it rides so the two rates are never confused.
        let motion = ' · still';
        if (l.speed > 0) {
          motion = l.speed >= 1
            ? ' · ' + l.speed + ' Hz pulse'
            : ' · ' + Math.round(1 / l.speed) + 's wave';
          if (l.swell > 0) motion += ' on a ' + Math.round(1 / l.swell) + 's swell';
        }
        sub2.textContent = (l.type || '').replace('noise', '') + motion;
      }
      sum.append(b, sub2);
      box.appendChild(sum);

      const tl = document.createElement('label');
      tl.className = 'field';
      // A carrier pair has no colour — the stage synthesizes sines and ignores
      // Type — so the picker would be a control that does nothing.
      tl.hidden = (l.beatHz || 0) > 0 || (l.eventsPerSec || 0) > 0;
      const ts = document.createElement('span');
      ts.textContent = 'Colour';
      const tsel = document.createElement('select');
      NOISE_TYPES.forEach((t) => {
        const o = document.createElement('option');
        o.value = t;
        o.textContent = t.replace('noise', '');
        if (t === l.type) o.selected = true;
        tsel.appendChild(o);
      });
      tsel.addEventListener('change', () => { l.type = tsel.value; renderLayers(); });
      tl.append(ts, tsel);
      box.appendChild(tl);

      // Three kinds of layer, three field sets. Order matches the server's
      // precedence (noise.go: a carrier pair wins over event markers), so the
      // editor can never show fields the stage will ignore.
      const isTone = (l.beatHz || 0) > 0;
      const isEvent = !isTone && (l.eventsPerSec || 0) > 0;
      const fields = isTone ? TONE_FIELDS : (isEvent ? EVENT_FIELDS : LAYER_FIELDS);
      fields.forEach(([key, label, min, max, step]) => {
        box.appendChild(numberField(label, l[key] || 0, min, max, step, (v) => {
          l[key] = parseFloat(v) || 0;
        }));
      });
      if (isTone) {
        // Monaural is the difference between "needs headphones" and "works on the
        // speakers in the room", so it is a control, not a config-file detail.
        const ml = document.createElement('label');
        ml.className = 'field';
        const ms = document.createElement('span');
        ms.textContent = 'Both tones in both ears (monaural \u2014 no headphones)';
        const mi = document.createElement('input');
        mi.type = 'checkbox';
        mi.checked = !!l.monaural;
        mi.addEventListener('change', () => { l.monaural = mi.checked; renderNoiseQuick(); });
        ml.append(ms, mi);
        box.appendChild(ml);
      } else {
        // A tone layer never sees the reverb at all, so this belongs to the
        // other two kinds. It matters for a fast layer: a 6 Hz period is 167 ms,
        // well inside a reverb tail, and the room smears the swing away.
        const dl = document.createElement('label');
        dl.className = 'field';
        const ds = document.createElement('span');
        ds.textContent = 'Dry (skip the room — keeps a fast swing sharp)';
        const di = document.createElement('input');
        di.type = 'checkbox';
        di.checked = !!l.dry;
        di.addEventListener('change', () => { l.dry = di.checked; });
        dl.append(ds, di);
        box.appendChild(dl);
      }

      const rm = document.createElement('button');
      rm.type = 'button';
      rm.className = 'btn';
      rm.textContent = 'Remove layer';
      rm.addEventListener('click', () => {
        p.layers.splice(i, 1);
        renderLayers();
      });
      box.appendChild(rm);
      return box;
    }

    addBtn.addEventListener('click', () => {
      if (!p.layers) p.layers = [];
      if (p.layers.length >= MAX_LAYERS) return;
      // A new layer starts as a slow, quiet brown wave — the shape of the thing
      // people add a layer FOR — rather than an empty object the server drops.
      p.layers.push({ type: 'brownnoise', speed: 0.02, depth: 45, gain: -6, offset: 0 });
      renderLayers();
    });
    renderLayers();
    det.appendChild(addBtn);

    // Each card saves itself — on the front page there is no screen-level Save
    // button underneath it. The PUT writes the whole config (the API's shape),
    // so edits made in several cards before one Save all land together.
    const save = document.createElement('button');
    save.type = 'button';
    save.className = 'btn';
    save.textContent = 'Save';
    save.addEventListener('click', () => {
      putNoise()
        .then(() => toast('Saved'))
        .catch((e) => toast(e.message, true));
    });
    det.appendChild(save);
    return det;
  }

  function windowRow(w) {
    const box = document.createElement('div');
    box.className = 'win';

    const times = document.createElement('div');
    times.className = 'row';
    ['start', 'end'].forEach((k) => {
      const l = document.createElement('label');
      l.className = 'field';
      const s = document.createElement('span');
      s.textContent = k === 'start' ? 'From' : 'To';
      const i = document.createElement('input');
      i.type = 'time';
      i.value = w[k] || '';
      i.addEventListener('input', () => { w[k] = i.value; });
      l.appendChild(s);
      l.appendChild(i);
      times.appendChild(l);
    });
    box.appendChild(times);

    const pl = document.createElement('label');
    pl.className = 'field';
    const ps = document.createElement('span');
    ps.textContent = 'Preset';
    const sel = document.createElement('select');
    (noiseCfg.presets || []).forEach((p) => {
      const o = document.createElement('option');
      o.value = p.name;
      o.textContent = p.name;
      if (p.name === w.preset) o.selected = true;
      sel.appendChild(o);
    });
    sel.addEventListener('change', () => { w.preset = sel.value; });
    if (!w.preset && sel.options.length) w.preset = sel.value;
    pl.appendChild(ps);
    pl.appendChild(sel);
    box.appendChild(pl);

    // No days ticked means every day, which is both the common case and what
    // the server reads an empty list as — so the boxes start clear.
    const days = document.createElement('div');
    days.className = 'days';
    DAY_NAMES.forEach((nm, idx) => {
      const l = document.createElement('label');
      l.className = 'day';
      const cb = document.createElement('input');
      cb.type = 'checkbox';
      cb.checked = (w.days || []).indexOf(idx) >= 0;
      cb.addEventListener('change', () => {
        const set = new Set(w.days || []);
        if (cb.checked) set.add(idx); else set.delete(idx);
        w.days = Array.from(set).sort((a, b) => a - b);
      });
      l.appendChild(cb);
      l.append(nm);
      days.appendChild(l);
    });
    box.appendChild(days);

    const rm = document.createElement('button');
    rm.type = 'button';
    rm.className = 'btn';
    rm.textContent = 'Remove';
    rm.addEventListener('click', () => {
      noiseCfg.schedule = noiseCfg.schedule.filter((x) => x !== w);
      renderNoiseScreen();
    });
    box.appendChild(rm);
    return box;
  }

  // The schedule screen. Presets are NOT rendered here any more — their cards
  // live on the front page (renderNoiseQuick); the windows' preset selects
  // still read the same shared noiseCfg, so a rename shows up on both screens.
  function renderNoiseScreen() {
    const wins = $('noise-windows');
    wins.textContent = '';
    (noiseCfg.schedule || []).forEach((w) => wins.appendChild(windowRow(w)));
  }

  // putNoise writes the whole config back — the shared PUT behind both the
  // schedule screen's Save and every preset card's own Save.
  function putNoise() {
    return api('/api/noise', {
      method: 'PUT',
      body: JSON.stringify({
        audioDev: noiseCfg.audioDev || '',
        default: noiseCfg.default || '',
        presets: noiseCfg.presets,
        schedule: noiseCfg.schedule,
      }),
    });
  }

  // The home tiles need the preset names at boot, without opening the screen.
  // Failure is quiet: a box with no sox still plays music, and an error toast on
  // every load would be noise of the wrong kind.
  async function loadNoiseQuick() {
    try {
      noiseCfg = await api('/api/noise');
      noiseCfg.presets = noiseCfg.presets || [];
      noiseCfg.schedule = noiseCfg.schedule || [];
      $('noise-sec').hidden = noiseCfg.available === false;
      renderNoiseQuick();
      renderScheduleCard();
    } catch (_) { /* leave the section as it is */ }
  }

  // Read the config fresh every time the screen opens. It is also written by the
  // CLI, so a cached copy could silently overwrite an edit made over ssh.
  async function openNoise() {
    try {
      noiseCfg = await api('/api/noise');
      noiseCfg.presets = noiseCfg.presets || [];
      noiseCfg.schedule = noiseCfg.schedule || [];
      $('noise-unavailable').hidden = noiseCfg.available !== false;
      renderNoiseQuick();
      renderScheduleCard();
      renderNoiseScreen();
      push('noise', 'Noise');
    } catch (e) {
      toast(e.message, true);
    }
  }

  async function saveNoise() {
    $('noise-msg').textContent = '';
    try {
      await putNoise();
      $('noise-msg').textContent = 'Saved';
      renderNoiseQuick();
      renderScheduleCard();
    } catch (e) {
      if (isGateError(e)) return; // the sign-in card is up
      $('noise-msg').textContent = e.message;
    }
  }

  // --- browse ------------------------------------------------------------
  //
  // A three-deep stack (browse → artist → album) rather than a router: there is
  // no URL to keep in sync, and Back only ever means "the screen I came from".

  const SCREENS = ['home', 'browse', 'artist', 'album', 'noise'];
  let stack = ['home'];
  let current = { artistId: 0, artistName: '', albumId: 0, albumTitle: '' };

  function showScreen(name, title) {
    SCREENS.forEach((s) => {
      const el = s === 'home' ? document.querySelector('main:not([id])') : $('screen-' + s);
      if (el) el.hidden = s !== name;
    });
    $('bar-title').textContent = title || 'hesplay';
    $('back-btn').hidden = stack.length <= 1;
    refresh(); // the footer's visibility depends on which screen is showing
  }

  function push(name, title) { stack.push(name); showScreen(name, title); }

  // Starting something takes you to the queue. You picked it from a browse
  // screen, but what you want to see next is what is now playing — and the
  // playing card lives in the home list. Deliberately only on START: forcing
  // home on every poll would make it impossible to browse for the next thing
  // while something plays.
  function toQueueView() {
    stack = ['home'];
    showScreen('home', 'hesplay');
  }
  function pop() {
    if (stack.length > 1) stack.pop();
    const top = stack[stack.length - 1];
    const titles = { home: 'hesplay', browse: 'Artists', artist: current.artistName, album: current.albumTitle, noise: 'Noise' };
    showScreen(top, titles[top]);
  }

  // One row: a label that opens the next level, plus play and shuffle for it,
  // and optionally a mix. Mix is optional because this row is shared with the
  // album list, and a mix is seeded from an ARTIST — there is no album mix to
  // offer, so passing nothing leaves the row at two actions.
  function itemRow(label, sub, onOpen, onPlay, onShuffle, onMix) {
    const wrap = document.createElement('div');
    wrap.className = 'row-item';

    const open = document.createElement('button');
    open.type = 'button';
    open.className = 'open';
    const b = document.createElement('b');
    b.textContent = label;
    open.appendChild(b);
    if (sub) {
      const s = document.createElement('span');
      s.className = 'sub';
      s.textContent = sub;
      open.appendChild(s);
    }
    open.addEventListener('click', onOpen);

    const p = document.createElement('button');
    p.type = 'button'; p.className = 'btn go'; p.appendChild(icon('play'));
    p.setAttribute('aria-label', 'Play ' + label);
    p.addEventListener('click', onPlay);

    const s = document.createElement('button');
    s.type = 'button'; s.className = 'btn go'; s.appendChild(icon('shuffle'));
    s.setAttribute('aria-label', 'Shuffle ' + label);
    s.addEventListener('click', onShuffle);

    wrap.append(open, p, s);

    if (onMix) {
      const m = document.createElement('button');
      m.type = 'button'; m.className = 'btn go'; m.appendChild(icon('radio'));
      m.setAttribute('aria-label', 'Mix ' + label);
      m.title = 'This artist plus similar artists';
      m.addEventListener('click', onMix);
      wrap.appendChild(m);
      // Three actions need a fourth column; the CSS default is two.
      wrap.classList.add('row-item--3');
    }
    return wrap;
  }

  const ALPHABET = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ'.split('').concat('#');

  // The grid is drawn immediately from the static alphabet, then refined once
  // the index answers — so the home screen never shows an empty hole while the
  // player reads the catalog, and a letter that turns out to hold nobody simply
  // goes dim a moment later.
  function renderLetterGrid(letters) {
    const strip = $('letters');
    strip.textContent = '';
    (letters || ALPHABET.map((l) => ({ letter: l, count: -1 }))).forEach((l) => {
      const b = document.createElement('button');
      b.type = 'button';
      b.textContent = l.letter;
      b.disabled = l.count === 0;
      if (l.count >= 0) b.title = l.count === 1 ? '1 artist' : l.count + ' artists';
      b.addEventListener('click', () => openLetter(l.letter));
      strip.appendChild(b);
    });
  }

  async function loadLetters() {
    renderLetterGrid(null);
    try {
      const r = await api('/api/letters');
      renderLetterGrid(r.letters);
      $('artists-h').textContent = 'Artists · ' + r.total;
    } catch (_) {
      // Leave the grid live: the letter tap will surface the real error, and a
      // dead grid on the home screen says less than one that tries.
    }
  }

  async function openLetter(letter) {
    push('browse', letter === '#' ? 'Artists · #' : 'Artists · ' + letter);
    const box = $('artists');
    box.textContent = '';
    $('browse-msg').classList.remove('err');
    $('browse-msg').textContent = 'Loading…';
    try {
      const r = await api('/api/artists?letter=' + encodeURIComponent(letter));
      if (!r.artists.length) {
        $('browse-msg').textContent = 'No artists under ' + letter + '.';
        return;
      }
      $('browse-msg').textContent = r.artists.length === 1 ? '1 artist' : r.artists.length + ' artists';
      r.artists.forEach((a) => box.appendChild(itemRow(
        a.name, null,
        () => openArtist(a.id, a.name),
        () => play('artist', a.id, false),
        () => play('artist', a.id, true),
        // Not shuffled: a mix is the server's own weighted draw with the seed
        // track first, so shuffling it again would throw the seed away — the
        // same reasoning as the artist screen's Mix button.
        () => play('mix', a.id, false),
      )));
    } catch (e) {
      if (isGateError(e)) return; // the sign-in card is up; do not paint behind it
      $('browse-msg').classList.add('err');
      $('browse-msg').textContent = e.message;
    }
  }

  async function openArtist(id, name) {
    current.artistId = id; current.artistName = name;
    push('artist', name);
    const box = $('artist-albums');
    box.textContent = '';
    $('artist-albums-h').textContent = 'Albums';
    try {
      const r = await api('/api/artist?id=' + id);
      $('artist-albums-h').textContent = r.albums.length === 1 ? '1 album' : r.albums.length + ' albums';
      // "N by this artist", not "N tracks": the count is this artist's share of
      // the album, and on a compilation that is a fraction of it — The Rolling
      // Stones contribute 50 tracks to an "Essentials" album that holds 173.
      r.albums.forEach((al) => box.appendChild(itemRow(
        al.title, al.count + ' by this artist',
        () => openAlbum(al.id, al.title),
        () => play('album', al.id, false),
        () => play('album', al.id, true),
      )));
    } catch (e) {
      $('artist-albums-h').textContent = e.message;
    }
  }

  async function openAlbum(id, title) {
    current.albumId = id; current.albumTitle = title;
    push('album', title);
    const list = $('album-tracks');
    list.textContent = '';
    try {
      const r = await api('/api/album?id=' + id);
      r.tracks.forEach((t) => {
        const li = document.createElement('li');
        const btn = document.createElement('button');
        btn.type = 'button';
        btn.className = 'q-row';
        btn.setAttribute('aria-label', 'Play ' + t.title);
        const n = document.createElement('span');
        n.className = 'q-n';
        n.textContent = String(t.index);
        const tt = document.createElement('span');
        tt.className = 'q-t';
        tt.textContent = t.title;
        const ar = document.createElement('span');
        ar.className = 'q-a';
        ar.textContent = t.artist;
        tt.appendChild(ar);
        btn.append(n, tt);
        // Play the album from this song: the album queue, positioned. Keeps the
        // rest of the record queued behind it instead of playing one track and
        // falling silent.
        btn.addEventListener('click', () => play('album', id, false, t.index));
        li.appendChild(btn);
        list.appendChild(li);
      });
    } catch (e) {
      toast(e.message, true);
    }
  }

  // --- settings ----------------------------------------------------------

  async function loadServer() {
    try {
      const r = await api('/api/server');
      $('server').value = r.server || '';
    } catch (_) { /* leave the field empty; saving still works */ }
  }

  async function saveServer() {
    const msg = $('server-msg');
    const url = $('server').value.trim();
    msg.classList.remove('err');
    msg.textContent = 'Checking…';
    try {
      const r = await api('/api/server', { method: 'PUT', body: JSON.stringify({ server: url }) });
      $('server').value = r.server;
      msg.textContent = 'Connected to Hespera ' + (r.version || '');
      // A new server means a new library: rebuild both lists rather than
      // leaving the previous box's artists on screen.
      loadPlaylists();
      loadLetters();
    } catch (e) {
      msg.classList.add('err');
      msg.textContent = e.message;
    }
  }

  // --- boot --------------------------------------------------------------

  document.addEventListener('DOMContentLoaded', () => {
    $('settings-btn').addEventListener('click', () => {
      const p = $('settings');
      p.hidden = !p.hidden;
      $('settings-btn').setAttribute('aria-expanded', String(!p.hidden));
      if (!p.hidden) loadServer();
    });
    $('server-save').addEventListener('click', saveServer);

    document.querySelectorAll('#quick [data-source]').forEach((b) => {
      b.addEventListener('click', () => play(b.dataset.source, 0, b.dataset.shuffle === '1'));
    });

    $('back-btn').addEventListener('click', pop);
    document.querySelectorAll('[data-act]').forEach((b) => {
      b.addEventListener('click', () => {
        const a = b.dataset.act;
        if (a === 'artist-play') play('artist', current.artistId, false);
        else if (a === 'artist-shuffle') play('artist', current.artistId, true);
        // A mix is the server's own weighted draw, seed track first — so it
        // is NOT shuffled again here; that would throw the seed away.
        else if (a === 'artist-mix') play('mix', current.artistId, false);
        else if (a === 'album-play') play('album', current.albumId, false);
        else if (a === 'album-shuffle') play('album', current.albumId, true);
      });
    });

    $('pause').addEventListener('click', () => setPaused(!isPaused));
    $('prev').addEventListener('click', () => action('/api/prev'));
    $('next').addEventListener('click', () => action('/api/next'));
    $('stop').addEventListener('click', () => action('/api/stop'));

    $('noise-more').addEventListener('click', openNoise);
    $('noise-save').addEventListener('click', saveNoise);
    $('noise-add-window').addEventListener('click', () => {
      noiseCfg.schedule.push({ start: '20:00', end: '10:00', days: [], preset: noiseCfg.default || '' });
      renderNoiseScreen();
    });

    $('signin-go').addEventListener('click', submitSignIn);
    $('passphrase').addEventListener('keydown', (e) => {
      // isComposing: an IME's Enter commits a candidate and must not submit.
      if (e.key === 'Enter' && !e.isComposing) {
        e.preventDefault();
        submitSignIn();
      }
    });

    loadPlaylists();
    loadNoiseQuick();
    loadLetters();
    refresh();
    startPoll();
    // Coming back from a locked screen should show the truth immediately rather
    // than up to POLL_MS of stale now-playing.
    document.addEventListener('visibilitychange', () => { if (!document.hidden) refresh(); });

    if ('serviceWorker' in navigator) {
      navigator.serviceWorker.register('sw.js').catch(() => { /* http:// on some
        browsers refuses a worker; the app still works, it just won't install */ });
    }
  });
})();
