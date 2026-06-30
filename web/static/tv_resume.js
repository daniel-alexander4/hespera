// Topbar "resume watching" chip for video (TV and movies). The video lives on
// the player page and is torn down on every Turbo navigation (it pauses +
// stops), so when you click away this surfaces a link back to what you were
// watching. Playback position is stored server-side ({tv,movie}_playback_progress)
// and the player resumes from it, so the link just needs the kind + file id.
//
// State is kept in sessionStorage so the chip survives Turbo swaps and a hard
// reload within the same tab, and is empty at the start of a session — it only
// appears once you've opened a player this session.
(() => {
  const KEY = 'hespera_resume_tv';

  const read = () => {
    try { return JSON.parse(sessionStorage.getItem(KEY) || 'null'); } catch (e) { return null; }
  };
  const write = (v) => { try { sessionStorage.setItem(KEY, JSON.stringify(v)); } catch (e) {} };
  const clear = () => { try { sessionStorage.removeItem(KEY); } catch (e) {} };

  const cluster = () => document.getElementById('nw-cluster');

  const hide = () => { const c = cluster(); if (c) c.classList.add('hidden'); };
  const show = (target) => {
    const c = cluster();
    const link = document.getElementById('nw-link');
    const title = document.getElementById('nw-title');
    if (!c || !link || !title) return;
    // Default to 'tv' for entries written before the chip became multi-kind.
    link.href = '/' + (target.kind || 'tv') + '/player?file=' + target.fileID;
    title.textContent = target.label;
    c.classList.remove('hidden');
  };

  // turbo:load fires on the initial load and after every Turbo visit.
  const sync = () => {
    const video = document.getElementById('tvVideo');
    if (video) {
      // On a player page. Both the TV and movie players use #tvVideo +
      // .tv-player-header; record which kind so the chip links back to the right
      // /<kind>/player (the file-id namespaces differ between the two tables, so
      // the kind is what disambiguates them). Hide the chip while you're here.
      const kind = video.dataset.mediaKind;
      if (kind === 'tv' || kind === 'movie') {
        const fileID = parseInt(video.dataset.fileId, 10);
        if (fileID > 0) {
          const showEl = document.querySelector('.tv-player-header h1');
          const epEl = document.querySelector('.tv-player-header h2');
          const label = [
            showEl && showEl.textContent.trim(),
            epEl && epEl.textContent.trim(),
          ].filter(Boolean).join(' · ') || 'Resume watching';
          write({ kind, fileID, label });
        }
      }
      hide();
      return;
    }
    // Anywhere else: offer the resume link if there's something to resume.
    const target = read();
    if (target && target.fileID) show(target); else hide();
  };

  document.addEventListener('turbo:load', sync);

  // The "×" dismisses the chip and forgets the target.
  document.addEventListener('click', (e) => {
    const btn = e.target.closest && e.target.closest('#nw-dismiss');
    if (!btn) return;
    e.preventDefault();
    clear();
    hide();
  });
})();
