package web

import (
	"log/slog"
	"net/http"
	"net/url"
	"strings"
)

// csrfGuard rejects cross-site unsafe (POST/PUT/PATCH/DELETE) requests by
// requiring the Origin/Referer, when present, to match the request Host. Hespera
// binds a loopback (or LAN) port with no authentication, so without this any web
// page you visit could POST to http://127.0.0.1:<port>/libraries/delete or
// /shutdown in the background. GET/HEAD and same-origin unsafe requests pass;
// requests that omit both Origin and Referer are allowed (a same-origin
// navigation legitimately omits Origin — a forged cross-site fetch cannot). This
// was previously bundled inside the (now-removed) auth middleware and only ran
// when auth was enabled; it now wraps the mux unconditionally.
func csrfGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isSameOriginUnsafeRequest(r) {
			slog.Warn("csrf denied", "method", r.Method, "path", r.URL.Path)
			if requestWantsJSON(r) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"ok":false,"message":"cross-site request forbidden"}`))
				return
			}
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isSameOriginUnsafeRequest reports whether r is safe to process: a safe method,
// or an unsafe method whose Origin/Referer matches the request Host (or omits
// both). Extracted verbatim from the removed auth package.
func isSameOriginUnsafeRequest(r *http.Request) bool {
	if r == nil || !isUnsafeMethod(r.Method) {
		return true
	}
	reqHost := strings.TrimSpace(r.Host)
	if reqHost == "" {
		return true
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin != "" {
		u, err := url.Parse(origin)
		if err != nil || strings.TrimSpace(u.Host) == "" {
			return false
		}
		return sameHostPort(reqHost, u.Host)
	}
	referer := strings.TrimSpace(r.Header.Get("Referer"))
	if referer != "" {
		u, err := url.Parse(referer)
		if err != nil || strings.TrimSpace(u.Host) == "" {
			return false
		}
		return sameHostPort(reqHost, u.Host)
	}
	return true
}

func isUnsafeMethod(method string) bool {
	switch strings.ToUpper(strings.TrimSpace(method)) {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

func sameHostPort(a, b string) bool {
	hostA, portA := splitHostPort(a)
	hostB, portB := splitHostPort(b)
	if !strings.EqualFold(hostA, hostB) {
		return false
	}
	if portA == "" && portB == "" {
		return true
	}
	return portA == portB
}

func splitHostPort(v string) (host, port string) {
	u, err := url.Parse("//" + strings.TrimSpace(v))
	if err != nil {
		return "", ""
	}
	return strings.ToLower(strings.TrimSpace(u.Hostname())), strings.TrimSpace(u.Port())
}
