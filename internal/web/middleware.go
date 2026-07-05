package web

import (
	"log/slog"
	"net/http"
	"time"
)

type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wrote {
		w.status = code
		w.wrote = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.status = http.StatusOK
		w.wrote = true
	}
	return w.ResponseWriter.Write(b)
}

func withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		// Debug, not Info: this fires once per request (every HLS segment during
		// playback included), and it's a SYNCHRONOUS write to stdout in the request
		// goroutine through slog's process-wide mutex — so a stalling log sink (a
		// systemd journal on a busy disk) would add latency to request serving. At
		// the default info level slog drops this before the write, keeping the hot
		// path log-I/O-free; HESPERA_LOG_LEVEL=debug restores per-request access logs.
		slog.Debug("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.status,
			"duration", time.Since(start).String(),
		)
	})
}
