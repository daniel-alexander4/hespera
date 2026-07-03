// Package watch auto-triggers library scans when files change under library
// roots, so new media appears without clicking Scan. fsnotify (inotify) with
// the recursive-watch bookkeeping it doesn't do itself: every directory under
// each root is watched, and directories created later are added on their
// Create event. Events are debounced per library (a media copy emits Write
// events until it finishes; the scan fires only after a quiet period), and a
// fire is skipped while a scan-type job for that library is already queued or
// running — the running scan re-walks the whole root, so it picks the change
// up anyway. Best-effort throughout: watcher failures degrade to the manual
// Scan button, never break serving.
package watch

import (
	"database/sql"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Enqueue starts the full scan chain for a library (the caller wires it to
// web.Handler.EnqueueLibraryScan).
type Enqueue func(libraryID int64)

type Service struct {
	db       *sql.DB
	enqueue  Enqueue
	debounce time.Duration
	refresh  time.Duration

	fw   *fsnotify.Watcher
	done chan struct{}
	wg   sync.WaitGroup

	mu     sync.Mutex
	roots  map[int64]string      // library id → clean root
	timers map[int64]*time.Timer // per-library debounce
}

// New starts the watcher: an immediate root sync, then an event loop and a
// periodic re-sync (picks up libraries added/removed at runtime).
func New(db *sql.DB, enqueue Enqueue, debounce, refresh time.Duration) (*Service, error) {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	s := &Service{
		db: db, enqueue: enqueue, debounce: debounce, refresh: refresh,
		fw: fw, done: make(chan struct{}),
		roots: map[int64]string{}, timers: map[int64]*time.Timer{},
	}
	s.syncRoots(false)
	s.wg.Add(2)
	go s.eventLoop()
	go s.refreshLoop()
	return s, nil
}

// Close stops the loops, the debounce timers, and the underlying watcher.
func (s *Service) Close() error {
	close(s.done)
	err := s.fw.Close() // unblocks the event loop
	s.wg.Wait()
	s.mu.Lock()
	for id, t := range s.timers {
		t.Stop()
		delete(s.timers, id)
	}
	s.mu.Unlock()
	return err
}

func (s *Service) refreshLoop() {
	defer s.wg.Done()
	tick := time.NewTicker(s.refresh)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			s.syncRoots(true)
		case <-s.done:
			return
		}
	}
}

// syncRoots reconciles the watch set against the libraries table. With
// bumpAdded (every re-sync after the initial one), a newly-appeared root also
// gets a debounced fire — its pre-existing files will never emit events, so
// "add a library" auto-scans its current content too. The initial boot sync
// never bumps: relaunching the app must not re-chain every library.
func (s *Service) syncRoots(bumpAdded bool) {
	rows, err := s.db.Query("SELECT id, root_path FROM libraries WHERE type IN ('music','tv','movies')")
	if err != nil {
		slog.Warn("watch: list libraries", "err", err)
		return
	}
	current := map[int64]string{}
	for rows.Next() {
		var id int64
		var root string
		if rows.Scan(&id, &root) == nil {
			current[id] = filepath.Clean(root)
		}
	}
	rows.Close()

	s.mu.Lock()
	added, removed := map[int64]string{}, map[int64]string{}
	for id, root := range current {
		if s.roots[id] != root {
			if old, ok := s.roots[id]; ok {
				removed[id] = old
			}
			added[id] = root
		}
	}
	for id, root := range s.roots {
		if _, ok := current[id]; !ok {
			removed[id] = root
		}
	}
	s.roots = current
	s.mu.Unlock()

	for _, root := range removed {
		s.removeTree(root)
	}
	for id, root := range added {
		s.addTree(root)
		if bumpAdded {
			s.bump(id)
		}
	}
}

// addTree watches root and every non-hidden directory under it.
func (s *Service) addTree(root string) {
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil //nolint:nilerr // unreadable subtree: watch what we can
		}
		if p != root && strings.HasPrefix(d.Name(), ".") {
			return fs.SkipDir
		}
		if werr := s.fw.Add(p); werr != nil {
			slog.Warn("watch: add dir", "dir", p, "err", werr)
		}
		return nil
	})
}

// removeTree drops watches under a root that left the library set. fsnotify
// removes watches for deleted dirs itself; this covers a re-pointed library.
func (s *Service) removeTree(root string) {
	for _, w := range s.fw.WatchList() {
		if w == root || strings.HasPrefix(w, root+string(os.PathSeparator)) {
			_ = s.fw.Remove(w)
		}
	}
}

func (s *Service) eventLoop() {
	defer s.wg.Done()
	for {
		select {
		case ev, ok := <-s.fw.Events:
			if !ok {
				return
			}
			s.handleEvent(ev)
		case err, ok := <-s.fw.Errors:
			if !ok {
				return
			}
			slog.Warn("watch: fsnotify", "err", err)
		case <-s.done:
			return
		}
	}
}

func (s *Service) handleEvent(ev fsnotify.Event) {
	path := filepath.Clean(ev.Name)
	libID, root := s.libraryFor(path)
	// The hidden-path filter applies to segments BELOW the library root only —
	// the root itself may legitimately live under a dotted parent directory.
	if libID == 0 || hiddenSegment(strings.TrimPrefix(path, root+string(os.PathSeparator))) {
		return
	}
	// A new directory must join the watch set (fsnotify is not recursive) —
	// and its contents may already exist (mv/extract), so walk it.
	if ev.Op&fsnotify.Create != 0 {
		if fi, err := os.Stat(path); err == nil && fi.IsDir() {
			s.addTree(path)
		}
	}
	s.bump(libID)
}

// libraryFor maps an event path to the library whose root contains it.
func (s *Service) libraryFor(path string) (int64, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, root := range s.roots {
		if path == root || strings.HasPrefix(path, root+string(os.PathSeparator)) {
			return id, root
		}
	}
	return 0, ""
}

// hiddenSegment reports whether any element of a root-relative path is
// dot-prefixed (editor/sync scratch dirs and files — .stfolder, .tmp swap
// files — never trigger scans).
func hiddenSegment(path string) bool {
	for _, seg := range strings.Split(path, string(os.PathSeparator)) {
		if strings.HasPrefix(seg, ".") {
			return true
		}
	}
	return false
}

// bump (re)arms a library's debounce timer: the scan fires only after a quiet
// period, so a large copy-in-progress keeps pushing it back instead of
// triggering a mid-write scan storm.
func (s *Service) bump(libID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.timers[libID]; ok {
		t.Reset(s.debounce)
		return
	}
	s.timers[libID] = time.AfterFunc(s.debounce, func() { s.fire(libID) })
}

func (s *Service) fire(libID int64) {
	s.mu.Lock()
	delete(s.timers, libID)
	s.mu.Unlock()

	select {
	case <-s.done:
		return
	default:
	}
	if !s.enabled() {
		return
	}
	if s.scanActive(libID) {
		// The queued/running scan re-walks the whole root and picks these
		// changes up; further events after it will bump a fresh timer.
		return
	}
	s.enqueue(libID)
}

// enabled reads the watch_enabled kill-switch per fire (default on, '0' =
// off — the integrity_autorepair pattern), so the Settings toggle applies
// without a restart.
func (s *Service) enabled() bool {
	var v string
	_ = s.db.QueryRow("SELECT value FROM app_settings WHERE key='watch_enabled'").Scan(&v)
	return strings.TrimSpace(v) != "0"
}

// scanActive reports whether a scan-type job for the library is already
// queued or running (the jobs service has no dedup of its own).
func (s *Service) scanActive(libID int64) bool {
	var n int
	_ = s.db.QueryRow(`
SELECT COUNT(*) FROM scan_jobs
WHERE library_id = ? AND status IN ('queued','running')
  AND job_type IN ('scan','tvscan','moviescan')`, libID).Scan(&n)
	return n > 0
}
