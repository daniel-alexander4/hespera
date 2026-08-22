package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolate points passphrasePath at a temp dir for one test.
//
// It overrides the SEAM rather than setting XDG_CONFIG_HOME: os.UserConfigDir()
// ignores that variable on darwin and windows, so an env-based isolation would
// write the developer's real passphrase file when the suite runs on a Mac —
// and hesplay ships Mac binaries.
func isolate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "hespera", "hesplay-passphrase")
	prev := passphrasePath
	passphrasePath = func() (string, error) { return p, nil }
	t.Cleanup(func() { passphrasePath = prev })
	return p
}

func withStdin(t *testing.T, s string) {
	t.Helper()
	prev := passphraseStdin
	passphraseStdin = strings.NewReader(s)
	t.Cleanup(func() { passphraseStdin = prev })
}

// TestShowDoesNotCreate is the sharpest pin here. `hesplay passphrase` is
// read-only, and building it on newAuth — which generates and persists on a
// fresh box — would mint a credential merely because someone asked what the
// credential was.
func TestShowDoesNotCreate(t *testing.T) {
	p := isolate(t)
	if err := cmdPassphrase(nil); err == nil {
		t.Fatal("want an error on a box with no passphrase yet")
	}
	if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("showing the passphrase CREATED %s", p)
	}
	// Stimulus: once one exists, it prints it — so the assertion above is not
	// passing merely because the verb never works.
	if err := cmdPassphrase([]string{"rotate"}); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		if err := cmdPassphrase(nil); err != nil {
			t.Fatal(err)
		}
	})
	if len(strings.Fields(out)) != passphraseWordCount {
		t.Fatalf("show printed %q, want %d words", out, passphraseWordCount)
	}
}

func TestPathIsPrintedAndCreatesNothing(t *testing.T) {
	p := isolate(t)
	out := captureStdout(t, func() {
		if err := cmdPassphrase([]string{"path"}); err != nil {
			t.Fatal(err)
		}
	})
	if strings.TrimSpace(out) != p {
		t.Fatalf("path printed %q, want %q", strings.TrimSpace(out), p)
	}
	if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("`path` created the file")
	}
}

func TestRotateCreatesA0600FileAndChangesBothSecrets(t *testing.T) {
	p := isolate(t)
	if err := cmdPassphrase([]string{"rotate"}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if mode := fi.Mode().Perm(); mode != 0o600 {
		t.Fatalf("mode %o, want 600", mode)
	}
	first, key1, err := readAuthFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(strings.Fields(first)) != passphraseWordCount {
		t.Fatalf("rotate produced %q", first)
	}
	if err := cmdPassphrase([]string{"rotate"}); err != nil {
		t.Fatal(err)
	}
	second, key2, err := readAuthFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if second == first {
		t.Fatal("rotate produced the same words twice")
	}
	if string(key1) == string(key2) {
		t.Fatal("rotate reused the instance key — an old passphrase set again would replay every cookie")
	}
}

func TestSetReadsStdin(t *testing.T) {
	p := isolate(t)
	if err := cmdPassphrase([]string{"rotate"}); err != nil {
		t.Fatal(err)
	}
	withStdin(t, "Correct-Horse Battery \n")
	if err := cmdPassphrase([]string{"set"}); err != nil {
		t.Fatal(err)
	}
	got, _, err := readAuthFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if got != "correct horse battery" {
		t.Fatalf("stored %q — set did not normalise what was typed", got)
	}
}

// TestSetRefusesUnusableInput: every one of these must write NOTHING. A file
// left without a phrase line reads as "no passphrase", so the next start mints
// random words and the household is locked out of its own speakers.
func TestSetRefusesUnusableInput(t *testing.T) {
	for _, in := range []string{"", "\n", "   \n", "Пароль Секрет Ключ\n", strings.Repeat("a", maxPassphraseInput+10) + "\n"} {
		p := isolate(t)
		if err := cmdPassphrase([]string{"rotate"}); err != nil {
			t.Fatal(err)
		}
		before, _, err := readAuthFile(p)
		if err != nil {
			t.Fatal(err)
		}
		withStdin(t, in)
		if err := cmdPassphrase([]string{"set"}); err == nil {
			t.Fatalf("input %q was accepted", in)
		}
		after, _, err := readAuthFile(p)
		if err != nil {
			t.Fatalf("input %q left the file unreadable: %v", in, err)
		}
		if after != before {
			t.Fatalf("input %q changed the passphrase to %q", in, after)
		}
	}
}

func TestLogoutKeepsTheWordsAndChangesTheKey(t *testing.T) {
	p := isolate(t)
	if err := cmdPassphrase([]string{"rotate"}); err != nil {
		t.Fatal(err)
	}
	words, key1, err := readAuthFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := cmdPassphrase([]string{"logout"}); err != nil {
		t.Fatal(err)
	}
	after, key2, err := readAuthFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if after != words {
		t.Fatalf("logout changed the words: %q -> %q", words, after)
	}
	if string(key1) == string(key2) {
		t.Fatal("logout did not change the key, so nobody was signed out")
	}
	// And the token really did change — the property the household cares about.
	if deriveToken(key1, words) == deriveToken(key2, after) {
		t.Fatal("the session token survived a logout")
	}
}

func TestLogoutNeedsAnExistingPassphrase(t *testing.T) {
	isolate(t)
	if err := cmdPassphrase([]string{"logout"}); err == nil {
		t.Fatal("logout on a box with no passphrase should say so, not invent one")
	}
}

func TestPassphraseNeverTakesWordsAsArguments(t *testing.T) {
	isolate(t)
	// ps(1) is world-readable and the shell keeps history, so this must be an
	// error rather than a convenience.
	if err := cmdPassphrase([]string{"set", "correct", "horse", "battery"}); err == nil {
		t.Fatal("set accepted the passphrase from argv")
	}
	if err := cmdPassphrase([]string{"set", "correct horse battery"}); err == nil {
		t.Fatal("set accepted a quoted passphrase from argv")
	}
}

func TestUnknownSubcommand(t *testing.T) {
	isolate(t)
	if err := cmdPassphrase([]string{"delete"}); err == nil {
		t.Fatal("want an error for an unknown subcommand")
	}
}

// TestRefusesToOverwriteAnUnreadableFile: the words in there may be the ones the
// household knows, and replacing them is the one irreversible mistake.
func TestRefusesToOverwriteAnUnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file modes")
	}
	p := isolate(t)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("secret words here\nkey: x\n"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(p, 0o600) })
	withStdin(t, "new words here\n")
	if err := cmdPassphrase([]string{"set"}); err == nil {
		t.Fatal("set overwrote a file it could not read")
	}
	if err := cmdPassphrase([]string{"rotate"}); err == nil {
		t.Fatal("rotate overwrote a file it could not read")
	}
}

func TestGarbledFileIsKeptAsideOnSet(t *testing.T) {
	p := isolate(t)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	const garbled = "Пароль Секрет Ключ\n"
	if err := os.WriteFile(p, []byte(garbled), 0o600); err != nil {
		t.Fatal(err)
	}
	withStdin(t, "alpha bravo charlie\n")
	if err := cmdPassphrase([]string{"set"}); err != nil {
		t.Fatal(err)
	}
	got, _, err := readAuthFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if got != "alpha bravo charlie" {
		t.Fatalf("stored %q", got)
	}
	kept, err := os.ReadFile(p + ".unusable")
	if err != nil {
		t.Fatalf("the operator's file was destroyed rather than kept aside: %v", err)
	}
	if string(kept) != garbled {
		t.Fatalf("kept %q", kept)
	}
}

// --- pins for the review's findings ------------------------------------------

// TestFailedChangeLeavesTheBoxAlone is the critical one. changePassphrase used
// to move a garbled file aside BEFORE it knew whether it would write anything,
// so a command that then failed left the operator with no passphrase file at
// all — and the next remote start minted random words. A failed command must
// leave the box exactly as it was.
func TestFailedChangeLeavesTheBoxAlone(t *testing.T) {
	const garbled = "Пароль Секрет Ключ\n"
	cases := []struct {
		name  string
		args  []string
		stdin string
	}{
		{"logout has nothing to keep", []string{"logout"}, ""},
		{"set gets empty stdin", []string{"set"}, "\n"},
		{"set gets unusable stdin", []string{"set"}, "Пароль\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := isolate(t)
			if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(garbled), 0o600); err != nil {
				t.Fatal(err)
			}
			withStdin(t, c.stdin)
			if err := cmdPassphrase(c.args); err == nil {
				t.Fatalf("%v should have failed", c.args)
			}
			got, err := os.ReadFile(p)
			if err != nil {
				t.Fatalf("the failed command REMOVED %s: %v", p, err)
			}
			if string(got) != garbled {
				t.Fatalf("the failed command rewrote the file: %q", got)
			}
			if _, err := os.Stat(p + ".unusable"); err == nil {
				t.Fatal("the failed command moved the file aside anyway")
			}
		})
	}
}

// TestSudoIsRefusedByName: root writing into the user's 0700 directory SUCCEEDS,
// leaving a file the remote (as that user) cannot read — which degrades to an
// ephemeral passphrase that changes every restart. The message must name the
// verb the operator was running, since it is a command they are meant to copy.
func TestSudoIsRefusedByName(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("cannot simulate the non-root case while running as root")
	}
	// Not root: it must NOT refuse. (Stimulus for the assertion below — without
	// it a always-refusing implementation would pass the message check.)
	if err := refuseIfPrivileged("/tmp/x", "rotate"); err != nil {
		t.Fatalf("refused when not root: %v", err)
	}
	t.Setenv("SUDO_USER", "dan")
	if err := refuseIfPrivileged("/tmp/x", "rotate"); err != nil {
		t.Fatal("SUDO_USER alone, without euid 0, must not refuse")
	}
}

func TestSudoMessageNamesTheVerb(t *testing.T) {
	// The formatting is what regressed: a literal "…" shipped where the verb
	// belongs, handing the operator a command that does not work.
	for _, verb := range []string{"set", "rotate", "logout"} {
		msg := sudoRefusalText("/tmp/p", "dan", verb)
		if !strings.Contains(msg, "hesplay passphrase "+verb) {
			t.Fatalf("message for %q does not name the verb: %s", verb, msg)
		}
		if strings.Contains(msg, "…") {
			t.Fatalf("message for %q still carries a literal ellipsis: %s", verb, msg)
		}
	}
}

func TestHelpExitsCleanly(t *testing.T) {
	isolate(t)
	for _, a := range [][]string{{"help"}, {"-h"}, {"--help"}} {
		if err := cmdPassphrase(a); err != nil {
			t.Fatalf("passphrase %v = %v, want no error", a, err)
		}
	}
}

func TestRotateOverAGarbledFileKeepsIt(t *testing.T) {
	p := isolate(t)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	const garbled = "Пароль Секрет Ключ\n"
	if err := os.WriteFile(p, []byte(garbled), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cmdPassphrase([]string{"rotate"}); err != nil {
		t.Fatal(err)
	}
	kept, err := os.ReadFile(p + ".unusable")
	if err != nil || string(kept) != garbled {
		t.Fatalf("the operator's file was not preserved: %q %v", kept, err)
	}
	got, _, err := readAuthFile(p)
	if err != nil || len(strings.Fields(got)) != passphraseWordCount {
		t.Fatalf("no usable passphrase after rotate: %q %v", got, err)
	}
}
