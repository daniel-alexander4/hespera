package main

import "testing"

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
