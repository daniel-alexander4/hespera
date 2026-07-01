// playlists.js — Top 100 card controls on /music/playlists.
//
// The "My Music" buttons are plain [data-play] links handled by player.js. The
// Top-100 buttons build a source=top100 player-queue URL from the selected year,
// the reverse toggle, and the shuffle mode, then route it by playback mode:
//   default       → a YouTube popout window that iframes the video + auto-advances
//   "Test Audio"  → the in-app hidden-audio engine (navigate to the now-playing page)
// Loaded once from the layout shell; re-binds on each Turbo navigation.
(function () {
  function buildParams(mode, yearSel, revChk) {
    var p = new URLSearchParams();
    p.set('source', 'top100');
    if (mode === 'all-shuffle') {
      p.set('shuffle', '1');
      return p;
    }
    var y = yearSel ? yearSel.value : '';
    if (y) p.set('y', y);
    if (mode === 'year-shuffle') {
      p.set('shuffle', '1');
    } else if (revChk && revChk.checked) {
      p.set('dir', 'rev'); // "Play Year" reversed: #100 → #1
    }
    return p;
  }

  function init() {
    var card = document.getElementById('top100-card');
    if (!card) return;
    var yearSel = document.getElementById('top100-year');
    var revChk = document.getElementById('top100-rev');
    var testChk = document.getElementById('top100-test');

    card.querySelectorAll('[data-top100]').forEach(function (btn) {
      if (btn.dataset.plBound) return;
      btn.dataset.plBound = '1';
      btn.addEventListener('click', function () {
        var qs = buildParams(btn.getAttribute('data-top100'), yearSel, revChk).toString();
        if (testChk && testChk.checked) {
          // In-app: the now-playing page autoloads the queue from these params.
          window.location.href = '/music/player?' + qs;
        } else {
          // Compliant default: a visible YouTube embed in its own window.
          window.open('/static/popout.html?' + qs, 'hespera-top100', 'width=940,height=660');
        }
      });
    });
  }

  document.addEventListener('turbo:load', init);
  document.addEventListener('DOMContentLoaded', init);
})();
