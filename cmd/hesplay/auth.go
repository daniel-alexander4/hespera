package main

// The phone remote's three-word passphrase gate.
//
// TWO values live in ~/.config/hespera/hesplay-passphrase, mode 0600:
//
//	correct horse battery
//	key: <32 random bytes, base64url>
//
// The first line is the passphrase, in plain text and deliberately so — the
// answer to "what was it again?" has to exist, hashing it would make the only
// recovery a rotate that signs the whole household out, and anyone who can read
// a 0600 file in this account's config directory can already replace it. That
// is the posture Hespera already takes with API keys.
//
// The second is a per-instance HMAC key, and the session cookie's value is
//
//	base64url(HMAC-SHA256(key, "hesplay-session-v1\x00" + normalized phrase))
//
// Deriving rather than storing a session token means the cookie survives a
// restart — so the needrestart that every apt deploy triggers does not sign the
// household out — and that editing the first line rotates the cookie by itself,
// with no second value to keep in step. HMAC rather than a bare hash because a
// bare SHA-256 over a 38.77-bit secret is reversible offline in about twenty
// seconds on one GPU: a stolen cookie would hand back the words people say out
// loud. With the key it hands back nothing.
//
// What the cookie is NOT: port-scoped. HTTP cookies are keyed by host, so this
// one is offered to any other HTTP service on the same hostname. That is a
// property of cookies rather than of this design, and the proportionate fix (a
// bearer token plus HMAC-signed art URLs, because <img> cannot carry a header)
// costs more than the exposure on a single-purpose box. Being host-keyed is
// also what makes DNS rebinding a non-event here: a rebound origin is a
// different host, so it sends no cookie and gets a 401.

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
)

const (
	// passphraseWordCount words, drawn from passphraseWords.
	passphraseWordCount = 3

	sessionCookieName = "hesplay_session"

	// sessionMaxAge is a year. Nothing shorter buys anything: the token is
	// derived, so expiring it only forces a household member to re-type three
	// words at a moment nobody chose.
	sessionMaxAge = 365 * 24 * 60 * 60

	// tokenDomain separates the cookie's HMAC input from any other use of the
	// same key. One string, and "the cookie is not the passphrase" becomes
	// structural rather than a claim.
	tokenDomain = "hesplay-session-v1\x00"

	// keyLine prefixes the instance key inside the passphrase file.
	keyLine = "key:"

	// reloadInterval bounds how often the passphrase file is re-read. The file
	// is a few dozen bytes, so this is one small read per second however busy
	// the remote is — and it is what makes `hesplay passphrase rotate` take
	// effect without a restart, which matters because the only reason to rotate
	// is believing you are compromised.
	reloadInterval = time.Second

	// maxPassphraseFile caps the read. A passphrase file is tens of bytes;
	// anything larger is a mistake or a mount gone wrong, and reading it into
	// memory helps nobody.
	maxPassphraseFile = 64 << 10

	// maxPassphraseInput caps a submitted passphrase. The HTTP handler bounds
	// its body too, but a module that DoSes when its caller forgets is a module
	// with a latent bug rather than a caller with one.
	maxPassphraseInput = 512

	// The login rate limit: a per-source-IP token bucket.
	//
	// Per-IP rather than global, deliberately. A single global deadline is a
	// remote lockout — one looping attacker (or a stuck retry) keeps it in the
	// future forever and the owner is refused while holding the right
	// passphrase. An attacker with many source addresses does get more attempts
	// than one with a single address; at 38.77 bits that does not matter, and
	// not locking the owner out of his own speakers does.
	attemptBurst  = 8
	attemptRefill = 4 * time.Second
	attemptIPCap  = 256 // bounded: the deleted internal/auth grew this map forever
)

var (
	// errNoPassphrase means "there is nothing usable here, generate one" — an
	// absent or blank file. It is NOT returned for a file that holds something
	// unusable; see readAuthFile.
	errNoPassphrase = errors.New("no passphrase")

	// errPassphraseUnreadable means the file exists and cannot be read at all
	// (a directory, a permission error). Nothing may be written over it.
	errPassphraseUnreadable = errors.New("passphrase file unreadable")

	// errPassphraseGarbled means the file read fine and held something, but
	// nothing survived normalization — a passphrase written in a non-Latin
	// script, or a corrupted file. Distinct from errNoPassphrase because
	// regenerating over it would destroy content someone deliberately put
	// there, and distinct from errPassphraseUnreadable because the file CAN be
	// moved aside and replaced.
	errPassphraseGarbled = errors.New("passphrase file holds no usable a-z words")
)

// auth holds the remote's credential and the login rate limiter.
//
// Construct with newAuth (file-backed) or newMemoryAuth (not persisted).
type auth struct {
	mu     sync.Mutex
	path   string // "" = never re-read from disk (newMemoryAuth)
	phrase string // normalized
	key    []byte // per-instance HMAC key
	token  string // derived from phrase+key

	// checked bounds the re-read rate. There is deliberately no mtime/size
	// cache: a restored backup (rsync -a, cp -p, tar -xp) reproduces both, so a
	// metadata comparison would miss a real rotation permanently. The file is
	// tiny; comparing its content is both simpler and correct.
	checked time.Time

	// warnedState latches the last condition reported from reloadLocked, so a
	// file that is garbled or short is announced once rather than once per
	// second for as long as it stays that way.
	warnedState string

	buckets map[string]*attemptBucket

	// now is injected so the limiter's own tests resolve finer than the
	// interval they assert. A limiter tested against the wall clock can only
	// report pass.
	now func() time.Time

	// Counters, read through snapshot(). Relaxed adds on the request path.
	okCount        atomic.Uint64
	deniedCount    atomic.Uint64
	noCookieCount  atomic.Uint64
	loginCount     atomic.Uint64
	failureCount   atomic.Uint64
	throttledCount atomic.Uint64
	reloadCount    atomic.Uint64
}

type attemptBucket struct {
	tokens float64
	last   time.Time
	// warned latches one stderr line per drain cycle. Logging every failed
	// attempt let anyone on the LAN fill the journal at will; logging every 429
	// is worse, since a throttled attempt is refused without consuming a token
	// and so is unbounded. One line when a source drains its bucket is the
	// signal — a single fat-fingered guess is not news, a sustained run is.
	warned bool
}

// authCounts is the shape /api/state publishes.
//
// ⚠️ Where the "is the gate even mounted?" tripwire actually lives, corrected
// after the phase review: NOT here. An all-zero reading would be that signal —
// but /api/state is itself GATED, so reaching it at all requires validToken to
// have succeeded, which has already incremented OK. Every snapshot a reader can
// obtain has OK >= 1, so the invariant is unobservable through its only
// publisher. The real reader is TestProductionRoutesAreGated, which builds the
// handler through the production constructor and asserts 401 on every route.
//
// What these counters ARE good for, live: Denied vs NoCookie separates "the
// household is presenting a stale cookie" from "nobody has signed in", and
// Throttled shows a limiter that is actually firing. Those are the questions a
// running box can answer about itself.
type authCounts struct {
	OK uint64 `json:"ok"`
	// Denied and NoCookie are two counters on purpose, not one "refused".
	// A refusal has two causes — a cookie that was presented and rejected, and
	// no cookie at all — and lumping them reads backwards exactly when it
	// matters: a household that cannot sign in looks identical to a household
	// that has not tried. Attribution beats aggregation (instrument.md).
	Denied    uint64 `json:"denied"`
	NoCookie  uint64 `json:"noCookie"`
	Logins    uint64 `json:"logins"`
	Failures  uint64 `json:"failures"`
	Throttled uint64 `json:"throttled"`
	Reloads   uint64 `json:"reloads"`
}

// countNoCookie records a refusal where no credential was presented at all.
// Its own counter rather than a branch of deniedCount — see authCounts.
func (a *auth) countNoCookie() { a.noCookieCount.Add(1) }

func (a *auth) snapshot() authCounts {
	return authCounts{
		OK:        a.okCount.Load(),
		Denied:    a.deniedCount.Load(),
		NoCookie:  a.noCookieCount.Load(),
		Logins:    a.loginCount.Load(),
		Failures:  a.failureCount.Load(),
		Throttled: a.throttledCount.Load(),
		Reloads:   a.reloadCount.Load(),
	}
}

// passphrasePath is the twin of serverConfigPath: same directory, same
// convention, its own file. It is deliberately NOT inside hesplay.json — that
// file is rewritten wholesale by PUT /api/noise from request JSON, so a secret
// living there would be readable and destroyable by the very surface this gate
// exists to close.
// ⚠️ A package var, not a plain function, and for a sharp reason:
// os.UserConfigDir() honours $XDG_CONFIG_HOME only on unix — on darwin and
// windows it ignores it entirely. hesplay ships binaries for both, so a test
// that isolated itself with t.Setenv("XDG_CONFIG_HOME", …) would silently write
// the DEVELOPER'S REAL passphrase file when the suite ran on a Mac. Tests
// override this instead. (The powerOff / noisePause seam shape.)
var passphrasePath = func() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "hespera", "hesplay-passphrase"), nil
}

// normalizePassphrase folds everything a phone keyboard can do to three typed
// words into one canonical form: every non-letter becomes a separator, letters
// fold to lower case, runs of separators collapse, and the ends are trimmed.
//
// So "Correct-Horse Battery " and "correct horse battery" are one passphrase.
// Doing it by dropping non-letters rather than by Unicode normalization is
// deliberate: the wordlist is pure a-z, NFKC would need golang.org/x/text, and
// hesplay has no third-party dependencies. The cost is that a passphrase of
// non-Latin letters normalizes to nothing at all — which is why a file holding
// one is treated as unusable and preserved, never silently replaced.
func normalizePassphrase(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		lower := unicode.ToLower(r)
		if lower >= 'a' && lower <= 'z' {
			b.WriteRune(lower)
			continue
		}
		b.WriteByte(' ')
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// deriveToken maps a normalized passphrase to its cookie value under key.
func deriveToken(key []byte, normalized string) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(tokenDomain + normalized))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// newInstanceKey draws the per-instance HMAC key.
func newInstanceKey() ([]byte, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("draw a session key: %w", err)
	}
	return key, nil
}

// generatePassphrase draws passphraseWordCount words with crypto/rand. rand.Int
// is unbiased for any list length, which is why the wordlist does not have to
// be a power of six.
func generatePassphrase() (string, error) {
	n := big.NewInt(int64(len(passphraseWords)))
	words := make([]string, passphraseWordCount)
	for i := range words {
		k, err := rand.Int(rand.Reader, n)
		if err != nil {
			return "", fmt.Errorf("draw a passphrase word: %w", err)
		}
		words[i] = passphraseWords[k.Int64()]
	}
	return strings.Join(words, " "), nil
}

// authFileBody renders the on-disk form.
func authFileBody(phrase string, key []byte) []byte {
	return []byte("# hesplay remote passphrase. Type these words into the phone to sign in.\n" +
		"# Lines starting with # are ignored. Edit the words freely; changing them\n" +
		"# signs every phone out and takes effect within a second, no restart needed.\n" +
		phrase + "\n" +
		"# Session key — delete it to sign every phone out without changing the words.\n" +
		keyLine + " " + base64.RawURLEncoding.EncodeToString(key) + "\n")
}

// readAuthFile parses the passphrase file.
//
// Three outcomes, and keeping them apart is the whole point: nothing usable
// here (generate), unreadable as a file (do not touch it), or a phrase plus
// possibly a key. A key that is missing or malformed is not an error — the
// caller draws a new one, which signs phones out once and is strictly better
// than refusing to serve.
func readAuthFile(path string) (phrase string, key []byte, err error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil, errNoPassphrase
		}
		return "", nil, fmt.Errorf("%w: %v", errPassphraseUnreadable, err)
	}
	defer func() { _ = f.Close() }()

	b, err := io.ReadAll(io.LimitReader(f, maxPassphraseFile))
	if err != nil {
		return "", nil, fmt.Errorf("%w: %v", errPassphraseUnreadable, err)
	}
	raw := string(b)
	if strings.TrimSpace(raw) == "" {
		return "", nil, errNoPassphrase
	}

	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Case-insensitive: a hand-edited "Key: …" would otherwise be read as
		// the passphrase, losing the key AND locking the household out.
		if rest, ok := cutPrefixFold(line, keyLine); ok {
			if k, decErr := base64.RawURLEncoding.DecodeString(strings.TrimSpace(rest)); decErr == nil && len(k) == 32 {
				key = k
			}
			continue
		}
		if phrase == "" {
			phrase = normalizePassphrase(line)
		}
	}
	if phrase == "" {
		return "", key, errPassphraseGarbled
	}
	return phrase, key, nil
}

// cutPrefixFold is strings.CutPrefix with an ASCII case-insensitive prefix.
func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) < len(prefix) || !strings.EqualFold(s[:len(prefix)], prefix) {
		return "", false
	}
	return s[len(prefix):], true
}

// writeAuthFile persists the passphrase and key at 0600, tightening the
// containing directory to 0700 — which mirrors cmd/hespera/main.go, doing it
// for the same reason (the directory holds secrets and no other local user has
// business in it) and, like that code, tightening pre-existing installs since
// MkdirAll never changes an existing directory's mode.
func writeAuthFile(path, phrase string, key []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	_ = os.Chmod(dir, 0o700)
	return writeFileAtomic(path, authFileBody(phrase, key), 0o600)
}

// newMemoryAuth builds an auth around a passphrase that is never re-read from
// disk.
//
// TEST-ONLY: it panics if crypto/rand fails, which is the wrong behaviour for a
// process whose noise reconciler must survive anything. newAuth therefore draws
// its key itself and handles the error, and never calls this.
func newMemoryAuth(phrase string, now func() time.Time) *auth {
	if now == nil {
		now = time.Now
	}
	key, err := newInstanceKey()
	if err != nil {
		panic("hesplay: " + err.Error())
	}
	return newMemoryAuthWithKey(phrase, key, now)
}

// newRefusingAuth returns an auth that can never be satisfied — no passphrase,
// no key, and every login and every cookie refused.
//
// It exists because the alternative was a fail-OPEN. An auth carrying an empty
// passphrase used to accept any input that normalized to nothing ("", "!!!!",
// "12345", "   " all returned 200), because normalizing the submission and
// normalizing the stored phrase both produced "" and the MACs matched. That is
// reachable whenever the credential cannot be established — crypto/rand
// failing, a garbled file that will not move aside — which is precisely when a
// gate must shut rather than open. The remote still serves, so the schedule
// still runs; nobody can sign in until the operator fixes the cause.
func newRefusingAuth(now func() time.Time) *auth {
	if now == nil {
		now = time.Now
	}
	return &auth{buckets: make(map[string]*attemptBucket), now: now}
}

// usableLocked reports whether a credential is actually in force. Caller holds
// a.mu. An empty passphrase is never a credential.
func (a *auth) usableLocked() bool { return a.phrase != "" && len(a.key) > 0 }

// usable reports whether anyone can sign in at all.
//
// newAuth returns a non-nil auth AND a non-nil error in two very different
// situations: "signed in fine, the key could not be persisted" and "no
// credential could be established, the remote is shut". The error text
// distinguishes them for a human; this distinguishes them for the caller, which
// otherwise has to compare phraseForDisplay() against "" and hope.
func (a *auth) usable() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.usableLocked()
}

func newMemoryAuthWithKey(phrase string, key []byte, now func() time.Time) *auth {
	if now == nil {
		now = time.Now
	}
	normalized := normalizePassphrase(phrase)
	return &auth{
		phrase:  normalized,
		key:     key,
		token:   deriveToken(key, normalized),
		buckets: make(map[string]*attemptBucket),
		now:     now,
	}
}

// newAuth loads the passphrase at path, generating one when there is nothing
// usable there.
//
// **The returned *auth is never nil.** Any error is a warning for the caller to
// print, never a reason to stop: the reconciler that plays the household's
// noise at 20:00 lives inside this same process, so refusing to serve over a
// config-directory problem would trade a working schedule for a security
// posture nobody asked for. The remote is gated either way — a degraded start
// means the passphrase is in memory only and will change on the next restart,
// which is exactly what the warning says.
//
// Four cases, and keeping them apart is the point:
//
//	absent or blank      generate and persist
//	usable               load it (drawing a key if the file has none)
//	garbled              move it aside, generate and persist, say where it went
//	unreadable           generate in memory, touch nothing, warn
func newAuth(path string, now func() time.Time) (a *auth, generated bool, warning error) {
	if now == nil {
		now = time.Now
	}
	phrase, key, readErr := readAuthFile(path)

	if readErr == nil {
		if key != nil {
			a = newMemoryAuthWithKey(phrase, key, now)
			a.path, a.checked = path, now()
			warnShortPassphrase(path, phrase)
			return a, false, nil
		}
		// A hand-edited file (echo "three words" > …) drops the key line. Draw
		// a new one and put it back: phones sign in once more, which is the
		// right outcome for a file someone just rewrote by hand.
		key, err := newInstanceKey()
		if err != nil {
			// A phrase with no key cannot derive a token, and newMemoryAuth
			// would re-draw and panic on the very error just caught. Refuse.
			// The FILE is healthy here — only entropy failed — but a refusing
			// auth never re-reads it, so recovery needs a restart. Acceptable:
			// getrandom does not start failing on a box that has booted.
			return newRefusingAuth(now), false, fmt.Errorf(
				"%s: no session key could be drawn (%v) — the remote will refuse every sign-in until it is restarted", path, err)
		}
		a = newMemoryAuthWithKey(phrase, key, now)
		a.path, a.checked = path, now()
		warnShortPassphrase(path, phrase)
		return a, false, writeAuthFile(path, phrase, key)
	}

	if errors.Is(readErr, errPassphraseUnreadable) {
		// Cannot be read and must not be written over — the household may know
		// the passphrase it holds. Serve gated on an ephemeral one and say so.
		eph, err := ephemeralAuth(now)
		if err != nil {
			return newRefusingAuth(now), false, fmt.Errorf("%w; and no temporary passphrase could be drawn: %v", readErr, err)
		}
		// Keep the path. The file is unreadable NOW, and reloadLocked retries
		// once a second — so the moment an operator fixes the permissions or
		// runs `hesplay passphrase set`, the running remote adopts it. Without
		// this the box served an ephemeral passphrase until someone restarted
		// it, and the CLI's "a running remote picks this up within a second"
		// was a promise it could not keep.
		eph.path, eph.checked = path, now()
		return eph, true, fmt.Errorf("%w — serving with a temporary passphrase until the file is readable again", readErr)
	}

	var aside string
	if errors.Is(readErr, errPassphraseGarbled) {
		var err error
		if aside, err = preserveUnusable(path); err != nil {
			eph, gerr := ephemeralAuth(now)
			if gerr != nil {
				return newRefusingAuth(now), false, fmt.Errorf("%w and could not be moved aside: %v", errPassphraseGarbled, err)
			}
			return eph, true, fmt.Errorf("%w and could not be moved aside (%v) — serving with a temporary passphrase", errPassphraseGarbled, err)
		}
	}

	fresh, err := generatePassphrase()
	if err != nil {
		return newRefusingAuth(now), false, err
	}
	key, err = newInstanceKey()
	if err != nil {
		return newRefusingAuth(now), false, err
	}
	a = newMemoryAuthWithKey(fresh, key, now)
	a.path, a.checked = path, now()
	warning = writeAuthFile(path, a.phrase, key)
	if aside != "" {
		note := fmt.Errorf("%w — moved to %s and replaced with a fresh passphrase", errPassphraseGarbled, aside)
		if warning != nil {
			warning = fmt.Errorf("%v; %w", note, warning)
		} else {
			warning = note
		}
	}
	return a, true, warning
}

// ephemeralAuth draws an in-memory-only credential for the paths where the file
// cannot be used. Never panics — that is why newAuth does not call
// newMemoryAuth.
func ephemeralAuth(now func() time.Time) (*auth, error) {
	fresh, err := generatePassphrase()
	if err != nil {
		return nil, err
	}
	key, err := newInstanceKey()
	if err != nil {
		return nil, err
	}
	return newMemoryAuthWithKey(fresh, key, now), nil
}

// warnShortPassphrase reports a loaded passphrase with too few words rather
// than refusing it. It is the operator's box and the operator's file: a short
// passphrase is a choice to be told about, not one to overrule — and overruling
// it would mean either destroying what they wrote or refusing to serve, which
// takes the noise schedule down with it.
func warnShortPassphrase(path, phrase string) {
	if n := len(strings.Fields(phrase)); n < passphraseWordCount {
		fmt.Fprintf(os.Stderr, "warn: %s holds a %d-word passphrase; %d words is the designed strength\n",
			path, n, passphraseWordCount)
	}
}

// preserveUnusable moves an unreadable-but-present passphrase file aside so a
// caller may generate a fresh one without destroying what was there. Returns
// the path it was moved to.
func preserveUnusable(path string) (string, error) {
	aside := path + ".unusable"
	if err := os.Rename(path, aside); err != nil {
		return "", err
	}
	return aside, nil
}

// reloadLocked re-reads the passphrase file when its content has changed, at
// most once per reloadInterval. Caller holds a.mu.
//
// A file that has gone missing or unreadable leaves the in-memory secret in
// place: neither failing open (the gate still refuses everyone without the
// current passphrase) nor regenerating behind the operator's back. Deleting the
// file is not a way in.
func (a *auth) reloadLocked() {
	if a.path == "" {
		return
	}
	now := a.now()
	if !a.checked.IsZero() && now.Sub(a.checked) < reloadInterval {
		return
	}
	a.checked = now

	phrase, key, err := readAuthFile(a.path)
	if err != nil {
		// Keep the live secret — neither failing open nor regenerating behind
		// the operator's back. But say so once: startup moves a garbled file
		// aside and announces it, and a reload that stayed silent about the
		// same condition meant editing the file to something unusable looked
		// like it had worked until the next restart signed everyone out.
		a.warnOnceLocked("reload-err:"+err.Error(),
			fmt.Sprintf("warn: %s could not be re-read (%v) — keeping the passphrase already in force", a.path, err))
		return
	}
	// A successful read re-arms every condition warning, whether or not the
	// content changed. Clearing the latch only on a change meant: garble the
	// file (warns), restore it to what is already in memory (no change, latch
	// kept), garble it again — silent forever.
	a.warnedState = ""
	if key == nil {
		key = a.key // hand-edited; keep signing with what we have
	}
	if phrase == a.phrase && bytes.Equal(key, a.key) {
		return
	}
	a.phrase, a.key = phrase, key
	a.token = deriveToken(key, phrase)
	a.reloadCount.Add(1)
	fmt.Fprintln(os.Stderr, "warn: passphrase reloaded from disk — every phone must sign in again")
	// Not latched, and it does not need to be: this line is only reachable when
	// the content actually changed, and the no-change return above is what stops
	// an unchanged short passphrase warning once a second.
	if n := len(strings.Fields(phrase)); n < passphraseWordCount {
		fmt.Fprintf(os.Stderr, "warn: %s now holds a %d-word passphrase; %d words is the designed strength\n",
			a.path, n, passphraseWordCount)
	}
}

// warnOnceLocked prints msg only when the condition differs from the last one
// reported. reloadLocked runs once a second; without this a garbled file would
// emit a line per second for as long as it stayed garbled.
func (a *auth) warnOnceLocked(state, msg string) {
	if a.warnedState == state {
		return
	}
	a.warnedState = state
	fmt.Fprintln(os.Stderr, msg)
}

// validToken reports whether a cookie value is the current session token.
//
// The token is a fixed-length base64 MAC, so its length is public and a
// length-mismatch short-circuit leaks nothing. The passphrase compare in login
// is the one that has to hide a length, and it does.
func (a *auth) validToken(tok string) bool {
	a.mu.Lock()
	a.reloadLocked()
	usable, want := a.usableLocked(), a.token
	a.mu.Unlock()

	// An auth with no credential in force refuses everything. Without this, an
	// empty stored phrase and an empty submitted token both derive to the same
	// MAC and every request is admitted — see newRefusingAuth. The predicate is
	// usableLocked(), matching login: a non-empty phrase with a NIL KEY derives
	// its token under an empty HMAC key, which anyone knowing the words can
	// compute offline, so "want != \"\"" alone would be the weaker test.
	if !usable || want == "" {
		a.deniedCount.Add(1)
		return false
	}
	if hmac.Equal([]byte(tok), []byte(want)) {
		a.okCount.Add(1)
		return true
	}
	a.deniedCount.Add(1)
	return false
}

// allowAttemptLocked takes a token from ip's bucket. Caller holds a.mu.
func (a *auth) allowAttemptLocked(ip string) bool {
	now := a.now()
	b, ok := a.buckets[ip]
	if !ok {
		if len(a.buckets) >= attemptIPCap {
			a.evictOldestLocked()
		}
		b = &attemptBucket{tokens: attemptBurst, last: now}
		a.buckets[ip] = b
	} else {
		// Clamp: a clock stepping backwards (NTP, a test's injected clock)
		// would otherwise leave a large negative debt and lock the owner out
		// for its whole duration. time.Now's monotonic reading saves the
		// production path, but relying on that would make the guarantee an
		// accident of which clock is installed.
		if elapsed := now.Sub(b.last); elapsed > 0 {
			b.tokens += elapsed.Seconds() / attemptRefill.Seconds()
			if b.tokens > attemptBurst {
				b.tokens = attemptBurst
			}
		}
		// b.last advances unconditionally. Advancing it only on forward motion
		// froze the bucket after a backward step: `last` stayed in the future,
		// every later elapsed was negative, and the owner was refused for the
		// whole duration of the step.
		b.last = now
	}
	if b.tokens < 1 {
		if !b.warned {
			b.warned = true
			fmt.Fprintf(os.Stderr, "warn: too many failed sign-ins from %s — throttling\n", ip)
		}
		return false
	}
	b.warned = false
	b.tokens--
	return true
}

// evictOldestLocked drops the least recently used bucket. An O(n) scan over a
// map capped at attemptIPCap, run only on overflow — an ordered structure would
// be more machinery than 256 entries earn.
//
// `first` rather than an empty-string sentinel: an empty IP is a legitimate map
// key (an unparsed RemoteAddr), and keying the scan on `oldestIP == ""` made
// every later entry overwrite the candidate regardless of age.
func (a *auth) evictOldestLocked() {
	var oldestIP string
	var oldest time.Time
	first := true
	for ip, b := range a.buckets {
		if first || b.last.Before(oldest) {
			oldestIP, oldest, first = ip, b.last, false
		}
	}
	if !first {
		delete(a.buckets, oldestIP)
	}
}

// login checks a submitted passphrase and returns the session token to set.
//
// Returns (token, http status). The rate limit is charged BEFORE any comparison
// so a throttled attempt learns nothing at all, not even a timing difference.
func (a *auth) login(ip, submitted string) (string, int) {
	a.mu.Lock()
	if !a.allowAttemptLocked(ip) {
		a.mu.Unlock()
		a.throttledCount.Add(1)
		return "", http.StatusTooManyRequests
	}
	a.reloadLocked()
	usable := a.usableLocked()
	key, token := a.key, a.token
	a.mu.Unlock()

	// No credential in force: refuse, whatever was typed. This is the fail-open
	// guard, and it is checked before the compare rather than relying on the
	// compare to disagree — an empty phrase makes the compare AGREE.
	if !usable {
		a.failureCount.Add(1)
		return "", http.StatusUnauthorized
	}
	if len(submitted) > maxPassphraseInput {
		a.failureCount.Add(1)
		return "", http.StatusUnauthorized
	}
	// Compare fixed-length MACs rather than the strings themselves: a direct
	// compare returns on a length mismatch, which would leak the passphrase's
	// byte length across enough attempts.
	if !hmac.Equal([]byte(deriveToken(key, normalizePassphrase(submitted))), []byte(token)) {
		a.failureCount.Add(1)
		return "", http.StatusUnauthorized
	}
	a.loginCount.Add(1)
	return token, http.StatusOK
}

// phraseForDisplay returns the passphrase, for `hesplay passphrase` and for the
// signed-in Settings panel.
func (a *auth) phraseForDisplay() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reloadLocked()
	return a.phrase
}
