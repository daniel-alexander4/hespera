package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func healthzServer(t *testing.T, status int, body, header string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		if header != "" {
			w.Header().Set("X-Hespera", header)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestRunningAppURL pins the attach probe: only a live server that positively
// identifies as Hespera (200, body "ok", X-Hespera header) is attachable —
// a reused port running something else, a dead server, or a garbage file all
// answer "" so startup proceeds normally.
func TestRunningAppURL(t *testing.T) {
	t.Run("healthy instance attaches", func(t *testing.T) {
		dir := t.TempDir()
		srv := healthzServer(t, 200, "ok", "0.1.0")
		writeAppURL(dir, srv.URL+"/")
		if got := runningAppURL(dir); got != srv.URL+"/" {
			t.Fatalf("runningAppURL = %q, want %q", got, srv.URL+"/")
		}
	})

	t.Run("no discovery file", func(t *testing.T) {
		if got := runningAppURL(t.TempDir()); got != "" {
			t.Fatalf("runningAppURL = %q, want empty", got)
		}
	})

	t.Run("dead server", func(t *testing.T) {
		dir := t.TempDir()
		srv := healthzServer(t, 200, "ok", "0.1.0")
		writeAppURL(dir, srv.URL+"/")
		srv.Close()
		if got := runningAppURL(dir); got != "" {
			t.Fatalf("runningAppURL = %q, want empty", got)
		}
	})

	t.Run("not hespera — missing identity header", func(t *testing.T) {
		dir := t.TempDir()
		srv := healthzServer(t, 200, "ok", "")
		writeAppURL(dir, srv.URL+"/")
		if got := runningAppURL(dir); got != "" {
			t.Fatalf("attached to a non-Hespera server: %q", got)
		}
	})

	t.Run("not hespera — wrong body", func(t *testing.T) {
		dir := t.TempDir()
		srv := healthzServer(t, 200, "welcome to nginx", "x")
		writeAppURL(dir, srv.URL+"/")
		if got := runningAppURL(dir); got != "" {
			t.Fatalf("attached to a wrong-body server: %q", got)
		}
	})

	t.Run("unhealthy status", func(t *testing.T) {
		dir := t.TempDir()
		srv := healthzServer(t, 503, "ok", "0.1.0")
		writeAppURL(dir, srv.URL+"/")
		if got := runningAppURL(dir); got != "" {
			t.Fatalf("attached to an unhealthy server: %q", got)
		}
	})

	t.Run("garbage file", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(appURLPath(dir), []byte("not a url\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := runningAppURL(dir); got != "" {
			t.Fatalf("runningAppURL = %q, want empty", got)
		}
	})
}

// TestRecordedAppURL pins the unprobed reader used by the socket-liveness
// backstop: it returns the recorded loopback URL as-is, with no health probe,
// and "" for a missing or non-loopback file.
func TestRecordedAppURL(t *testing.T) {
	t.Run("reads a recorded loopback url without probing", func(t *testing.T) {
		dir := t.TempDir()
		writeAppURL(dir, "http://127.0.0.1:4321/")
		if got := recordedAppURL(dir); got != "http://127.0.0.1:4321/" {
			t.Fatalf("recordedAppURL = %q, want the recorded URL", got)
		}
	})
	t.Run("no file", func(t *testing.T) {
		if got := recordedAppURL(t.TempDir()); got != "" {
			t.Fatalf("recordedAppURL = %q, want empty", got)
		}
	})
	t.Run("non-http content rejected", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(appURLPath(dir), []byte("garbage\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := recordedAppURL(dir); got != "" {
			t.Fatalf("recordedAppURL = %q, want empty", got)
		}
	})
}

// TestRemoveAppURLOnlyOwn pins the shutdown rule: an instance only removes the
// discovery file while it still holds its OWN url — a newer instance's record
// must survive an older instance's clean exit.
func TestRemoveAppURLOnlyOwn(t *testing.T) {
	dir := t.TempDir()
	writeAppURL(dir, "http://127.0.0.1:1111/")
	removeAppURL(dir, "http://127.0.0.1:1111/")
	if _, err := os.Stat(appURLPath(dir)); !os.IsNotExist(err) {
		t.Fatal("own url not removed")
	}

	writeAppURL(dir, "http://127.0.0.1:2222/") // a newer instance's record
	removeAppURL(dir, "http://127.0.0.1:1111/")
	if _, err := os.Stat(appURLPath(dir)); err != nil {
		t.Fatal("a newer instance's record was removed by an older instance's shutdown")
	}
}
