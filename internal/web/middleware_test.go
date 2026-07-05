package web

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestWithLoggingLevel pins that the per-request access log is emitted at Debug,
// so at the default info level it does no synchronous stdout write on the hot
// path (a stalling log sink can't add request latency), and reappears when the
// level is lowered to debug.
func TestWithLoggingLevel(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	run := func(level slog.Level) string {
		var buf bytes.Buffer
		prev := slog.Default()
		slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: level})))
		defer slog.SetDefault(prev)
		rec := httptest.NewRecorder()
		withLogging(next).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/anything", nil))
		return buf.String()
	}

	if out := run(slog.LevelInfo); strings.Contains(out, `"msg":"request"`) {
		t.Errorf("access log written at info level (hot-path stall risk): %s", out)
	}
	if out := run(slog.LevelDebug); !strings.Contains(out, `"msg":"request"`) {
		t.Errorf("access log not written at debug level: %s", out)
	}
}
