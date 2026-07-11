// about_health.js — fills the Settings → About "System" health rows.
//
// Three rows: Hespera (reuses /update/check, the same data the home-screen version
// pill shows), ffmpeg and Browser (from /about/health — local version probes).
// Fetched once, lazily, only when the About card is actually opened, so the
// subprocess probes never run on an ordinary settings load. Renders with
// textContent only (no injected HTML). Runs on turbo:load like the other
// controllers so it survives Turbo body swaps.
(() => {
  'use strict';

  // status → badge class (reusing the existing badge palette) + label.
  const BADGE = {
    ok:      { cls: 'badge-done',   text: 'OK' },
    warn:    { cls: 'badge-warn',   text: 'Update available' },
    missing: { cls: 'badge-failed', text: 'Not found' },
    na:      { cls: 'badge-queued', text: 'On your device' },
  };

  function setRow(root, key, status, version, detail, name) {
    const row = root.querySelector('[data-health="' + key + '"]');
    if (!row) return;
    const badge = row.querySelector('.about-health-badge');
    const ver = row.querySelector('.about-health-version');
    const det = row.querySelector('.about-health-detail');
    const b = BADGE[status] || BADGE.na;
    badge.className = 'badge about-health-badge ' + b.cls;
    badge.textContent = b.text;
    ver.textContent = [name, version].filter(Boolean).join(' ');
    if (detail) det.textContent = detail;
  }

  async function loadHespera(root) {
    const current = root.dataset.current || '';
    try {
      const res = await fetch('/update/check');
      if (!res.ok) throw new Error('http ' + res.status);
      const d = await res.json();
      if (d.updateAvailable) {
        setRow(root, 'hespera', 'warn', 'v' + d.current,
          'A newer version (v' + d.latest + ') is available — the version pill on the home screen downloads it.');
      } else if (d.latest) {
        setRow(root, 'hespera', 'ok', 'v' + d.current, 'Up to date — the latest published release.');
      } else {
        // No releases published / check disabled — show the installed version
        // without a pass/fail claim we can't back up.
        setRow(root, 'hespera', 'na', 'v' + current, 'Installed version. No published release to compare against yet.');
      }
    } catch (_) {
      setRow(root, 'hespera', 'na', 'v' + current, 'Installed version (could not reach the update server).');
    }
  }

  async function loadTools(root) {
    try {
      const res = await fetch('/about/health');
      if (!res.ok) throw new Error('http ' + res.status);
      const d = await res.json();
      setRow(root, 'ffmpeg', d.ffmpeg.status, d.ffmpeg.version, d.ffmpeg.detail);
      setRow(root, 'chrome', d.chrome.status, d.chrome.version, d.chrome.detail, d.chrome.name);
    } catch (_) {
      setRow(root, 'ffmpeg', 'na', '', 'Could not check the ffmpeg version.');
      setRow(root, 'chrome', 'na', '', 'Could not check the browser version.');
    }
  }

  function load(root) {
    if (root.dataset.loaded) return; // fetch once per rendered card
    root.dataset.loaded = '1';
    loadHespera(root);
    loadTools(root);
  }

  function init() {
    const root = document.getElementById('about-health');
    if (!root) return;
    const card = root.closest('details');
    if (card && card.open) {
      load(root); // deep-linked open (?open=about) — load now
    } else if (card) {
      card.addEventListener('toggle', () => { if (card.open) load(root); }, { once: false });
    }
  }

  document.addEventListener('turbo:load', init);
})();
