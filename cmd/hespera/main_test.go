package main

import (
	"log/slog"
	"testing"
)

func TestParseLogLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"":        slog.LevelInfo,
		"info":    slog.LevelInfo,
		"DEBUG":   slog.LevelDebug,
		" debug ": slog.LevelDebug,
		"warn":    slog.LevelWarn,
		"warning": slog.LevelWarn,
		"error":   slog.LevelError,
		"garbage": slog.LevelInfo, // unrecognized → default
	}
	for in, want := range cases {
		if got := parseLogLevel(in); got != want {
			t.Errorf("parseLogLevel(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestAppURL(t *testing.T) {
	cases := []struct {
		bound string
		want  string
	}{
		{"127.0.0.1:54321", "http://127.0.0.1:54321/"}, // random loopback (app mode)
		{"[::]:8080", "http://127.0.0.1:8080/"},        // wildcard IPv6 → loopback host
		{"0.0.0.0:8080", "http://127.0.0.1:8080/"},     // wildcard IPv4 → loopback host
		{"127.0.0.1:8080", "http://127.0.0.1:8080/"},   // fixed loopback
	}
	for _, c := range cases {
		if got := appURL(c.bound); got != c.want {
			t.Errorf("appURL(%q) = %q, want %q", c.bound, got, c.want)
		}
	}
}

// TestLaunchDecision pins the second-instance policy. The load-bearing invariant
// is the app-mode+running rows: a desktop launch (which passes --replace) must
// ATTACH onto a healthy service, never kill it. Server mode refuses a second
// instance unless --replace asks for a deliberate take-over.
func TestLaunchDecision(t *testing.T) {
	const url = "http://127.0.0.1:8080/"
	cases := []struct {
		name       string
		appMode    bool
		replace    bool
		runningURL string
		want       launchAction
	}{
		{"app, none", true, false, "", launchProceed},
		{"app, none, replace", true, true, "", launchProceed},
		{"app, running -> attach", true, false, url, launchAttach},
		{"app, running, replace -> attach (never kill a healthy service)", true, true, url, launchAttach},
		{"server, none", false, false, "", launchProceed},
		{"server, running -> refuse", false, false, url, launchRefuse},
		{"server, running, replace -> take over", false, true, url, launchProceed},
	}
	for _, c := range cases {
		if got := launchDecision(c.appMode, c.replace, c.runningURL); got != c.want {
			t.Errorf("%s: launchDecision(%v,%v,%q) = %d, want %d",
				c.name, c.appMode, c.replace, c.runningURL, got, c.want)
		}
	}
}
