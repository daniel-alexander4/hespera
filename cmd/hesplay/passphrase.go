package main

// `hesplay passphrase` — read and change the phone remote's passphrase.
//
// Pure file work, dispatched before the server probe for the same reason
// `server` is: it has to work when no Hespera is up, and on a headless box it
// is often the reason you sshed in.

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// passphraseStdin is where `set` reads from — a package var so tests can drive
// it without a terminal, the noisePause / powerOff seam shape.
var passphraseStdin io.Reader = os.Stdin

// passphraseUsage is printed for `hesplay passphrase help` and on a bad verb.
const passphraseUsage = `hesplay passphrase — the phone remote's sign-in

  hesplay passphrase          Print the passphrase
  hesplay passphrase path     Print the file it lives in
  hesplay passphrase set      Replace it — reads the new words from stdin
  hesplay passphrase rotate   Replace it with three fresh random words
  hesplay passphrase logout   Keep the words, sign every phone out

set/rotate/logout invalidate every signed-in phone. A running remote picks the
change up within a second — no restart. The words are never taken as a command
argument: ps is world-readable and your shell keeps history.`

// cmdPassphrase dispatches the passphrase verbs.
func cmdPassphrase(args []string) error {
	if len(args) > 0 && isHelp(args[0]) {
		fmt.Fprintln(os.Stderr, passphraseUsage)
		return nil
	}
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	if len(args) > 1 {
		return fmt.Errorf("passphrase %s: no arguments — the words are read from stdin, never the command line", sub)
	}

	p, err := passphrasePath()
	if err != nil {
		return fmt.Errorf("no config directory: %w", err)
	}

	switch sub {
	case "path":
		fmt.Println(p)
		return nil

	case "":
		// Read-only, and deliberately NOT via newAuth: that generates and
		// persists on a fresh box and renames a garbled file aside. Showing
		// something must never create it.
		phrase, _, err := readAuthFile(p)
		if err != nil {
			return describeAuthFileErr(p, err)
		}
		// Printed unconditionally, not TTY-gated. The gate on the server's
		// startup announce exists to keep the words out of a persistent journal;
		// an explicit one-shot request is the opposite case, and gating it would
		// make `P=$(hesplay passphrase)` hand back a path.
		fmt.Println(phrase)
		return nil

	case "set", "rotate", "logout":
		return changePassphrase(p, sub)

	default:
		fmt.Fprintln(os.Stderr, passphraseUsage)
		return fmt.Errorf("passphrase: unknown command %q", sub)
	}
}

// describeAuthFileErr turns a readAuthFile sentinel into something an operator
// can act on, naming the file every time.
func describeAuthFileErr(path string, err error) error {
	switch {
	case errors.Is(err, errNoPassphrase):
		return fmt.Errorf("no passphrase yet at %s — it is created the first time the remote runs (hesplay --listen :8090), or run: hesplay passphrase rotate", path)
	case errors.Is(err, errPassphraseGarbled):
		return fmt.Errorf("%s holds no usable words (a-z only) — `hesplay passphrase set` or `rotate` will replace it, keeping this one as %s.unusable", path, path)
	default:
		return fmt.Errorf("cannot read %s: %w%s", path, err, privilegeHint(path))
	}
}

// privilegeHint appends the ownership/mode facts when a file cannot be read,
// because the usual cause is a root-owned file left by a `sudo` run.
func privilegeHint(path string) string {
	fi, statErr := os.Stat(path)
	if statErr != nil {
		return ""
	}
	return fmt.Sprintf(" (mode %o; you are uid %d — if this is root-owned, a `sudo hesplay passphrase` created it)", fi.Mode().Perm(), os.Geteuid())
}

// refuseIfPrivileged stops a sudo'd run before it writes anything.
//
// os.UserConfigDir() follows $HOME, so under sudo it resolves to root's config
// directory — or, when HOME survives, root writes into the invoking user's 0700
// directory successfully, because root ignores the mode. Either way the remote
// (running as the ordinary user) then cannot read its own passphrase file: the
// reader reports it unreadable, keeps whatever is in memory, and the next
// restart falls back to an EPHEMERAL passphrase that changes on every restart.
// That state cannot be undone without sudo, which is why this refuses rather
// than warns.
func refuseIfPrivileged(path, sub string) error {
	if os.Geteuid() != 0 {
		return nil
	}
	who := os.Getenv("SUDO_USER")
	if who == "" {
		return nil // genuinely running as root; that is the operator's own box
	}
	return errors.New(sudoRefusalText(path, who, sub))
}

// sudoRefusalText is split out so the wording is testable without being root —
// and the wording is the point: it is a command the operator will copy.
func sudoRefusalText(path, who, sub string) string {
	return fmt.Sprintf("refusing to run as root: this would write %s, which the remote (running as %s) could not read afterwards — run it as that account instead:\n    sudo -u %s hesplay passphrase %s",
		path, who, who, sub)
}

// readPassphraseFromStdin reads one line of words.
//
// Never from argv: ps(1) is world-readable and shells keep history. An empty
// line, EOF before any line, and input with no usable letters are all hard
// errors that write NOTHING — a file without a phrase line reads as "no
// passphrase", so the next start would silently mint random words and lock the
// household out of its own speakers.
func readPassphraseFromStdin(in io.Reader) (string, error) {
	r := bufio.NewReader(in)
	// One more byte than the cap, so an over-long line is an error rather than a
	// silent truncation: storing something the operator did not type is worse
	// than refusing.
	buf := make([]byte, 0, maxPassphraseInput+1)
	for len(buf) <= maxPassphraseInput {
		b, err := r.ReadByte()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return "", err
		}
		if b == '\n' {
			break
		}
		buf = append(buf, b)
	}
	if len(buf) > maxPassphraseInput {
		return "", fmt.Errorf("passphrase too long (over %d bytes)", maxPassphraseInput)
	}
	line := strings.TrimSpace(string(buf))
	if line == "" {
		return "", errors.New("no passphrase given on stdin — nothing was changed")
	}
	phrase := normalizePassphrase(line)
	if phrase == "" {
		return "", errors.New("that has no a-z words in it — the wordlist is plain ASCII, so nothing usable survived; nothing was changed")
	}
	return phrase, nil
}

// changePassphrase implements set / rotate / logout.
// changePassphrase implements set / rotate / logout.
//
// Order is the whole correctness argument here: VALIDATE, then RESOLVE the new
// passphrase, and only then MUTATE anything on disk. The first version moved a
// garbled file aside before it knew whether it would write — so `logout` on such
// a file renamed it away and *then* refused ("nothing to keep"), leaving the box
// with no passphrase file at all and the next start minting random words. A
// command that fails must leave the box exactly as it was.
func changePassphrase(path, sub string) error {
	if err := refuseIfPrivileged(path, sub); err != nil {
		return err
	}

	existing, _, readErr := readAuthFile(path)
	switch {
	case readErr == nil, errors.Is(readErr, errNoPassphrase), errors.Is(readErr, errPassphraseGarbled):
		// Proceed. A garbled file is preserved below, once we know we will write.
	default:
		// Unreadable is the one case that must not be written over: the words in
		// there may be the ones the household knows.
		return fmt.Errorf("refusing to overwrite a file that cannot be read: %w%s", readErr, privilegeHint(path))
	}

	// --- resolve, still touching nothing ---
	var phrase string
	switch sub {
	case "set":
		// Gate the prompt on the reader actually being read, not on os.Stdin —
		// they diverge under the passphraseStdin seam.
		if f, ok := passphraseStdin.(*os.File); ok && term(f) {
			fmt.Fprint(os.Stderr, "New passphrase (three words): ")
		}
		p, err := readPassphraseFromStdin(passphraseStdin)
		if err != nil {
			return err
		}
		phrase = p
	case "rotate":
		p, err := generatePassphrase()
		if err != nil {
			return err
		}
		phrase = normalizePassphrase(p)
	case "logout":
		if existing == "" {
			return fmt.Errorf("no passphrase to keep at %s — use `hesplay passphrase rotate`", path)
		}
		phrase = existing
	}

	// --- mutate ---
	if errors.Is(readErr, errPassphraseGarbled) {
		aside, mvErr := preserveUnusable(path)
		if mvErr != nil {
			// Never fall through to the write: that would destroy what the
			// operator put there, which is the thing preserving it exists to stop.
			return fmt.Errorf("%s holds no usable words and could not be moved aside (%v) — refusing to overwrite it", path, mvErr)
		}
		fmt.Fprintf(os.Stderr, "warn: %s held no usable words; kept as %s\n", path, aside)
	}

	// A NEW key on every change, including `logout` and a `set` back to a phrase
	// used before. Changing the words already invalidates every cookie (the
	// token is HMAC(key, words)), so this is not what does the revoking — it is
	// what stops REPLAY: reuse an old passphrase under the old key and every
	// cookie ever issued for it validates again.
	key, err := newInstanceKey()
	if err != nil {
		return err
	}
	if err := writeAuthFile(path, phrase, key); err != nil {
		return err
	}

	// Read back what is actually on disk rather than reporting what we meant to
	// write — this is the check that catches a wrong path, a wrong owner, and a
	// write that silently went somewhere else.
	got, _, err := readAuthFile(path)
	if err != nil {
		return fmt.Errorf("wrote %s but cannot read it back: %w%s", path, err, privilegeHint(path))
	}
	if got != phrase {
		return fmt.Errorf("wrote %s but it reads back as %q", path, got)
	}

	if sub == "logout" {
		fmt.Println("every phone signed out; the words are unchanged")
	} else if term(os.Stdout) {
		fmt.Println(got)
	} else {
		fmt.Println("passphrase changed —", path)
	}
	fmt.Fprintln(os.Stderr, "every signed-in phone must sign in again; a running remote picks this up within a second")
	return nil
}

// term reports whether f is a terminal.
func term(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
