package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
)

// gatedPaths is every route that must refuse an unauthenticated caller.
// TestRouteTableIsFullyCovered pins it against routes() itself, so a route added
// later cannot quietly arrive ungated.
var gatedPaths = []string{
	"/api/state", "/api/playlists", "/api/letters", "/api/artists",
	"/api/artist", "/api/album", "/api/play", "/api/jump", "/api/next",
	"/api/prev", "/api/pause", "/api/stop", "/api/server", "/api/noise",
	"/api/noise/start", "/art/album/1",
}

// TestProductionRoutesAreGated is the row this whole feature exists for: the
// gate being absent is the one failure it could have and never notice. It
// builds the handler through the SAME routes() serveControl calls, so a gate
// that is only present in a test helper fails here.
func TestProductionRoutesAreGated(t *testing.T) {
	h, a := newTestRoutes()
	for _, p := range gatedPaths {
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete} {
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, httptest.NewRequest(method, p, nil))
			if rr.Code != http.StatusUnauthorized {
				t.Errorf("%s %s without a cookie = %d, want 401", method, p, rr.Code)
			}
		}
		// A wrong cookie is not a cookie.
		rr := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, p, nil)
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "not-the-token"})
		h.ServeHTTP(rr, r)
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("GET %s with a wrong cookie = %d, want 401", p, rr.Code)
		}
	}
	// And the stimulus: with a cookie these same routes are NOT 401. Without
	// this the test above would pass against a handler that 401s everything.
	for _, p := range gatedPaths {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, signedIn(httptest.NewRequest(http.MethodGet, p, nil), a))
		if rr.Code == http.StatusUnauthorized {
			t.Errorf("GET %s WITH a valid cookie = 401; the gate refuses a real session", p)
		}
	}
}

// TestRouteTableIsFullyCovered reads routes() and fails if it registers a path
// that gatedPaths does not cover. The list above is a hand-kept mirror of the
// mux, and a hand-kept mirror rots — this is what stops it.
//
// ⚠️ It must match CONSTANT patterns too, not only string literals. An earlier
// version matched `mux.HandleFunc("..."` only, and a reviewer mutation-proved
// the hole: adding `mux.HandleFunc(dangerPath, ct.handleStop)` with
// `const dangerPath = "/api/wipe"` passed both this test AND
// TestProductionRoutesAreGated. The route table already uses that idiom for
// loginPath, so the hole was one keystroke from being real. An identifier this
// test cannot resolve is a FAILURE, not a skip — failing closed is the whole
// point of a coverage guard.
//
// Blind spot, stated rather than implied: it reads control.go only, so a route
// registered from another file would still slip past.
func TestRouteTableIsFullyCovered(t *testing.T) {
	src, err := os.ReadFile("control.go")
	if err != nil {
		t.Fatal(err)
	}
	// Constants the route table is allowed to register through, resolved to
	// their values so a renamed or re-pointed constant cannot hide a route.
	consts := map[string]string{"loginPath": loginPath}

	re := regexp.MustCompile(`mux\.Handle(?:Func)?\(\s*(?:"([^"]*)"|([A-Za-z_]\w*))`)
	ms := re.FindAllStringSubmatch(string(src), -1)
	if want := strings.Count(string(src), "mux.Handle"); len(ms) != want {
		t.Fatalf("matched %d route registrations but control.go contains %d `mux.Handle` calls — the regex has stopped seeing some", len(ms), want)
	}
	if len(ms) < 16 {
		t.Fatalf("only found %d route registrations — the regex has stopped matching routes()", len(ms))
	}

	covered := map[string]bool{}
	for _, p := range gatedPaths {
		covered[p] = true
	}
	for _, m := range ms {
		pat := m[1]
		if m[2] != "" {
			v, ok := consts[m[2]]
			if !ok {
				t.Errorf("route registered through constant %q, which this test cannot resolve — add it to consts and decide whether it is gated", m[2])
				continue
			}
			pat = v
		}
		switch {
		case pat == "/": // the static shell, deliberately public
		case pat == loginPath: // the way in, deliberately public
		case strings.HasSuffix(pat, "/"): // a subtree: covered by a concrete path
			found := false
			for _, g := range gatedPaths {
				if strings.HasPrefix(g, pat) {
					found = true
				}
			}
			if !found {
				t.Errorf("route %q is registered but no gatedPaths entry exercises it", pat)
			}
		case !covered[pat]:
			t.Errorf("route %q is registered but not in gatedPaths — is it gated?", pat)
		}
	}
}

func TestPublicAssetsAreNotGated(t *testing.T) {
	h, _ := newTestRoutes()
	for _, p := range []string{"/", "/index.html", "/app.js", "/app.css", "/sw.js", "/manifest.webmanifest", "/icons/icon-192.png"} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, p, nil))
		// 301 is legitimate here and only for /index.html: net/http's FileServer
		// canonicalises it away to "/". What must never happen is the gate
		// refusing it.
		if rr.Code == http.StatusUnauthorized || rr.Code == http.StatusBadRequest {
			t.Errorf("GET %s unauthenticated = %d — the shell must load so the sign-in can render", p, rr.Code)
		}
		if rr.Code != http.StatusOK && !(p == "/index.html" && rr.Code == http.StatusMovedPermanently) {
			t.Errorf("GET %s unauthenticated = %d, want 200", p, rr.Code)
		}
	}
	// A directory is not a regular file, so it is not public: /icons/ no longer
	// offers a listing.
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/icons/", nil))
	if rr.Code == http.StatusOK {
		t.Error("GET /icons/ unauthenticated returned a directory listing")
	}
}

// TestNonCanonicalPathsAreRefused pins D7. The first entry is the bypass this
// was written for: /art/%2e%2e/app.js decodes to /art/../app.js, which cleans
// to /app.js — a public file, so an exemption computed on the cleaned path
// would let it through — while ServeMux routes it to the GATED /art/ handler.
func TestNonCanonicalPathsAreRefused(t *testing.T) {
	h, a := newTestRoutes()
	for _, p := range []string{
		"/art/%2e%2e/app.js",
		"/art/%2e%2e/%2e%2e/app.js",
		"/art/%2e%2e/%2e%2e/healthz",
		"/art/album/1/%2e%2e/%2e%2e/%2e%2e/app.js",
		"/art/%2e%2e/%2e%2e/api/login",
		"//api/state",
		"/api/state/../app.js",
		"/./app.js",
	} {
		for _, signIn := range []bool{false, true} {
			rr := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, p, nil)
			if signIn {
				r = signedIn(r, a)
			}
			h.ServeHTTP(rr, r)
			if rr.Code != http.StatusBadRequest {
				t.Errorf("GET %q (signedIn=%v) = %d, want 400 — a non-canonical path must never be routed", p, signIn, rr.Code)
			}
		}
	}
}

func TestLoginEndpoint(t *testing.T) {
	post := func(h http.Handler, body string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, loginPath, strings.NewReader(body)))
		return rr
	}

	t.Run("correct passphrase sets a usable cookie", func(t *testing.T) {
		h, _ := newTestRoutes()
		rr := post(h, `{"passphrase":"Correct-Horse Battery "}`)
		if rr.Code != http.StatusOK {
			t.Fatalf("login = %d, want 200 (body %s)", rr.Code, rr.Body)
		}
		var c *http.Cookie
		for _, got := range rr.Result().Cookies() {
			if got.Name == sessionCookieName {
				c = got
			}
		}
		if c == nil {
			t.Fatal("no session cookie was set")
		}
		if !c.HttpOnly || c.Path != "/" || c.SameSite != http.SameSiteLaxMode {
			t.Fatalf("cookie attrs wrong: HttpOnly=%v Path=%q SameSite=%v", c.HttpOnly, c.Path, c.SameSite)
		}
		if c.Secure {
			t.Fatal("Secure is set — the remote is plain http, so the cookie would never be stored")
		}
		// The cookie it handed out must actually open the gate.
		rr2 := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/api/state", nil)
		r.AddCookie(c)
		h.ServeHTTP(rr2, r)
		if rr2.Code != http.StatusOK {
			t.Fatalf("the cookie login handed out was refused: %d", rr2.Code)
		}
	})

	t.Run("wrong passphrase", func(t *testing.T) {
		h, _ := newTestRoutes()
		rr := post(h, `{"passphrase":"wrong wrong wrong"}`)
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("= %d, want 401", rr.Code)
		}
		if len(rr.Result().Cookies()) != 0 {
			t.Fatal("a failed login set a cookie")
		}
	})

	t.Run("throttled after a burst", func(t *testing.T) {
		h, _ := newTestRoutes()
		for i := 0; i < attemptBurst; i++ {
			post(h, `{"passphrase":"wrong wrong wrong"}`)
		}
		if rr := post(h, `{"passphrase":"`+testPassphrase+`"}`); rr.Code != http.StatusTooManyRequests {
			t.Fatalf("= %d, want 429 even for the right passphrase once the bucket is empty", rr.Code)
		}
	})

	t.Run("wrong method", func(t *testing.T) {
		h, _ := newTestRoutes()
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, loginPath, nil))
		if rr.Code != http.StatusMethodNotAllowed {
			t.Fatalf("GET /api/login = %d, want 405", rr.Code)
		}
	})

	t.Run("oversized body", func(t *testing.T) {
		h, _ := newTestRoutes()
		rr := post(h, `{"passphrase":"`+strings.Repeat("a", maxLoginBody+1024)+`"}`)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("= %d, want 400", rr.Code)
		}
	})

	t.Run("no credential in force says so", func(t *testing.T) {
		h := newTestController().routes(newRefusingAuth(nil))
		if rr := post(h, `{"passphrase":"anything"}`); rr.Code != http.StatusServiceUnavailable {
			t.Fatalf("= %d, want 503 — a box with no passphrase must not show a sign-in that can never work", rr.Code)
		}
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/state", nil))
		if rr.Code != http.StatusServiceUnavailable {
			t.Fatalf("gated route on a credential-less box = %d, want 503", rr.Code)
		}
	})
}

func TestArtPathWhitelist(t *testing.T) {
	h, a := newTestRoutes()
	for _, p := range []string{"/art/artist/1", "/art/album/", "/art/album/abc", "/art/healthz", "/art/album/1/extra"} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, signedIn(httptest.NewRequest(http.MethodGet, p, nil), a))
		if rr.Code != http.StatusBadRequest {
			t.Errorf("GET %s = %d, want 400 — only /art/album/{id} may reach the upstream", p, rr.Code)
		}
	}
	// Stimulus: the shape the client actually uses is NOT rejected by the
	// whitelist. (The upstream is unreachable in tests, so it 502s — what
	// matters is that it is not the 400 above.)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, signedIn(httptest.NewRequest(http.MethodGet, "/art/album/12", nil), a))
	if rr.Code == http.StatusBadRequest {
		t.Error("GET /art/album/12 was rejected by the whitelist")
	}
}

func TestStatePublishesAuthCounters(t *testing.T) {
	h, a := newTestRoutes()
	// Drive the counters non-zero first: a zero here must not be readable as
	// "the gate is fine", and a counter never probed is a counter that might
	// count something that cannot happen.
	rr0 := httptest.NewRecorder()
	h.ServeHTTP(rr0, httptest.NewRequest(http.MethodGet, "/api/state", nil)) // denied
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, signedIn(httptest.NewRequest(http.MethodGet, "/api/state", nil), a)) // ok

	var got struct {
		Auth authCounts `json:"auth"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("state is not JSON: %v", err)
	}
	if got.Auth.OK == 0 {
		t.Error("auth.ok is zero after an authenticated request — the counter has no reader or no writer")
	}
	if got.Auth.NoCookie == 0 {
		t.Error("auth.noCookie is zero after a request with no cookie")
	}
	// And the OTHER denial cause, separately — the whole reason these are two
	// counters rather than one.
	rrBad := httptest.NewRecorder()
	rBad := httptest.NewRequest(http.MethodGet, "/api/state", nil)
	rBad.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "not-the-token"})
	h.ServeHTTP(rrBad, rBad)
	rr2 := httptest.NewRecorder()
	h.ServeHTTP(rr2, signedIn(httptest.NewRequest(http.MethodGet, "/api/state", nil), a))
	if err := json.Unmarshal(rr2.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Auth.Denied == 0 {
		t.Error("auth.denied is zero after a WRONG cookie — the two denial causes are not separated")
	}
}

// TestAnnounceDoesNotPointAtAFileThatIsWrong pins a real defect found in review.
//
// Two degraded starts hand back a working, in-memory passphrase while the file
// at passphrasePath() holds something ELSE or nothing: an unreadable file (the
// household's old passphrase is still on disk) and a failed write. In both the
// announce used to print "the passphrase is in <path>" — sending the operator
// to words that do not work, with the truth one stderr line further up a
// journal. Under systemd, where stdout is not a terminal, that message is the
// only thing they see.
func TestAnnounceDoesNotPointAtAFileThatIsWrong(t *testing.T) {
	a := newMemoryAuth(testPassphrase, nil)
	if !a.usable() {
		t.Fatal("setup: the fixture auth is not usable")
	}
	// Stimulus first: when the passphrase IS persisted, the path is printed.
	// Without this the assertion below would also pass against an announce that
	// prints nothing at all, ever.
	if out := captureStdout(t, func() { announcePassphrase(a, true) }); !strings.Contains(out, "the passphrase is in") {
		t.Fatalf("persisted start printed no path: %q", out)
	}
	if out := captureStdout(t, func() { announcePassphrase(a, false) }); strings.Contains(out, "the passphrase is in") {
		t.Fatalf("a NON-persisted passphrase was advertised as being in the file: %q", out)
	}
}
