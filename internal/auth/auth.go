package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"isomedia/internal/config"
)

const (
	sessionCookieName = "isomedia_session"
	preAuthCookieName = "isomedia_preauth"
)

var (
	errChallengeExpired = errors.New("challenge expired")
	errInvalidSession   = errors.New("invalid session")
)

type authConfig struct {
	Enabled         bool
	SessionSecret   string
	Namespace       string
	SSHKeygenPath   string
	SessionTTL      time.Duration
	ChallengeTTL    time.Duration
	MaxVerifyPerMin int
}

type Manager struct {
	cfg       authConfig
	store     *Store
	verifySSH func(ctx context.Context, in VerifyInput) error
	now       func() time.Time

	mu         sync.Mutex
	challenges map[string]challengeState
	rateByIP   map[string][]time.Time
}

type VerifyInput struct {
	SSHKeygenPath string
	Username      string
	Namespace     string
	Payload       string
	Signature     string
	PublicKeys    []string
}

type challengeState struct {
	Value     string
	ExpiresAt time.Time
	Used      bool
	Attempts  int
	IP        string
}

type sessionClaims struct {
	Username string `json:"u"`
	Exp      int64  `json:"exp"`
	Nonce    string `json:"n"`
}

type Challenge struct {
	Value     string
	ExpiresAt time.Time
}

func New(cfg config.Config, db *sql.DB) *Manager {
	ac := authConfig{
		Enabled:         cfg.AuthEnabled,
		SessionSecret:   cfg.AuthSessionSecret,
		Namespace:       cfg.SSHAuthNamespace,
		SSHKeygenPath:   cfg.SSHKeygenPath,
		SessionTTL:      24 * time.Hour,
		ChallengeTTL:    10 * time.Minute,
		MaxVerifyPerMin: 10,
	}
	if strings.TrimSpace(ac.Namespace) == "" {
		ac.Namespace = "isomedia"
	}
	if strings.TrimSpace(ac.SSHKeygenPath) == "" {
		ac.SSHKeygenPath = "ssh-keygen"
	}
	return &Manager{
		cfg:        ac,
		store:      NewStore(db),
		verifySSH:  verifyWithSSHKeygen,
		now:        time.Now,
		challenges: map[string]challengeState{},
		rateByIP:   map[string][]time.Time{},
	}
}

func (m *Manager) Enabled() bool {
	return m != nil && m.cfg.Enabled
}

func (m *Manager) Namespace() string {
	if m == nil {
		return "isomedia"
	}
	return m.cfg.Namespace
}

func (m *Manager) CreateChallenge(w http.ResponseWriter, r *http.Request) (Challenge, error) {
	if m == nil {
		return Challenge{}, errors.New("auth manager is not initialized")
	}
	preAuth, err := m.ensurePreAuthCookie(w, r)
	if err != nil {
		return Challenge{}, err
	}
	now := m.now().UTC()
	m.mu.Lock()
	m.pruneLocked(now)
	if existing, ok := m.challenges[preAuth]; ok && !existing.Used && now.Before(existing.ExpiresAt) {
		m.mu.Unlock()
		return Challenge{Value: existing.Value, ExpiresAt: existing.ExpiresAt}, nil
	}
	m.mu.Unlock()

	challenge, err := randomToken(24)
	if err != nil {
		return Challenge{}, err
	}
	entry := challengeState{
		Value:     challenge,
		ExpiresAt: now.Add(m.cfg.ChallengeTTL),
		IP:        clientIP(r),
	}
	m.mu.Lock()
	m.challenges[preAuth] = entry
	m.mu.Unlock()
	slog.Info("auth challenge issued", "ip", entry.IP)
	return Challenge{Value: challenge, ExpiresAt: entry.ExpiresAt}, nil
}

func (m *Manager) VerifyAndStartSession(w http.ResponseWriter, r *http.Request, username, signature string) error {
	if m == nil {
		return errors.New("auth manager is not initialized")
	}
	ip := clientIP(r)
	if !m.allowVerifyAttempt(ip) {
		slog.Warn("auth verify denied", "ip", ip, "reason", "rate_limited")
		return errors.New("too many verify attempts; try again shortly")
	}
	preAuthCookie, err := r.Cookie(preAuthCookieName)
	if err != nil {
		return errChallengeExpired
	}

	now := m.now().UTC()
	m.mu.Lock()
	m.pruneLocked(now)
	entry, ok := m.challenges[preAuthCookie.Value]
	if !ok {
		m.mu.Unlock()
		return errChallengeExpired
	}
	if entry.Used || now.After(entry.ExpiresAt) {
		delete(m.challenges, preAuthCookie.Value)
		m.mu.Unlock()
		return errChallengeExpired
	}
	entry.Attempts++
	if entry.Attempts > 5 {
		delete(m.challenges, preAuthCookie.Value)
		m.mu.Unlock()
		return errors.New("challenge attempt limit reached")
	}
	m.challenges[preAuthCookie.Value] = entry
	m.mu.Unlock()

	username, err = normalizeUsername(username)
	if err != nil {
		return err
	}
	signature = strings.TrimSpace(signature)
	if signature == "" {
		return errors.New("signature is required")
	}

	keys, err := m.store.PublicKeysByUsername(r.Context(), username)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("unknown user or no SSH keys configured for %q", username)
		}
		return err
	}

	if err := m.verifySSH(r.Context(), VerifyInput{
		SSHKeygenPath: m.cfg.SSHKeygenPath,
		Username:      username,
		Namespace:     m.cfg.Namespace,
		Payload:       entry.Value,
		Signature:     signature,
		PublicKeys:    keys,
	}); err != nil {
		slog.Warn("auth verify denied", "ip", ip, "user", username, "reason", "signature_invalid")
		return errors.New("signature verification failed")
	}

	m.mu.Lock()
	entry.Used = true
	m.challenges[preAuthCookie.Value] = entry
	m.mu.Unlock()
	slog.Info("auth verify success", "ip", ip, "user", username)
	return m.setSessionCookie(w, r, username)
}

func (m *Manager) ClearSession(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
		SameSite: http.SameSiteLaxMode,
		Secure:   useSecureCookies(r),
	})
}

func (m *Manager) CurrentUsername(r *http.Request) string {
	if r == nil {
		return ""
	}
	if v, ok := UsernameFromContext(r.Context()); ok {
		return v
	}
	if m == nil || !m.Enabled() {
		return ""
	}
	username, err := m.sessionUsername(r)
	if err != nil {
		return ""
	}
	return username
}

func (m *Manager) Middleware(next http.Handler) http.Handler {
	if m == nil || !m.Enabled() {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isPublicPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		username, err := m.sessionUsername(r)
		if err == nil {
			if !isSameOriginUnsafeRequest(r) {
				slog.Warn("csrf denied", "ip", clientIP(r), "method", r.Method, "path", r.URL.Path)
				if wantsJSON(r) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusForbidden)
					_, _ = w.Write([]byte(`{"ok":false,"message":"cross-site request forbidden"}`))
					return
				}
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r.WithContext(WithUsername(r.Context(), username)))
			return
		}
		if wantsJSON(r) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"ok":false,"message":"authentication required"}`))
			return
		}
		nextURL := safeLocalPathWithQuery(r)
		http.Redirect(w, r, "/login?next="+url.QueryEscape(nextURL), http.StatusSeeOther)
	})
}

// context key for username

type ctxKey struct{}

func WithUsername(ctx context.Context, username string) context.Context {
	return context.WithValue(ctx, ctxKey{}, username)
}

func UsernameFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(ctxKey{}).(string)
	return v, ok
}

// internal helpers

func (m *Manager) setSessionCookie(w http.ResponseWriter, r *http.Request, username string) error {
	if strings.TrimSpace(m.cfg.SessionSecret) == "" {
		return errors.New("AUTH_SESSION_SECRET is required")
	}
	nonce, err := randomToken(18)
	if err != nil {
		return err
	}
	claims := sessionClaims{
		Username: username,
		Exp:      m.now().UTC().Add(m.cfg.SessionTTL).Unix(),
		Nonce:    nonce,
	}
	raw, err := json.Marshal(claims)
	if err != nil {
		return err
	}
	payload := base64.RawURLEncoding.EncodeToString(raw)
	sig := signHMACSHA256([]byte(m.cfg.SessionSecret), payload)
	token := payload + "." + base64.RawURLEncoding.EncodeToString(sig)
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   useSecureCookies(r),
		MaxAge:   int(m.cfg.SessionTTL.Seconds()),
		Expires:  m.now().UTC().Add(m.cfg.SessionTTL),
	})
	return nil
}

func (m *Manager) sessionUsername(r *http.Request) (string, error) {
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return "", errInvalidSession
	}
	parts := strings.Split(c.Value, ".")
	if len(parts) != 2 {
		return "", errInvalidSession
	}
	expected := signHMACSHA256([]byte(m.cfg.SessionSecret), parts[0])
	got, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(expected, got) {
		return "", errInvalidSession
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", errInvalidSession
	}
	var claims sessionClaims
	if err := json.Unmarshal(raw, &claims); err != nil {
		return "", errInvalidSession
	}
	if claims.Exp <= m.now().UTC().Unix() {
		return "", errInvalidSession
	}
	username, err := normalizeUsername(claims.Username)
	if err != nil {
		return "", errInvalidSession
	}
	return username, nil
}

func (m *Manager) ensurePreAuthCookie(w http.ResponseWriter, r *http.Request) (string, error) {
	if c, err := r.Cookie(preAuthCookieName); err == nil {
		if strings.TrimSpace(c.Value) != "" {
			return c.Value, nil
		}
	}
	tok, err := randomToken(18)
	if err != nil {
		return "", err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     preAuthCookieName,
		Value:    tok,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   useSecureCookies(r),
		MaxAge:   int((24 * time.Hour).Seconds()),
		Expires:  time.Now().Add(24 * time.Hour),
	})
	return tok, nil
}

func (m *Manager) allowVerifyAttempt(ip string) bool {
	if strings.TrimSpace(ip) == "" {
		ip = "unknown"
	}
	now := m.now().UTC()
	windowStart := now.Add(-time.Minute)
	m.mu.Lock()
	defer m.mu.Unlock()
	existing := m.rateByIP[ip]
	keep := existing[:0]
	for _, ts := range existing {
		if ts.After(windowStart) {
			keep = append(keep, ts)
		}
	}
	if len(keep) >= m.cfg.MaxVerifyPerMin {
		m.rateByIP[ip] = keep
		return false
	}
	keep = append(keep, now)
	m.rateByIP[ip] = keep
	return true
}

func (m *Manager) pruneLocked(now time.Time) {
	for k, v := range m.challenges {
		if v.Used || now.After(v.ExpiresAt) {
			delete(m.challenges, k)
		}
	}
	// Evict rate-limiter entries for IPs with no attempts in the current
	// window; otherwise the map grows unbounded, one entry per distinct IP
	// that ever made a request.
	windowStart := now.Add(-time.Minute)
	for ip, ts := range m.rateByIP {
		fresh := ts[:0]
		for _, t := range ts {
			if t.After(windowStart) {
				fresh = append(fresh, t)
			}
		}
		if len(fresh) == 0 {
			delete(m.rateByIP, ip)
		} else {
			m.rateByIP[ip] = fresh
		}
	}
}

func signHMACSHA256(secret []byte, payload string) []byte {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(payload))
	return mac.Sum(nil)
}

func randomToken(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		return strings.TrimSpace(r.RemoteAddr)
	}
	return host
}

func isPublicPath(p string) bool {
	switch {
	case p == "/healthz":
		return true
	case p == "/favicon.ico":
		return true
	case p == "/login":
		return true
	case p == "/auth/challenge":
		return true
	case p == "/auth/verify":
		return true
	case p == "/auth/logout":
		return true
	case strings.HasPrefix(p, "/static/"):
		return true
	case strings.HasPrefix(p, "/art/"):
		return true
	default:
		return false
	}
}

func wantsJSON(r *http.Request) bool {
	accept := strings.ToLower(r.Header.Get("Accept"))
	if strings.Contains(accept, "application/json") {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Requested-With")), "XMLHttpRequest")
}

func safeLocalPathWithQuery(r *http.Request) string {
	if r == nil || r.URL == nil {
		return "/"
	}
	path := strings.TrimSpace(r.URL.Path)
	if path == "" || !strings.HasPrefix(path, "/") {
		path = "/"
	}
	if r.URL.RawQuery != "" {
		path += "?" + r.URL.RawQuery
	}
	return path
}

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

func useSecureCookies(r *http.Request) bool {
	if r == nil {
		return false
	}
	if r.TLS != nil {
		return true
	}
	proto := strings.TrimSpace(strings.ToLower(r.Header.Get("X-Forwarded-Proto")))
	if proto == "" {
		return false
	}
	if i := strings.Index(proto, ","); i >= 0 {
		proto = strings.TrimSpace(proto[:i])
	}
	return proto == "https"
}

func verifyWithSSHKeygen(ctx context.Context, in VerifyInput) error {
	if strings.TrimSpace(in.Signature) == "" {
		return errors.New("signature is required")
	}
	if len(in.PublicKeys) == 0 {
		return errors.New("no public keys configured")
	}
	normalizedSig, err := normalizeArmoredSignature(in.Signature)
	if err != nil {
		return err
	}

	tmpDir, err := os.MkdirTemp("", "isomedia-auth-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	allowedSignersPath := filepath.Join(tmpDir, "allowed_signers")
	var b strings.Builder
	for _, key := range in.PublicKeys {
		k := strings.TrimSpace(key)
		if k == "" {
			continue
		}
		b.WriteString(in.Username)
		b.WriteByte(' ')
		b.WriteString(k)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(allowedSignersPath, []byte(b.String()), 0o600); err != nil {
		return err
	}

	sigPath := filepath.Join(tmpDir, "sig")
	if err := os.WriteFile(sigPath, []byte(normalizedSig), 0o600); err != nil {
		return err
	}

	attempts := []struct {
		name    string
		payload string
	}{
		{name: "exact", payload: in.Payload},
		{name: "with_newline", payload: in.Payload + "\n"},
	}
	var lastErr error
	for _, a := range attempts {
		cmd := exec.CommandContext(ctx, in.SSHKeygenPath, "-Y", "verify", "-f", allowedSignersPath, "-I", in.Username, "-n", in.Namespace, "-s", sigPath)
		cmd.Stdin = strings.NewReader(a.payload)
		out, err := cmd.CombinedOutput()
		if err == nil {
			return nil
		}
		lastErr = fmt.Errorf("payload_mode=%s: %w (%s)", a.name, err, strings.TrimSpace(string(out)))
	}
	return fmt.Errorf("ssh-keygen verify failed: %v", lastErr)
}

func normalizeArmoredSignature(v string) (string, error) {
	const begin = "-----BEGIN SSH SIGNATURE-----"
	const end = "-----END SSH SIGNATURE-----"

	v = strings.ReplaceAll(v, "\r\n", "\n")
	v = strings.ReplaceAll(v, "\r", "\n")
	lines := strings.Split(v, "\n")

	beginIdx := -1
	endIdx := -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if beginIdx < 0 && trimmed == begin {
			beginIdx = i
			continue
		}
		if beginIdx >= 0 && trimmed == end {
			endIdx = i
			break
		}
	}
	if beginIdx < 0 || endIdx < 0 || endIdx <= beginIdx {
		return "", errors.New("invalid SSH signature block")
	}

	out := make([]string, 0, endIdx-beginIdx+1)
	out = append(out, begin)
	for i := beginIdx + 1; i < endIdx; i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		out = append(out, line)
	}
	out = append(out, end)
	return strings.Join(out, "\n") + "\n", nil
}
