package main

import (
	"bytes"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- the wordlist itself -----------------------------------------------------

// TestWordlistShape pins the properties the passphrase's security and its
// display both rest on. A regeneration that reintroduces the hyphenated
// entries, or that silently loses half the list, fails here rather than in a
// passphrase that reads as four words or carries 20 bits.
func TestWordlistShape(t *testing.T) {
	if len(passphraseWords) != 7772 {
		t.Fatalf("wordlist has %d words, want 7772 (EFF long minus its 4 hyphenated entries)", len(passphraseWords))
	}
	seen := make(map[string]bool, len(passphraseWords))
	for _, w := range passphraseWords {
		if seen[w] {
			t.Fatalf("duplicate word %q — duplicates silently reduce entropy", w)
		}
		seen[w] = true
		if w == "" {
			t.Fatal("empty word in the list")
		}
		for _, r := range w {
			if r < 'a' || r > 'z' {
				t.Fatalf("word %q contains %q; every word must be pure a-z or normalizePassphrase will split it", w, r)
			}
		}
	}
}

// --- normalization -----------------------------------------------------------

func TestNormalizePassphrase(t *testing.T) {
	const want = "correct horse battery"
	cases := []struct {
		name, in, want string
	}{
		{"already canonical", "correct horse battery", want},
		{"upper case", "CORRECT HORSE BATTERY", want},
		{"phone autocapitalize", "Correct Horse Battery", want},
		{"hyphens", "correct-horse-battery", want},
		{"underscores", "correct_horse_battery", want},
		{"trailing space", "correct horse battery ", want},
		{"leading space", "  correct horse battery", want},
		{"runs of space", "correct   horse\t\nbattery", want},
		{"smart punctuation", "correct—horse’battery", "correct horse battery"},
		{"digits separate", "correct1horse2battery", want},
		{"empty", "", ""},
		{"only separators", " -_- ", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := normalizePassphrase(c.in); got != c.want {
				t.Fatalf("normalizePassphrase(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestTokenIsNotThePassphrase is the whole point of deriving rather than
// storing: a leaked cookie must not hand back the words.
var testKey = []byte("0123456789abcdef0123456789abcdef")

func TestTokenIsNotThePassphrase(t *testing.T) {
	const phrase = "correct horse battery"
	tok := deriveToken(testKey, phrase)
	if tok == "" {
		t.Fatal("empty token")
	}
	if strings.Contains(tok, "correct") || strings.Contains(tok, "horse") || strings.Contains(tok, "battery") {
		t.Fatalf("token %q leaks the passphrase", tok)
	}
	if got := deriveToken(testKey, phrase); got != tok {
		t.Fatal("token is not deterministic — a restart would sign everyone out")
	}
	if deriveToken(testKey, "correct horse batteryy") == tok {
		t.Fatal("distinct passphrases derive the same token")
	}
}

func TestGeneratePassphrase(t *testing.T) {
	inList := make(map[string]bool, len(passphraseWords))
	for _, w := range passphraseWords {
		inList[w] = true
	}
	distinct := make(map[string]bool)
	for i := 0; i < 50; i++ {
		p, err := generatePassphrase()
		if err != nil {
			t.Fatalf("generatePassphrase: %v", err)
		}
		words := strings.Fields(p)
		if len(words) != passphraseWordCount {
			t.Fatalf("generated %q = %d words, want %d", p, len(words), passphraseWordCount)
		}
		for _, w := range words {
			if !inList[w] {
				t.Fatalf("generated word %q is not in the wordlist", w)
			}
		}
		if normalizePassphrase(p) != p {
			t.Fatalf("generated %q does not survive its own normalizer", p)
		}
		distinct[p] = true
	}
	// Probe the randomness non-zero: a constant generator would pass every
	// assertion above.
	if len(distinct) < 40 {
		t.Fatalf("only %d distinct passphrases in 50 draws — the draw looks stuck", len(distinct))
	}
}

// --- the file ----------------------------------------------------------------

func TestNewAuthGeneratesAndPersists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg", "hesplay-passphrase")

	a, generated, err := newAuth(path, nil)
	if err != nil {
		t.Fatalf("newAuth: %v", err)
	}
	if !generated {
		t.Fatal("want generated=true on a fresh directory")
	}
	if len(strings.Fields(a.phraseForDisplay())) != passphraseWordCount {
		t.Fatalf("generated passphrase %q is not %d words", a.phraseForDisplay(), passphraseWordCount)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("passphrase file not written: %v", err)
	}
	if mode := fi.Mode().Perm(); mode != 0o600 {
		t.Fatalf("passphrase file mode %o, want 600", mode)
	}
	di, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if mode := di.Mode().Perm(); mode != 0o700 {
		t.Fatalf("config dir mode %o, want 700", mode)
	}

	// A second construction must LOAD, never regenerate — otherwise every
	// restart would sign the household out.
	b, generated, err := newAuth(path, nil)
	if err != nil {
		t.Fatalf("second newAuth: %v", err)
	}
	if generated {
		t.Fatal("want generated=false when the file already exists")
	}
	if b.phraseForDisplay() != a.phraseForDisplay() {
		t.Fatal("reload produced a different passphrase")
	}
	if b.token != a.token {
		t.Fatal("token did not survive a reconstruction — a restart would sign every phone out")
	}
}

// TestNewAuthSelfHealsUnusableFile: an empty or truncated file must not wedge
// the remote shut.
func TestNewAuthSelfHealsUnusableFile(t *testing.T) {
	// "---" is deliberately NOT here: it is garbled, not blank, and is
	// preserved by TestGarbledFileIsPreservedNotDestroyed rather than replaced.
	for _, content := range []string{"", "\n", "   \n"} {
		dir := t.TempDir()
		path := filepath.Join(dir, "hesplay-passphrase")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		a, generated, err := newAuth(path, nil)
		if err != nil {
			t.Fatalf("newAuth over %q: %v", content, err)
		}
		if !generated {
			t.Fatalf("content %q should have been treated as unusable", content)
		}
		if len(strings.Fields(a.phraseForDisplay())) != passphraseWordCount {
			t.Fatalf("did not regenerate a usable passphrase over %q", content)
		}
	}
}

// TestNewAuthSurvivesUnwritableDir pins the decision that a config directory
// problem must not take the noise schedule down with it: newAuth returns a
// usable, gated auth plus a non-fatal error for the caller to warn about.
func TestNewAuthSurvivesUnwritableDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	locked := filepath.Join(dir, "locked")
	if err := os.Mkdir(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

	a, generated, persistErr := newAuth(filepath.Join(locked, "sub", "hesplay-passphrase"), nil)
	if persistErr == nil {
		t.Fatal("want a persist error on an unwritable directory")
	}
	if a == nil || !generated {
		t.Fatal("want a usable in-memory auth despite the persist failure")
	}
	if len(strings.Fields(a.phraseForDisplay())) != passphraseWordCount {
		t.Fatal("in-memory fallback carries no usable passphrase")
	}
	if _, status := a.login("10.0.0.1", a.phraseForDisplay()); status != 200 {
		t.Fatalf("in-memory auth refused its own passphrase: status %d", status)
	}
}

// --- reload ------------------------------------------------------------------

// TestRotateTakesEffectWithoutRestart is the reason reloadLocked exists: the
// only reason to rotate is believing you are compromised, so "it applies at the
// next restart" is the wrong answer.
func TestRotateTakesEffectWithoutRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hesplay-passphrase")
	clock := time.Unix(1000, 0)
	now := func() time.Time { return clock }

	a, _, err := newAuth(path, now)
	if err != nil {
		t.Fatal(err)
	}
	oldToken := a.token

	// Stimulus first: the old token works before the rotate. Without this the
	// assertion below would also pass against an auth that never worked.
	if !a.validToken(oldToken) {
		t.Fatal("the freshly generated token was rejected")
	}

	if err := writeAuthFile(path, "correct horse battery", a.key); err != nil {
		t.Fatal(err)
	}
	// Force the mtime to differ: a same-second write can otherwise collide.
	future := clock.Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(reloadInterval + time.Millisecond)

	if a.validToken(oldToken) {
		t.Fatal("the old cookie still works after a rotate — rotation does not revoke")
	}
	if got := a.phraseForDisplay(); got != "correct horse battery" {
		t.Fatalf("phrase after rotate = %q", got)
	}
	if !a.validToken(deriveToken(a.key, "correct horse battery")) {
		t.Fatal("the new token was rejected")
	}
	if a.reloadCount.Load() == 0 {
		t.Fatal("reload counter never moved — the metric cannot report a reload that happened")
	}
}

// TestReloadIsRateLimited: the stat must not run per request.
func TestReloadIsRateLimited(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hesplay-passphrase")
	clock := time.Unix(1000, 0)
	a, _, err := newAuth(path, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	if err := writeAuthFile(path, "correct horse battery", a.key); err != nil {
		t.Fatal(err)
	}
	future := clock.Add(time.Hour)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
	// The clock has not advanced, so no reload may happen yet.
	if got := a.phraseForDisplay(); got == "correct horse battery" {
		t.Fatal("reloaded inside the interval — the stat is running per request")
	}
	clock = clock.Add(reloadInterval + time.Millisecond)
	if got := a.phraseForDisplay(); got != "correct horse battery" {
		t.Fatalf("did not reload after the interval: %q", got)
	}
}

// TestDeletingTheFileIsNotAWayIn: a missing file leaves the live secret alone.
func TestDeletingTheFileIsNotAWayIn(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hesplay-passphrase")
	clock := time.Unix(1000, 0)
	a, _, err := newAuth(path, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	tok := a.token
	if !a.validToken(tok) {
		t.Fatal("token rejected before the delete")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Hour)
	if !a.validToken(tok) {
		t.Fatal("deleting the file changed the secret")
	}
	if a.validToken("") || a.validToken("anything") {
		t.Fatal("deleting the file opened the gate")
	}
}

// --- login and the limiter ---------------------------------------------------

func TestLogin(t *testing.T) {
	cases := []struct {
		name, submitted string
		want            int
	}{
		{"exact", "correct horse battery", 200},
		{"upper case", "Correct Horse Battery", 200},
		{"hyphenated", "correct-horse-battery", 200},
		{"extra whitespace", "  correct   horse battery ", 200},
		{"wrong", "correct horse batteries", 401},
		{"empty", "", 401},
		{"two of three", "correct horse", 401},
		{"prefix", "correct horse batter", 401},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := newMemoryAuth("correct horse battery", nil)
			tok, status := a.login("10.0.0.1", c.submitted)
			if status != c.want {
				t.Fatalf("login(%q) = %d, want %d", c.submitted, status, c.want)
			}
			if c.want == 200 {
				if !a.validToken(tok) {
					t.Fatal("login returned a token its own gate rejects")
				}
			} else if tok != "" {
				t.Fatalf("a failed login returned a token %q", tok)
			}
		})
	}
}

// TestLimiterChargesBeforeComparing is the security property, not a convenience:
// a throttled attempt must learn nothing, so even the CORRECT passphrase is
// refused with 429 once the bucket is empty.
func TestLimiterChargesBeforeComparing(t *testing.T) {
	const phrase = "correct horse battery"
	clock := time.Unix(1000, 0)
	a := newMemoryAuth(phrase, func() time.Time { return clock })

	// Stimulus: the correct passphrase works before the bucket is drained.
	if _, status := a.login("10.0.0.1", phrase); status != 200 {
		t.Fatalf("correct passphrase refused with a full bucket: %d", status)
	}
	for i := 0; i < attemptBurst-1; i++ {
		if _, status := a.login("10.0.0.1", "wrong wrong wrong"); status != 401 {
			t.Fatalf("attempt %d: status %d, want 401", i, status)
		}
	}
	if _, status := a.login("10.0.0.1", phrase); status != 429 {
		t.Fatalf("want 429 once the bucket is empty even for the right passphrase, got %d", status)
	}
	if a.throttledCount.Load() == 0 {
		t.Fatal("throttled counter never moved")
	}
	before := a.failureCount.Load()
	_, _ = a.login("10.0.0.1", "wrong wrong wrong")
	if a.failureCount.Load() != before {
		t.Fatal("a throttled attempt was compared — the failure counter moved")
	}

	// It refills.
	clock = clock.Add(attemptRefill + time.Millisecond)
	if _, status := a.login("10.0.0.1", phrase); status != 200 {
		t.Fatalf("bucket did not refill: %d", status)
	}
}

// TestLimiterIsPerIP: one attacker must not lock the owner out of his speakers.
func TestLimiterIsPerIP(t *testing.T) {
	const phrase = "correct horse battery"
	clock := time.Unix(1000, 0)
	a := newMemoryAuth(phrase, func() time.Time { return clock })

	for i := 0; i < attemptBurst+4; i++ {
		_, _ = a.login("10.0.0.99", "wrong wrong wrong")
	}
	if _, status := a.login("10.0.0.99", phrase); status != 429 {
		t.Fatalf("attacker not throttled: %d", status)
	}
	if _, status := a.login("10.0.0.1", phrase); status != 200 {
		t.Fatalf("the owner was locked out by another address: %d", status)
	}
}

// TestLimiterMapIsBounded: the deleted internal/auth grew this map forever.
func TestLimiterMapIsBounded(t *testing.T) {
	a := newMemoryAuth("correct horse battery", nil)
	for i := 0; i < attemptIPCap+64; i++ {
		_, _ = a.login(testIP(i), "wrong wrong wrong")
	}
	a.mu.Lock()
	n := len(a.buckets)
	a.mu.Unlock()
	if n > attemptIPCap {
		t.Fatalf("bucket map holds %d entries, cap is %d", n, attemptIPCap)
	}
}

// testIP builds distinct source addresses for the limiter tests.
func testIP(i int) string {
	return "10.1." + strconv.Itoa(i/256) + "." + strconv.Itoa(i%256)
}

// TestAuthIsRaceFree exercises the paths a live remote runs concurrently.
func TestAuthIsRaceFree(t *testing.T) {
	dir := t.TempDir()
	a, _, err := newAuth(filepath.Join(dir, "hesplay-passphrase"), nil)
	if err != nil {
		t.Fatal(err)
	}
	tok := a.token
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				a.validToken(tok)
				a.validToken("not-the-token")
				_, _ = a.login(testIP(i), "wrong wrong wrong")
				_ = a.phraseForDisplay()
				_ = a.snapshot()
			}
		}(i)
	}
	wg.Wait()
	if a.okCount.Load() == 0 || a.deniedCount.Load() == 0 {
		t.Fatal("counters did not move under concurrency")
	}
}

// TestPassphrasePathIsItsOwnFile pins that the secret does not live in
// hesplay.json, which PUT /api/noise rewrites wholesale from request JSON.
func TestPassphrasePathIsItsOwnFile(t *testing.T) {
	// The SEAM, not XDG_CONFIG_HOME: os.UserConfigDir() ignores that variable on
	// darwin and windows, and hesplay ships binaries for both — an env-based
	// isolation here would reach the developer's real config directory when the
	// suite runs on a Mac. (passphrase_test.go's isolate() is the pattern.)
	dir := t.TempDir()
	prev := passphrasePath
	passphrasePath = func() (string, error) { return filepath.Join(dir, "hespera", "hesplay-passphrase"), nil }
	t.Cleanup(func() { passphrasePath = prev })
	p, err := passphrasePath()
	if err != nil {
		t.Fatal(err)
	}
	n, err := noiseConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if p == n {
		t.Fatal("the passphrase must not share a file with the remotely-writable noise config")
	}
	if filepath.Base(p) != "hesplay-passphrase" {
		t.Fatalf("passphrase path = %q", p)
	}
}

// --- the findings the slice's code review turned up --------------------------

// TestCookieDoesNotYieldThePassphrase is the property the HMAC key exists for.
// With a bare SHA-256 the cookie is reversible offline in ~22s on one GPU
// against a 38.77-bit secret, which made the header's own security claim false.
// Two instances holding the SAME passphrase must mint different cookies, and a
// cookie must not be derivable without the key.
func TestCookieDoesNotYieldThePassphrase(t *testing.T) {
	const phrase = "correct horse battery"
	a, b := newMemoryAuth(phrase, nil), newMemoryAuth(phrase, nil)
	if a.token == b.token {
		t.Fatal("two instances with the same passphrase mint the same cookie — the key is not in the derivation")
	}
	if a.validToken(b.token) || b.validToken(a.token) {
		t.Fatal("one instance accepts another's cookie")
	}
	if deriveToken(testKey, phrase) == a.token {
		t.Fatal("the token is derivable from the passphrase alone")
	}
}

// TestGarbledFileIsPreservedNotDestroyed: a passphrase written in a non-Latin
// script normalizes to nothing. Regenerating over it silently would destroy
// what the operator deliberately wrote.
func TestGarbledFileIsPreservedNotDestroyed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hesplay-passphrase")
	const garbled = "Пароль Секрет Ключ\n"
	if err := os.WriteFile(path, []byte(garbled), 0o600); err != nil {
		t.Fatal(err)
	}
	a, generated, warning := newAuth(path, nil)
	if a == nil {
		t.Fatal("newAuth returned nil — the contract is never-nil so the noise schedule survives")
	}
	if !generated {
		t.Fatal("want a freshly generated passphrase")
	}
	if warning == nil || !errors.Is(warning, errPassphraseGarbled) {
		t.Fatalf("want an errPassphraseGarbled warning, got %v", warning)
	}
	kept, err := os.ReadFile(path + ".unusable")
	if err != nil {
		t.Fatalf("the operator's file was not preserved: %v", err)
	}
	if string(kept) != garbled {
		t.Fatalf("preserved file = %q, want %q", kept, garbled)
	}
	if len(strings.Fields(a.phraseForDisplay())) != passphraseWordCount {
		t.Fatal("no usable passphrase after the move-aside")
	}
}

// TestUnreadableFileStillServes: a file that cannot be read must not be written
// over (the household may know the passphrase in it) and must not stop the
// process (D5 — the noise reconciler lives here).
func TestUnreadableFileStillServes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hesplay-passphrase")
	if err := os.Mkdir(path, 0o700); err != nil { // a directory where a file belongs
		t.Fatal(err)
	}
	a, _, warning := newAuth(path, nil)
	if a == nil {
		t.Fatal("newAuth returned nil on an unreadable file")
	}
	if warning == nil || !errors.Is(warning, errPassphraseUnreadable) {
		t.Fatalf("want an errPassphraseUnreadable warning, got %v", warning)
	}
	if len(strings.Fields(a.phraseForDisplay())) != passphraseWordCount {
		t.Fatal("no usable in-memory passphrase")
	}
	if fi, err := os.Stat(path); err != nil || !fi.IsDir() {
		t.Fatal("the unreadable path was written over")
	}
}

// TestHandEditedFileGetsAKey: `echo "three words" > file` drops the key line.
func TestHandEditedFileGetsAKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hesplay-passphrase")
	if err := os.WriteFile(path, []byte("correct horse battery\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a, generated, warning := newAuth(path, nil)
	if warning != nil {
		t.Fatalf("unexpected warning: %v", warning)
	}
	if generated {
		t.Fatal("the operator's passphrase should have been kept, not regenerated")
	}
	if got := a.phraseForDisplay(); got != "correct horse battery" {
		t.Fatalf("phrase = %q", got)
	}
	_, key, err := readAuthFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(key) != 32 {
		t.Fatalf("no key was written back: %v", key)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := fi.Mode().Perm(); mode != 0o600 {
		t.Fatalf("rewritten file mode %o, want 600", mode)
	}
}

// TestShortPassphraseLoads: it is the operator's box. A two-word file is warned
// about, not overruled — overruling means destroying it or refusing to serve.
func TestShortPassphraseLoads(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hesplay-passphrase")
	if err := os.WriteFile(path, []byte("hunter\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var a *auth
	var generated bool
	stderr := captureStderr(t, func() { a, generated, _ = newAuth(path, nil) })
	if generated {
		t.Fatal("a short passphrase was silently replaced")
	}
	if !strings.Contains(stderr, "1-word passphrase") {
		t.Fatalf("no warning was printed for a short passphrase; stderr = %q", stderr)
	}
	if got := a.phraseForDisplay(); got != "hunter" {
		t.Fatalf("phrase = %q, want the operator's", got)
	}
	if _, status := a.login("10.0.0.1", "hunter"); status != 200 {
		t.Fatalf("the operator's own passphrase was refused: %d", status)
	}
}

// TestReloadDetectsSameSizeSameMtimeRotation is why there is no mtime/size
// cache: `rsync -a` / `cp -p` / `tar -xp` reproduce both, so a metadata
// comparison would miss a restored file permanently.
func TestReloadDetectsSameSizeSameMtimeRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hesplay-passphrase")
	clock := time.Unix(1000, 0)
	a, _, err := newAuth(path, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	if err := writeAuthFile(path, "alpha bravo charlie", a.key); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(reloadInterval + time.Millisecond)
	if got := a.phraseForDisplay(); got != "alpha bravo charlie" {
		t.Fatalf("first rotation missed: %q", got)
	}

	// Same byte length, and the mtime forced back to what it was.
	if err := writeAuthFile(path, "delta echo foxtrott", a.key); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() != before.Size() {
		t.Fatalf("test setup: sizes differ (%d vs %d) so this would pass on size alone", after.Size(), before.Size())
	}
	if err := os.Chtimes(path, before.ModTime(), before.ModTime()); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(reloadInterval + time.Millisecond)
	if got := a.phraseForDisplay(); got != "delta echo foxtrott" {
		t.Fatalf("a same-size, same-mtime rotation was missed: %q", got)
	}
}

// TestLimiterSurvivesBackwardClock: an NTP step backwards must not leave a
// negative token debt that locks the owner out for its whole duration.
func TestLimiterSurvivesBackwardClock(t *testing.T) {
	const phrase = "correct horse battery"
	clock := time.Unix(100000, 0)
	a := newMemoryAuth(phrase, func() time.Time { return clock })
	if _, status := a.login("10.0.0.1", phrase); status != 200 {
		t.Fatalf("setup: %d", status)
	}
	clock = clock.Add(-time.Hour)
	if _, status := a.login("10.0.0.1", phrase); status != 200 {
		t.Fatalf("a backward clock step locked the owner out: %d", status)
	}
	a.mu.Lock()
	tokens := a.buckets["10.0.0.1"].tokens
	a.mu.Unlock()
	if tokens < 0 {
		t.Fatalf("negative token debt %v", tokens)
	}
}

// TestEvictOldestPicksTheOldest: the previous implementation used an
// `oldestIP == ""` sentinel, so a bucket keyed by an empty IP (an unparsed
// RemoteAddr) survived eviction while every later entry overwrote the
// candidate regardless of age. The empty key is the case that exposed it.
func TestEvictOldestPicksTheOldest(t *testing.T) {
	a := newMemoryAuth("correct horse battery", nil)
	base := time.Unix(1000, 0)
	a.buckets[""] = &attemptBucket{tokens: 1, last: base} // oldest
	a.buckets["10.0.0.2"] = &attemptBucket{tokens: 1, last: base.Add(time.Minute)}
	a.buckets["10.0.0.3"] = &attemptBucket{tokens: 1, last: base.Add(2 * time.Minute)}

	a.mu.Lock()
	a.evictOldestLocked()
	a.mu.Unlock()

	if _, still := a.buckets[""]; still {
		t.Fatal("the oldest bucket survived eviction")
	}
	if len(a.buckets) != 2 {
		t.Fatalf("evicted %d buckets, want 1", 3-len(a.buckets))
	}
}

func TestWriteFileAtomicSetsMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f")
	if err := writeFileAtomic(path, []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := fi.Mode().Perm(); mode != 0o600 {
		t.Fatalf("mode %o, want 600", mode)
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "hello\n" {
		t.Fatalf("content %q err %v", b, err)
	}
	// No temp files left behind.
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 1 {
		t.Fatalf("%d entries left in the directory, want 1", len(ents))
	}
}

// --- regression pins for the second review round -----------------------------

// captureStderr runs fn with os.Stderr redirected and returns what it wrote.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		var b strings.Builder
		_, _ = io.Copy(&b, r)
		done <- b.String()
	}()
	// Deferred: a t.Fatal inside fn unwinds via runtime.Goexit, which would
	// otherwise leave os.Stderr pointing at the pipe for every later test in
	// the package and leak the copier goroutine.
	restored := false
	restore := func() {
		if !restored {
			restored = true
			os.Stderr = orig
			_ = w.Close()
		}
	}
	defer restore()
	fn()
	restore()
	out := <-done
	_ = r.Close()
	return out
}

// TestEmptyPassphraseRefusesEverything pins the fail-OPEN the second review
// round found: an auth carrying an empty passphrase accepted any input that
// normalized to nothing, because both sides derived the same MAC. Reachable
// whenever the credential could not be established, which is exactly when the
// gate must shut.
func TestEmptyPassphraseRefusesEverything(t *testing.T) {
	for _, a := range map[string]*auth{
		"empty phrase": newMemoryAuth("", nil),
		"refusing":     newRefusingAuth(nil),
	} {
		for _, in := range []string{"", "!!!!", "12345", "   ", "correct horse battery"} {
			if _, status := a.login("10.0.0.1", in); status == http.StatusOK {
				t.Fatalf("FAIL-OPEN: login(%q) returned 200 with no credential in force", in)
			}
		}
		if a.validToken("") || a.validToken(deriveToken(nil, "")) {
			t.Fatal("FAIL-OPEN: a cookie was accepted with no credential in force")
		}
		// validToken must use the same predicate as login. Its own token is the
		// sharpest probe: a weaker "want != \"\"" test would admit it.
		if a.validToken(a.token) {
			t.Fatal("FAIL-OPEN: an auth with no credential accepted its own derived token")
		}
	}
}

// TestLimiterUnfreezesAfterBackwardClock: clamping the refill was not enough —
// `b.last` also has to advance, or it stays in the future, every later elapsed
// is negative, and the bucket never refills again.
func TestLimiterUnfreezesAfterBackwardClock(t *testing.T) {
	const phrase = "correct horse battery"
	clock := time.Unix(100000, 0)
	a := newMemoryAuth(phrase, func() time.Time { return clock })

	for i := 0; i < attemptBurst; i++ {
		_, _ = a.login("10.0.0.1", "wrong wrong wrong")
	}
	if _, status := a.login("10.0.0.1", phrase); status != http.StatusTooManyRequests {
		t.Fatalf("setup: bucket not drained (%d)", status)
	}
	clock = clock.Add(-time.Hour) // NTP steps backwards
	_, _ = a.login("10.0.0.1", "wrong wrong wrong")
	clock = clock.Add(10 * attemptRefill) // and time moves on
	if _, status := a.login("10.0.0.1", phrase); status != http.StatusOK {
		t.Fatalf("the bucket never refilled after a backward clock step: %d", status)
	}
}

// TestCommentLinesAreNotThePassphrase: the file hesplay writes is
// self-documenting, and a comment line must not be mistaken for the words.
func TestCommentLinesAreNotThePassphrase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hesplay-passphrase")
	body := "# hesplay remote passphrase — type these words in\n" +
		"correct horse battery\n" +
		"key: " + strings.TrimRight(encodeTestKey(), "=") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	phrase, key, err := readAuthFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if phrase != "correct horse battery" {
		t.Fatalf("phrase = %q — a comment line was read as the passphrase", phrase)
	}
	if len(key) != 32 {
		t.Fatalf("key not parsed: %v", key)
	}
}

// TestKeyPrefixIsCaseInsensitive: a hand-edited "Key:" would otherwise be read
// as the passphrase — losing the key AND locking the household out.
func TestKeyPrefixIsCaseInsensitive(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hesplay-passphrase")
	body := "correct horse battery\nKey: " + strings.TrimRight(encodeTestKey(), "=") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	phrase, key, err := readAuthFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if phrase != "correct horse battery" {
		t.Fatalf("phrase = %q", phrase)
	}
	if len(key) != 32 {
		t.Fatal("an upper-case Key: line was not recognised")
	}
}

// TestRoundTripsItsOwnFile: what writeAuthFile emits, readAuthFile must read
// back — comments, key line and all.
func TestRoundTripsItsOwnFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hesplay-passphrase")
	if err := writeAuthFile(path, "correct horse battery", testKey); err != nil {
		t.Fatal(err)
	}
	phrase, key, err := readAuthFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if phrase != "correct horse battery" || !bytes.Equal(key, testKey) {
		t.Fatalf("round trip lost data: phrase=%q key=%v", phrase, key)
	}
}

// TestReloadAnnouncesAnUnusableFile: startup moves a garbled file aside and says
// so; a reload that stayed silent made editing the file to something unusable
// look like it had worked, until the next restart signed everyone out.
func TestReloadAnnouncesAnUnusableFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hesplay-passphrase")
	clock := time.Unix(1000, 0)
	a, _, err := newAuth(path, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	live := a.phraseForDisplay()

	if err := os.WriteFile(path, []byte("Пароль Секрет Ключ\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(reloadInterval + time.Millisecond)
	out := captureStderr(t, func() { _ = a.phraseForDisplay() })
	if !strings.Contains(out, "could not be re-read") {
		t.Fatalf("a reload over an unusable file was silent; stderr = %q", out)
	}
	if a.phraseForDisplay() != live {
		t.Fatal("an unusable file changed the live passphrase")
	}

	// And it says it once, not once per second.
	clock = clock.Add(10 * reloadInterval)
	if again := captureStderr(t, func() { _ = a.phraseForDisplay() }); strings.Contains(again, "could not be re-read") {
		t.Fatalf("the warning repeats every reload; stderr = %q", again)
	}
}

func TestWriteFileAtomicReportsErrors(t *testing.T) {
	if err := writeFileAtomic(filepath.Join(t.TempDir(), "no", "such", "dir", "f"), []byte("x"), 0o600); err == nil {
		t.Fatal("want an error writing into a nonexistent directory")
	}
}

func encodeTestKey() string { return base64.RawURLEncoding.EncodeToString(testKey) }

// TestReloadWarningReArms pins the half of the latch that shipped broken:
// warnedState was cleared only by a CONTENT change, so garble → warn → restore
// the file to what was already in memory (no change, latch kept) → garble again
// left the operator with no signal at all. The latch must suppress a REPEAT,
// never a RECURRENCE.
func TestReloadWarningReArms(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hesplay-passphrase")
	clock := time.Unix(1000, 0)
	a, _, err := newAuth(path, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	good, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	tick := func() string {
		clock = clock.Add(2 * reloadInterval)
		return captureStderr(t, func() { _ = a.phraseForDisplay() })
	}
	garble := func() {
		if err := os.WriteFile(path, []byte("Пароль Секрет\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	garble()
	if out := tick(); !strings.Contains(out, "could not be re-read") {
		t.Fatalf("first fault was silent: %q", out)
	}
	if out := tick(); strings.Contains(out, "could not be re-read") {
		t.Fatalf("the same fault repeated every second: %q", out)
	}
	// Restore to content identical to what is already in memory: a successful
	// read with no change. This is what used to leave the latch stuck.
	if err := os.WriteFile(path, good, 0o600); err != nil {
		t.Fatal(err)
	}
	_ = tick()
	garble()
	if out := tick(); !strings.Contains(out, "could not be re-read") {
		t.Fatalf("a RECURRING fault is permanently silent: %q", out)
	}
}

// TestWriteFileAtomicLeavesNoTempOnRenameFailure covers the deepest error arm
// reachable without an injected seam: write, sync and chmod all succeed and the
// RENAME fails. It deliberately does not claim to cover the write/sync/chmod
// arms — those need a seam this package does not have, and naming the test for
// an arm it never reaches would be a green over an absence.
func TestWriteFileAtomicLeavesNoTempOnRenameFailure(t *testing.T) {
	dir := t.TempDir()
	// A directory where the destination file belongs makes Rename fail after a
	// successful write, chmod and sync — the deepest error arm reachable here.
	dst := filepath.Join(dir, "dst")
	if err := os.Mkdir(dst, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "occupant"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(dst, []byte("hello"), 0o600); err == nil {
		t.Fatal("want an error renaming over a non-empty directory")
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".hesplay-tmp-") {
			t.Fatalf("temp file %s leaked after a failed rename", e.Name())
		}
	}
}
