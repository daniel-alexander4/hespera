package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// stubReleaseServer serves a GitHub releases-latest response and repoints
// githubLatestURL at itself for the test's duration. status 404 = "no releases
// published yet" (also what a not-yet-created repo answers).
func stubReleaseServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	orig := githubLatestURL
	githubLatestURL = srv.URL
	t.Cleanup(func() { githubLatestURL = orig; srv.Close() })
	return srv
}

func getUpdateCheck(t *testing.T, h *Handler, query string) updateResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/update/check"+query, nil)
	rec := httptest.NewRecorder()
	h.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	var resp updateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	return resp
}

func TestUpdateCheckNewerRelease(t *testing.T) {
	stubReleaseServer(t, 200, `{
		"tag_name": "v9.9.9",
		"html_url": "https://example.test/releases/v9.9.9",
		"assets": [
			{"name": "hespera_9.9.9_amd64.deb", "browser_download_url": "https://example.test/hespera_9.9.9_amd64.deb"},
			{"name": "hespera-9.9.9-linux-amd64", "browser_download_url": "https://example.test/hespera-9.9.9-linux-amd64"}
		]
	}`)
	h, _ := newTestHandler(t)
	resp := getUpdateCheck(t, h, "")
	if !resp.Enabled {
		t.Fatal("manual check must always run (enabled=true)")
	}
	if resp.Latest != "9.9.9" || !resp.Available {
		t.Fatalf("latest=%q available=%v, want 9.9.9/true", resp.Latest, resp.Available)
	}
	// The test binary runs from a temp dir (not /usr), so the standalone raw
	// binary asset is expected on linux/amd64; on other platforms no asset
	// matches and DownloadURL is empty (the client falls back to the page).
	if resp.URL != "https://example.test/releases/v9.9.9" {
		t.Fatalf("URL = %q", resp.URL)
	}
}

func TestUpdateCheckCurrent(t *testing.T) {
	stubReleaseServer(t, 200, `{"tag_name": "v0.0.0", "html_url": "https://example.test/r", "assets": []}`)
	h, _ := newTestHandler(t)
	resp := getUpdateCheck(t, h, "")
	// h.version is "dev" (0.0.0) in tests → not less than 0.0.0 → up to date.
	if resp.Available {
		t.Fatalf("available = true for equal versions (current %q latest %q)", resp.Current, resp.Latest)
	}
	if resp.Latest != "0.0.0" {
		t.Fatalf("latest = %q, want 0.0.0", resp.Latest)
	}
}

func TestUpdateCheckNoReleases(t *testing.T) {
	stubReleaseServer(t, 404, `{"message": "Not Found"}`)
	h, _ := newTestHandler(t)
	resp := getUpdateCheck(t, h, "")
	if resp.Available || resp.Latest != "" {
		t.Fatalf("404 must read as no releases: available=%v latest=%q", resp.Available, resp.Latest)
	}
}

func TestUpdateCheckAutoRespectsToggle(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write([]byte(`{"tag_name": "v9.9.9", "html_url": "https://example.test/r", "assets": []}`))
	}))
	orig := githubLatestURL
	githubLatestURL = srv.URL
	t.Cleanup(func() { githubLatestURL = orig; srv.Close() })
	h, db := newTestHandler(t)

	// Toggle off (default): the auto check answers without contacting the server.
	resp := getUpdateCheck(t, h, "?auto=1")
	if resp.Enabled {
		t.Fatal("auto check with the toggle off must report enabled=false")
	}
	if hits != 0 {
		t.Fatalf("auto check with the toggle off contacted the release server (%d hits)", hits)
	}

	// Toggle on: the auto check runs.
	if _, err := db.Exec("INSERT INTO app_settings (key, value) VALUES ('update_check_enabled', '1')"); err != nil {
		t.Fatal(err)
	}
	resp = getUpdateCheck(t, h, "?auto=1")
	if !resp.Enabled || !resp.Available {
		t.Fatalf("auto check with the toggle on: enabled=%v available=%v, want true/true", resp.Enabled, resp.Available)
	}
	if hits != 1 {
		t.Fatalf("release server hits = %d, want 1", hits)
	}

	// A manual check (no ?auto) runs even with the toggle off.
	if _, err := db.Exec("DELETE FROM app_settings WHERE key='update_check_enabled'"); err != nil {
		t.Fatal(err)
	}
	resp = getUpdateCheck(t, h, "")
	if !resp.Enabled || hits != 2 {
		t.Fatalf("manual check with the toggle off: enabled=%v hits=%d, want true/2", resp.Enabled, hits)
	}
}

func TestUpdateCheckUnreachable(t *testing.T) {
	srv := stubReleaseServer(t, 200, "{}")
	srv.Close() // connection refused from here on
	h, _ := newTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/update/check", nil)
	rec := httptest.NewRecorder()
	h.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
}

func TestAssetURL(t *testing.T) {
	assets := []releaseAsset{
		{Name: "LICENSE", URL: "u0"},
		{Name: "hespera_1.2.3_amd64.deb", URL: "u1"},
		{Name: "hespera_1.2.3_arm64.deb", URL: "u2"},
		{Name: "hespera-1.2.3-linux-amd64", URL: "u3"},
		{Name: "hespera-1.2.3-darwin-arm64", URL: "u4"},
		{Name: "hespera-1.2.3-windows-amd64.exe", URL: "u5"},
	}
	cases := []struct {
		goos, goarch string
		managed      bool
		want         string
	}{
		{"linux", "amd64", true, "u1"},
		{"linux", "arm64", true, "u2"},
		{"linux", "amd64", false, "u3"},
		{"darwin", "arm64", false, "u4"},
		{"windows", "amd64", false, "u5"},
		{"linux", "riscv64", false, ""}, // no matching asset
	}
	for _, c := range cases {
		if got := assetURL(c.goos, c.goarch, c.managed, assets); got != c.want {
			t.Errorf("assetURL(%s/%s managed=%v) = %q, want %q", c.goos, c.goarch, c.managed, got, c.want)
		}
	}
}

func TestVersionLess(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"0.25.5", "0.25.6", true},
		{"0.25.5", "0.26.0", true},
		{"0.25.5", "1.0.0", true},
		{"0.25.5", "0.25.5", false},
		{"0.26.0", "0.25.9", false},
		{"dev", "0.0.1", true},    // non-numeric counts as 0
		{"dev", "0.0.0", false},   // equal after parsing
		{"v1.2.3", "1.2.4", true}, // tolerates a v prefix
		{"1.2", "1.2.1", true},    // missing part counts as 0
	}
	for _, c := range cases {
		if got := versionLess(c.a, c.b); got != c.want {
			t.Errorf("versionLess(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestUpdateCheckEnabledToggle(t *testing.T) {
	h, db := newTestHandler(t)
	ctx := context.Background()
	if h.effectiveUpdateCheckEnabled(ctx) {
		t.Fatal("update_check_enabled must default off")
	}
	if _, err := db.Exec("INSERT INTO app_settings (key, value) VALUES ('update_check_enabled', '1')"); err != nil {
		t.Fatal(err)
	}
	if !h.effectiveUpdateCheckEnabled(ctx) {
		t.Fatal("stored '1' must read as on")
	}
}
