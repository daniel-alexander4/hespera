package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Key tracer — a remote-input diagnostic. When the keytrace_enabled app-setting
// is on, keytrace.js beacons every keydown (key/code, target, whether a handler
// consumed it, where focus moved) plus each Turbo navigation here, and the
// events are appended as JSON lines to DataDir/keytrace.jsonl. That answers
// "what does this TV remote actually emit, and what did Hespera do with it"
// without devtools — the app window on a TV has no address bar or console, and
// a .desktop launch swallows stdout. Off by default; the toggle lives on
// Settings → API Keys (remote-reachable) and in hescli (`config set
// keytrace_enabled on` over SSH).

// effectiveKeytraceEnabled reports whether key-event tracing is on. Default OFF
// (opt-in) — stored as '1' when enabled, absent = off; the lyrics_enabled shape.
// Read per call so the toggle takes effect without a restart.
func (h *Handler) effectiveKeytraceEnabled(ctx context.Context) bool {
	var v string
	_ = h.db.QueryRowContext(ctx, "SELECT value FROM app_settings WHERE key='keytrace_enabled'").Scan(&v)
	return strings.TrimSpace(v) == "1"
}

// keytraceMaxEventBytes caps a single traced event's payload — real events are
// ~200 bytes; anything bigger is malformed or abuse.
const keytraceMaxEventBytes = 8 << 10

// keytracePath is the trace file the POST handler appends to.
func (h *Handler) keytracePath() string {
	return filepath.Join(h.cfg.DataDir, "keytrace.jsonl")
}

// keytrace serves the tracer's two verbs: GET tells the client whether to arm
// (one probe per full page load), POST appends one traced event. POSTs are
// refused while the setting is off, so a stale armed client can't keep writing
// after the toggle is turned off.
func (h *Handler) keytrace(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "enabled": h.effectiveKeytraceEnabled(r.Context())})
	case http.MethodPost:
		if !h.effectiveKeytraceEnabled(r.Context()) {
			jsonError(w, "key tracing is disabled", http.StatusForbidden)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, keytraceMaxEventBytes))
		if err != nil {
			jsonError(w, "event too large", http.StatusBadRequest)
			return
		}
		if !json.Valid(body) {
			jsonError(w, "event must be JSON", http.StatusBadRequest)
			return
		}
		line := fmt.Sprintf(`{"ts":%q,"event":%s}`+"\n", time.Now().Format(time.RFC3339Nano), body)
		f, err := os.OpenFile(h.keytracePath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			jsonErr(w, 500, "internal server error", "open keytrace file failed", "handler", "keytrace", "err", err)
			return
		}
		_, werr := f.WriteString(line)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			jsonErr(w, 500, "internal server error", "write keytrace file failed", "handler", "keytrace", "err", werr)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}
