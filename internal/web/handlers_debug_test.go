package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// The key tracer: off by default (GET reports disabled, POST refuses and writes
// nothing), armed by the keytrace_enabled app-setting (POST appends a
// server-timestamped JSON line to DataDir/keytrace.jsonl), and rejects
// non-JSON payloads.
func TestKeytrace(t *testing.T) {
	h, db := newTestHandler(t)

	get := func() bool {
		rec := httptest.NewRecorder()
		h.keytrace(rec, httptest.NewRequest(http.MethodGet, "/debug/keytrace", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET status = %d", rec.Code)
		}
		var body struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("GET body: %v", err)
		}
		return body.Enabled
	}
	post := func(payload string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/debug/keytrace", strings.NewReader(payload))
		h.keytrace(rec, req)
		return rec.Code
	}

	// Default off: probe says disabled, events are refused and no file appears.
	if get() {
		t.Fatal("keytrace should be disabled by default")
	}
	if code := post(`{"type":"key","key":"ArrowDown"}`); code != http.StatusForbidden {
		t.Fatalf("POST while disabled = %d, want 403", code)
	}
	if _, err := os.Stat(h.keytracePath()); !os.IsNotExist(err) {
		t.Fatalf("trace file should not exist while disabled (stat err=%v)", err)
	}

	// Enabled: probe flips, valid events append, junk is rejected.
	if _, err := db.Exec("INSERT INTO app_settings (key, value) VALUES ('keytrace_enabled', '1')"); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if !get() {
		t.Fatal("keytrace should report enabled")
	}
	if code := post(`not json`); code != http.StatusBadRequest {
		t.Fatalf("POST non-JSON = %d, want 400", code)
	}
	if code := post(`{"type":"key","key":"ArrowDown","code":"ArrowDown"}`); code != http.StatusNoContent {
		t.Fatalf("POST valid = %d, want 204", code)
	}
	if code := post(`{"type":"nav","url":"/tv"}`); code != http.StatusNoContent {
		t.Fatalf("POST second = %d, want 204", code)
	}
	data, err := os.ReadFile(h.keytracePath())
	if err != nil {
		t.Fatalf("read trace: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("trace lines = %d, want 2 (non-JSON must not be written)", len(lines))
	}
	for _, line := range lines {
		var entry struct {
			TS    string         `json:"ts"`
			Event map[string]any `json:"event"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("trace line not valid JSON: %v (%s)", err, line)
		}
		if entry.TS == "" || entry.Event == nil {
			t.Fatalf("trace line missing ts/event: %s", line)
		}
	}
	if !strings.Contains(lines[0], `"ArrowDown"`) {
		t.Fatalf("first line should carry the key event: %s", lines[0])
	}
}
