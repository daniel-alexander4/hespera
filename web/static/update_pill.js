// update_pill.js — the topbar version pill (#update-pill).
//
// Three states: yellow `is-unknown` (the server-rendered default — automatic
// checks off, no releases published, or a failed check), green `is-current`
// (checked, running the latest release), red `is-outdated` (a newer release
// exists — clicking downloads its asset for this machine).
//
// The automatic check runs at most once per browser session (sessionStorage)
// and only when the update_check_enabled setting is on — the server enforces
// that via `?auto=1`, answering enabled:false without any network call when
// off. Clicking the pill always re-checks, toggle or no toggle, and when an
// update exists navigates to the asset URL so the browser downloads it (nothing
// installs automatically). State persists in sessionStorage and is re-applied
// on every turbo:load, since the topbar re-renders with each body swap.
(() => {
  if (window.__hesperaUpdatePill) return;
  window.__hesperaUpdatePill = true;

  const STATE_KEY = 'hespera_update_state';
  const CHECKED_KEY = 'hespera_update_checked';

  const loadState = () => {
    try { return JSON.parse(sessionStorage.getItem(STATE_KEY)); } catch { return null; }
  };
  const saveState = (st) => {
    try { sessionStorage.setItem(STATE_KEY, JSON.stringify(st)); } catch { /* private mode */ }
  };

  const pill = () => document.getElementById('update-pill');

  const apply = (st) => {
    const el = pill();
    if (!el || !st) return;
    el.classList.remove('is-unknown', 'is-current', 'is-outdated');
    el.classList.add(st.cls);
    el.textContent = st.text;
    el.title = st.title;
  };

  // stateFrom maps a /update/check response to a pill state; null when no check
  // ran (the auto path with the toggle off) — the yellow default stands.
  const stateFrom = (d) => {
    if (!d || d.enabled === false) return null;
    const v = 'v' + (d.current || 'dev');
    if (d.updateAvailable) {
      return {
        cls: 'is-outdated', text: v,
        title: 'Update available: v' + d.latest + ' — click to download',
        downloadUrl: d.downloadUrl || '', url: d.url || '',
      };
    }
    if (d.latest) return { cls: 'is-current', text: v, title: 'You’re on the latest version (' + v + ')' };
    return { cls: 'is-unknown', text: v, title: 'No releases published yet (' + v + ')' };
  };

  const check = async (auto) => {
    const res = await fetch('/update/check' + (auto ? '?auto=1' : ''));
    if (!res.ok) throw new Error('update check failed');
    return res.json();
  };

  // Navigation runs through this indirection because jsdom (test/js) cannot
  // intercept a real location assignment — the harness overrides __updatePillGo.
  const go = (url) => (window.__updatePillGo || ((u) => { window.location.href = u; }))(url);

  document.addEventListener('turbo:load', () => {
    const cached = loadState();
    if (cached) { apply(cached); return; }
    if (sessionStorage.getItem(CHECKED_KEY)) return; // checked already: disabled or failed — stay yellow
    sessionStorage.setItem(CHECKED_KEY, '1');
    check(true).then((d) => {
      const st = stateFrom(d);
      if (st) { saveState(st); apply(st); }
    }).catch(() => { /* unreachable update server: the yellow default stands */ });
  });

  // Delegated so it survives Turbo body swaps (bound once, window-guarded).
  document.addEventListener('click', (e) => {
    const el = e.target && e.target.closest ? e.target.closest('#update-pill') : null;
    if (!el) return;
    el.title = 'Checking…';
    check(false).then((d) => {
      const st = stateFrom(d);
      saveState(st);
      apply(st);
      // Download the matching asset; the https guard keeps a malformed URL from
      // navigating anywhere surprising. Fall back to the release page when no
      // asset matched this OS/arch.
      const dest = st.downloadUrl || st.url || '';
      if (d.updateAvailable && dest.startsWith('https://')) go(dest);
    }).catch(() => {
      const el2 = pill();
      if (el2) el2.title = 'Could not reach the update server';
    });
  });
})();
