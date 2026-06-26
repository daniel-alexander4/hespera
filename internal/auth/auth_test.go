package auth

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"hespera/internal/config"
	"hespera/internal/db"
)

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.sqlite")
	conn, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := db.Migrate(conn); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	cfg := config.Config{
		AuthEnabled:       true,
		AuthSessionSecret: "test-secret-at-least-16-chars!!",
		SSHAuthNamespace:  "hespera",
		SSHKeygenPath:     "ssh-keygen",
	}
	return New(cfg, conn)
}

func TestManagerEnabled(t *testing.T) {
	m := newTestManager(t)
	if !m.Enabled() {
		t.Fatalf("expected Enabled=true")
	}
}

func TestManagerNamespace(t *testing.T) {
	m := newTestManager(t)
	if m.Namespace() != "hespera" {
		t.Fatalf("expected namespace=hespera, got %q", m.Namespace())
	}
}

func TestCreateChallenge(t *testing.T) {
	m := newTestManager(t)
	r := httptest.NewRequest(http.MethodPost, "/auth/challenge", nil)
	r.RemoteAddr = "127.0.0.1:12345"
	w := httptest.NewRecorder()

	ch, err := m.CreateChallenge(w, r)
	if err != nil {
		t.Fatalf("CreateChallenge: %v", err)
	}
	if ch.Value == "" {
		t.Fatalf("expected non-empty challenge")
	}
	if ch.ExpiresAt.IsZero() {
		t.Fatalf("expected non-zero expiry")
	}
}

func TestCreateChallengeReuse(t *testing.T) {
	m := newTestManager(t)
	r := httptest.NewRequest(http.MethodPost, "/auth/challenge", nil)
	r.RemoteAddr = "127.0.0.1:12345"
	w := httptest.NewRecorder()

	ch1, err := m.CreateChallenge(w, r)
	if err != nil {
		t.Fatalf("CreateChallenge 1: %v", err)
	}

	// Reuse the preauth cookie.
	cookies := w.Result().Cookies()
	var preAuth *http.Cookie
	for _, c := range cookies {
		if c.Name == preAuthCookieName {
			preAuth = c
			break
		}
	}
	if preAuth == nil {
		t.Fatalf("expected preauth cookie")
	}

	r2 := httptest.NewRequest(http.MethodPost, "/auth/challenge", nil)
	r2.RemoteAddr = "127.0.0.1:12345"
	r2.AddCookie(preAuth)
	w2 := httptest.NewRecorder()

	ch2, err := m.CreateChallenge(w2, r2)
	if err != nil {
		t.Fatalf("CreateChallenge 2: %v", err)
	}
	if ch1.Value != ch2.Value {
		t.Fatalf("expected same challenge on reuse, got %q vs %q", ch1.Value, ch2.Value)
	}
}

func TestSessionCookieRoundtrip(t *testing.T) {
	m := newTestManager(t)

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	if err := m.setSessionCookie(w, r, "testuser"); err != nil {
		t.Fatalf("setSessionCookie: %v", err)
	}

	cookies := w.Result().Cookies()
	var sessionCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == sessionCookieName {
			sessionCookie = c
			break
		}
	}
	if sessionCookie == nil {
		t.Fatalf("expected session cookie")
	}

	r2 := httptest.NewRequest(http.MethodGet, "/", nil)
	r2.AddCookie(sessionCookie)

	username, err := m.sessionUsername(r2)
	if err != nil {
		t.Fatalf("sessionUsername: %v", err)
	}
	if username != "testuser" {
		t.Fatalf("expected username=testuser, got %q", username)
	}
}

func TestMiddlewarePublicPaths(t *testing.T) {
	m := newTestManager(t)
	handler := m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	publicPaths := []string{"/healthz", "/favicon.ico", "/login", "/static/app.css", "/auth/challenge"}
	for _, path := range publicPaths {
		t.Run(path, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, path, nil)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != http.StatusOK {
				t.Fatalf("expected 200 for %s, got %d", path, w.Code)
			}
		})
	}
}

func TestMiddlewareRedirectsUnauthenticated(t *testing.T) {
	m := newTestManager(t)
	handler := m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	r := httptest.NewRequest(http.MethodGet, "/settings", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect, got %d", w.Code)
	}
	loc := w.Header().Get("Location")
	if loc == "" {
		t.Fatalf("expected Location header")
	}
}

func TestRateLimit(t *testing.T) {
	m := newTestManager(t)
	m.cfg.MaxVerifyPerMin = 3

	ip := "10.0.0.1"
	for i := 0; i < 3; i++ {
		if !m.allowVerifyAttempt(ip) {
			t.Fatalf("expected attempt %d to be allowed", i)
		}
	}
	if m.allowVerifyAttempt(ip) {
		t.Fatalf("expected attempt to be denied after limit")
	}
}

func TestClearSession(t *testing.T) {
	m := newTestManager(t)
	r := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	w := httptest.NewRecorder()

	m.ClearSession(w, r)

	cookies := w.Result().Cookies()
	var found bool
	for _, c := range cookies {
		if c.Name == sessionCookieName && c.MaxAge < 0 {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected session cookie to be cleared")
	}
}

func TestExpiredSession(t *testing.T) {
	m := newTestManager(t)
	// Override time to create an already-expired session.
	originalNow := m.now
	m.now = func() time.Time { return time.Now().Add(-25 * time.Hour) }

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	if err := m.setSessionCookie(w, r, "testuser"); err != nil {
		t.Fatalf("setSessionCookie: %v", err)
	}

	m.now = originalNow

	cookies := w.Result().Cookies()
	var sessionCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == sessionCookieName {
			sessionCookie = c
			break
		}
	}

	r2 := httptest.NewRequest(http.MethodGet, "/", nil)
	r2.AddCookie(sessionCookie)

	_, err := m.sessionUsername(r2)
	if err == nil {
		t.Fatalf("expected error for expired session")
	}
}
