package podcast

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestPublicUnicastBlocksEverythingInward is the SSRF guard stated as a
// property. Each of these is a real escape, not a hypothetical: loopback
// reaches Hespera's own unauthenticated API on the same box, 169.254.169.254 is
// the cloud metadata endpoint that hands out credentials, the private ranges
// are the rest of the household including the router's admin page and the Vigo
// controller, and 100.64/10 is what Tailscale assigns.
func TestPublicUnicastBlocksEverythingInward(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "127.1.2.3", "::1",
		"10.0.0.5", "172.16.0.1", "172.31.255.254", "192.168.1.189",
		"169.254.169.254", // cloud metadata
		"fe80::1",         // link-local v6
		"0.0.0.0", "::",
		"224.0.0.1", "ff02::1", // multicast
		"100.64.0.1", "100.127.255.255", // CGNAT / tailnet
		"::ffff:127.0.0.1",   // v4-mapped loopback
		"::ffff:192.168.1.1", // v4-mapped private
	}
	for _, s := range blocked {
		ip := net.ParseIP(s)
		if ip == nil {
			t.Fatalf("test bug: %q is not an IP", s)
		}
		if publicUnicast(ip) {
			t.Errorf("%s must be blocked", s)
		}
	}

	allowed := []string{"8.8.8.8", "1.1.1.1", "93.184.216.34", "2606:2800:220:1:248:1893:25c8:1946", "100.63.255.255", "100.128.0.1"}
	for _, s := range allowed {
		ip := net.ParseIP(s)
		if ip == nil {
			t.Fatalf("test bug: %q is not an IP", s)
		}
		if !publicUnicast(ip) {
			t.Errorf("%s is a public address and must be allowed", s)
		}
	}
}

// TestRealClientRefusesLoopback proves the guard is WIRED, not merely written.
// httptest always binds loopback, so a client that can reach an httptest server
// is a client whose dialer policy is not in effect — which makes this both a
// guard test and the reason the fetch tests below inject their own transport.
func TestRealClientRefusesLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<rss><channel><title>nope</title></channel></rss>`))
	}))
	defer srv.Close()

	_, err := NewClient("Hespera/test").FetchFeed(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("the guard is not wired: a loopback URL was fetched")
	}
	if !strings.Contains(err.Error(), ErrBlockedAddress.Error()) {
		t.Fatalf("want a blocked-address error, got %v", err)
	}
}

func TestValidateURL(t *testing.T) {
	for _, ok := range []string{"http://example.com/feed.xml", "https://example.com/f", "https://example.com:8443/f"} {
		if _, err := ValidateURL(ok); err != nil {
			t.Errorf("%q should be accepted: %v", ok, err)
		}
	}
	// file: and data: would let a feed name something that is not a network
	// resource at all; gopher/ftp are not things this server should speak.
	for _, bad := range []string{"file:///etc/passwd", "data:text/xml,<rss/>", "ftp://example.com/f", "javascript:alert(1)", "", "   ", "https://", "not a url at all"} {
		if _, err := ValidateURL(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

// permissiveClient swaps in a transport with no address policy so the fetch
// logic can be exercised against httptest. The policy itself is covered above.
func permissiveClient(t *testing.T) *Client {
	t.Helper()
	c := NewClient("Hespera/test")
	c.http = &http.Client{}
	return c
}

func TestFetchFeedParses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("User-Agent"); got != "Hespera/test" {
			t.Errorf("User-Agent not sent: %q", got)
		}
		_, _ = w.Write([]byte(minimalFeed))
	}))
	defer srv.Close()

	f, err := permissiveClient(t).FetchFeed(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("FetchFeed: %v", err)
	}
	if f.Title != "Test Show" || len(f.Episodes) != 1 {
		t.Fatalf("got %+v", f)
	}
}

// TestFetchFeedCapsBody: a hostile host can declare any Content-Length, so the
// cap has to apply to bytes actually read, not to what the header claims.
func TestFetchFeedCapsBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		chunk := strings.Repeat("A", 1<<20)
		for i := 0; i < (maxFeedBytes>>20)+2; i++ {
			if _, err := w.Write([]byte(chunk)); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	_, err := permissiveClient(t).FetchFeed(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("an oversized body was accepted")
	}
	if !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("want a size-cap error, got %v", err)
	}
}

func TestFetchFeedSurfacesUpstreamStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := permissiveClient(t).FetchFeed(context.Background(), srv.URL)
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("want the upstream status in the error, got %v", err)
	}
}

// TestGetPassesRange is what makes seeking work through the proxy: the episode
// stream handler forwards the browser's Range header and this must carry it.
func TestGetPassesRange(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Range")
		w.WriteHeader(http.StatusPartialContent)
	}))
	defer srv.Close()

	resp, err := permissiveClient(t).Get(context.Background(), srv.URL, "bytes=100-200")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if got != "bytes=100-200" {
		t.Fatalf("Range not forwarded: %q", got)
	}
}
