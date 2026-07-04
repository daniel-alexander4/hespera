// job_status.js — shared live per-library job status, used by the libraries page
// and the TV series page. Clicking a [data-live-job] form enqueues via fetch (no
// navigation), and a poll of /libraries/jobs-status updates every [data-lib-status]
// badge on the page in place. The /settings/jobs page stays the full audit record.
//
// Loaded once from the layout shell (defer) and run on turbo:load (mirroring
// couch.js/media_player.js), so it survives Turbo body swaps without re-binding
// stale listeners; it no-ops on pages with no [data-lib-status] badge.

function initJobStatus() {
  const rowEls = document.querySelectorAll('[data-lib-status]');
  if (!rowEls.length) return;

  const VERB = {
    scan: 'Scanning', tvscan: 'Scanning', moviescan: 'Scanning', photoscan: 'Scanning',
    music_match: 'Matching', tv_match: 'Matching', movie_match: 'Matching',
    tag_writeback: 'Writing tags',
    tv_probe: 'Verifying', movie_probe: 'Verifying', photo_probe: 'Verifying',
    photo_thumb: 'Generating thumbnails', tv_thumb: 'Generating thumbnails',
    tv_trickplay: 'Generating trickplay', movie_trickplay: 'Generating trickplay',
    integrity_check: 'Checking integrity', integrity_deep: 'Checking integrity',
  };
  const ACTIVE = new Set(['queued', 'running']);
  const seenActive = new Set();   // libraries observed active during this page's life
  const clearTimers = {};
  let timer = null, graceLeft = 0;

  const statusEl = (id) => document.querySelector('[data-lib-status="' + id + '"]');

  function setBadge(el, status, text) {
    el.innerHTML = '';
    const b = document.createElement('span');
    b.className = 'badge badge-' + status;
    b.textContent = text;            // textContent — never inject job.error as HTML
    el.appendChild(b);
    el.hidden = false;
  }

  function render(id, job) {
    const el = statusEl(id);
    if (!el) return;
    if (job && ACTIVE.has(job.status)) {
      seenActive.add(id);
      const verb = VERB[job.type] || 'Working';
      const prog = job.status === 'running' && job.total > 0 ? ' ' + job.current + '/' + job.total : '';
      setBadge(el, job.status, (job.status === 'queued' ? 'Queued' : verb) + prog);
      return;
    }
    // Terminal (or gone): only surface for a library we watched go active, so a
    // stale finished job never flashes on load.
    if (!seenActive.has(id)) { el.hidden = true; el.textContent = ''; return; }
    seenActive.delete(id);
    if (job && job.status === 'failed') setBadge(el, 'failed', 'Failed' + (job.error ? ': ' + job.error : ''));
    else if (job && job.status === 'canceled') setBadge(el, 'canceled', 'Canceled');
    else setBadge(el, 'done', '✓ Done');
    clearTimeout(clearTimers[id]);
    clearTimers[id] = setTimeout(() => { const e = statusEl(id); if (e) { e.hidden = true; e.textContent = ''; } }, 6000);
  }

  const schedule = (ms) => { clearTimeout(timer); timer = setTimeout(poll, ms); };

  async function poll() {
    let data = null;
    if (!document.hidden) {
      try {
        const res = await fetch('/libraries/jobs-status', { headers: { Accept: 'application/json' } });
        if (res.ok) data = await res.json();
      } catch (_) {}
    }
    let active = false;
    rowEls.forEach((el) => {
      const id = el.getAttribute('data-lib-status');
      const job = data && data.jobs ? data.jobs[id] : null;
      render(id, job);
      if (job && ACTIVE.has(job.status)) active = true;
    });
    if (active) { graceLeft = 2; schedule(2000); }
    else if (graceLeft > 0) { graceLeft--; schedule(2000); } // grace bridges the scan→match handoff
    // else: idle — stop until the next action triggers a poll.
  }

  // The badge a form's action drives: data-status-target (the libraries rows and
  // the series page set it explicitly), falling back to the form's own id input.
  function targetFor(form) {
    if (form.dataset.statusTarget) return form.dataset.statusTarget;
    const inp = form.querySelector('input[name="id"]');
    return inp ? inp.value : null;
  }

  // Intercept the job-enqueueing forms: fetch POST (no nav), optimistic Queued, poll.
  document.querySelectorAll('form[data-live-job]').forEach((form) => {
    form.addEventListener('submit', (e) => {
      e.preventDefault();                       // beats Turbo's bubble-phase submit
      const id = targetFor(form);
      const el = id != null ? statusEl(id) : null;
      if (el) { seenActive.add(id); setBadge(el, 'queued', 'Queued'); }
      fetch(form.action, { method: 'POST', headers: { Accept: 'application/json' }, body: new URLSearchParams(new FormData(form)) })
        .catch(() => {})
        .finally(() => { graceLeft = 2; schedule(600); });
    });
  });

  document.addEventListener('turbo:before-cache', () => {
    clearTimeout(timer);
    Object.values(clearTimers).forEach(clearTimeout);
  }, { once: true });

  poll(); // pick up any in-flight job on load
}

document.addEventListener('turbo:load', initJobStatus);
