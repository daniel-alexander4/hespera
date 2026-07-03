// search.js — the global jump-to palette. "/" anywhere (outside a text field)
// opens an overlay with grouped instant results across the whole library;
// Enter opens the first result, arrows walk the rows (couch.js's always-on
// spatial nav — while the palette is visible its data-couch-overlay scopes
// focus to it), Escape closes. Rows arrive fully shaped from GET /search
// (href/text/context) and are rendered via textContent — the client never
// interprets result strings as HTML.
//
// The palette markup lives in the layout shell, so it re-renders (closed) on
// every Turbo swap; per-render listeners bind in turbo:load like subtabs.js.
// The "/" opener is a document-level listener bound ONCE (window-guarded,
// couch.js pattern) that queries the live DOM per press.
(() => {
  const modal = () => document.getElementById('search-modal');
  const input = () => document.getElementById('search-input');

  const open = () => {
    const m = modal();
    if (!m) return;
    m.classList.remove('hidden');
    const i = input();
    if (i) { i.value = ''; i.focus(); }
    renderSections([]);
  };
  const close = () => {
    const m = modal();
    if (m) m.classList.add('hidden');
  };

  function renderSections(sections) {
    const box = document.getElementById('search-results');
    if (!box) return;
    box.textContent = '';
    for (const sec of sections || []) {
      const label = document.createElement('div');
      label.className = 'search-section-label';
      label.textContent = sec.label;
      box.appendChild(label);
      for (const row of sec.rows || []) {
        const a = document.createElement('a');
        a.className = 'search-row';
        a.href = row.href;
        const text = document.createElement('span');
        text.className = 'search-row-text';
        text.textContent = row.text;
        a.appendChild(text);
        if (row.context) {
          const ctx = document.createElement('span');
          ctx.className = 'search-row-context';
          ctx.textContent = row.context;
          a.appendChild(ctx);
        }
        box.appendChild(a);
      }
    }
  }

  // Debounced fetch with a stale-response guard: only the newest query's
  // response may render (a slow early response must not clobber a later one).
  let seq = 0, debounceTimer = null;
  function queryNow(q) {
    const mine = ++seq;
    fetch('/search?q=' + encodeURIComponent(q))
      .then((res) => res.json())
      .then((data) => { if (mine === seq) renderSections(data.sections); })
      .catch(() => {});
  }

  const bind = () => {
    const i = input();
    if (!i) return;
    document.querySelectorAll('[data-search-open]').forEach((b) => b.addEventListener('click', open));

    i.addEventListener('input', () => {
      clearTimeout(debounceTimer);
      const q = i.value.trim();
      if (q.length < 2) { renderSections([]); return; }
      debounceTimer = setTimeout(() => queryNow(q), 150);
    });

    i.addEventListener('keydown', (e) => {
      // couch.js's Back is typing-guarded inside inputs, so the palette must
      // dismiss itself where focus always is; likewise arrows in an input stay
      // native, so row entry needs its own handoff.
      if (e.key === 'Escape') {
        e.preventDefault();
        e.stopPropagation();
        close();
      } else if (e.key === 'Enter') {
        const first = document.querySelector('#search-results a');
        if (first) { e.preventDefault(); first.click(); close(); }
      } else if (e.key === 'ArrowDown') {
        const first = document.querySelector('#search-results a');
        if (first) { e.preventDefault(); e.stopPropagation(); first.focus(); }
      }
    });

    // A row click is a normal Turbo navigation — just close behind it.
    const results = document.getElementById('search-results');
    if (results) results.addEventListener('click', () => close());
    const closeBtn = document.getElementById('search-close');
    if (closeBtn) closeBtn.addEventListener('click', close);
  };
  document.addEventListener('turbo:load', bind);

  if (!window.__searchKeyBound) {
    window.__searchKeyBound = true;
    document.addEventListener('keydown', (e) => {
      if (e.key !== '/') return;
      const t = e.target;
      if (t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.tagName === 'SELECT' || t.isContentEditable)) return;
      if (!modal()) return;
      e.preventDefault();
      open();
    });
  }
})();
