// Package podcast fetches and parses podcast feeds, and is the only place in
// Hespera that talks to a host the user chose.
//
// Everywhere else the server's outbound hosts are compiled in — TMDB,
// MusicBrainz, ListenBrainz, Cover Art Archive, Wikipedia, LRCLIB,
// OpenSubtitles, GitHub. That is deliberate, and the codebase has refused
// fetch-by-URL three separate times: album art is upload-only, the artist image
// picker requires the chosen URL be a member of a candidate set the server
// itself produced, and OpenSubtitles downloads are pinned to
// *.opensubtitles.com. A podcast subscription cannot work that way — the whole
// point is a feed only the user knows about.
//
// So this package concentrates the entire new exposure into ONE function,
// Fetch, and everything about podcasts goes through it. Nothing else in the
// tree should ever hold a URL that came from a feed.
package podcast

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
)

const (
	// maxRedirects bounds a redirect chain. Podcast audio routinely goes
	// through two or three tracking prefixes before reaching a CDN, so this
	// cannot be zero — but every hop is re-validated, so a chain that starts
	// public and ends at 127.0.0.1 is refused at the hop that turns inward.
	maxRedirects = 5

	// maxFeedBytes bounds a feed body. Real feeds with a decade of episodes
	// reach a few MB; anything past this is not a feed we can use, and reading
	// it unbounded is how a hostile host turns a subscribe button into memory
	// exhaustion.
	maxFeedBytes = 16 << 20

	// feedTimeout bounds a whole feed fetch including redirects.
	feedTimeout = 30 * time.Second
)

// ErrBlockedAddress is returned when a URL resolves somewhere the server must
// not reach. Distinguished from a transport error so a caller can tell "this
// feed is hostile or misconfigured" from "the network is down".
var ErrBlockedAddress = errors.New("address is not reachable by policy")

// safeDialer refuses to connect to anything that is not a public unicast
// address.
//
// The check runs on the RESOLVED address inside the dialer, not on the
// hostname, and that placement is the entire point. Validating the hostname up
// front is the classic broken version: DNS is controlled by the same party that
// controls the feed, so a name that resolves public during validation can
// resolve to 127.0.0.1 microseconds later when the connection is actually made
// (DNS rebinding). Checking in Control means the check applies to the address
// the socket is about to use, which is the only address that matters.
func safeDialer() *net.Dialer {
	return &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return fmt.Errorf("%w: unparseable address %q", ErrBlockedAddress, address)
			}
			ip := net.ParseIP(host)
			if ip == nil {
				return fmt.Errorf("%w: %q is not an IP", ErrBlockedAddress, host)
			}
			if !publicUnicast(ip) {
				return fmt.Errorf("%w: %s", ErrBlockedAddress, ip)
			}
			return nil
		},
	}
}

// publicUnicast reports whether an address is one the server may talk to.
//
// Deny-listed on purpose rather than allow-listed: the set of addresses that
// must never be reachable is small, closed and well-defined, while "the public
// internet" is not. Named explicitly because each one is a real escape:
// loopback reaches Hespera's own unauthenticated API and the hescli socket's
// neighbours; link-local 169.254.169.254 is the cloud metadata endpoint that
// hands out credentials; private ranges are the rest of the household,
// including the router's admin page and the Vigo controller.
func publicUnicast(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() {
		return false
	}
	// 100.64.0.0/10, carrier-grade NAT. Not covered by IsPrivate, and it is
	// what Tailscale hands out — so without this, a tailnet peer is reachable.
	if v4 := ip.To4(); v4 != nil && v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
		return false
	}
	// IPv4-mapped IPv6 (::ffff:127.0.0.1) would otherwise slip past the v6
	// checks above while connecting to a v4 loopback.
	if v4 := ip.To4(); v4 != nil && !ip.Equal(v4) {
		return publicUnicast(v4)
	}
	return true
}

// Client fetches feeds and episodes under the policy above. Zero value is not
// usable — use NewClient.
type Client struct {
	http *http.Client
	ua   string
}

// NewClient builds a fetcher. The User-Agent identifies Hespera the way the
// MusicBrainz client does: podcast hosts block unlabelled clients, and an
// honest name is also what lets a host complain to the right place.
func NewClient(userAgent string) *Client {
	if strings.TrimSpace(userAgent) == "" {
		userAgent = "Hespera"
	}
	tr := &http.Transport{
		DialContext:           safeDialer().DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
		MaxIdleConns:          8,
		IdleConnTimeout:       60 * time.Second,
	}
	return &Client{
		http: &http.Client{
			Transport: tr,
			// Every hop re-enters the dialer, so each is address-checked; this
			// only bounds the chain length.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= maxRedirects {
					return fmt.Errorf("stopped after %d redirects", maxRedirects)
				}
				if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
					return fmt.Errorf("%w: redirect to %q", ErrBlockedAddress, req.URL.Scheme)
				}
				return nil
			},
		},
		ua: userAgent,
	}
}

// ValidateURL checks the parts of a URL that can be judged without connecting:
// the scheme, and that there is a host at all. Address policy is NOT enforced
// here — see safeDialer for why that has to happen at connect time.
func ValidateURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("not a URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("only http and https feeds are supported, not %q", u.Scheme)
	}
	if u.Host == "" {
		return "", errors.New("URL has no host")
	}
	return u.String(), nil
}

// Get performs a guarded request. rangeHdr, when non-empty, is passed through —
// that is what lets episode playback seek without this package knowing anything
// about media.
//
// The caller owns closing the body.
func (c *Client) Get(ctx context.Context, rawURL, rangeHdr string) (*http.Response, error) {
	clean, err := ValidateURL(rawURL)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, clean, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.ua)
	req.Header.Set("Accept", "*/*")
	if rangeHdr != "" {
		req.Header.Set("Range", rangeHdr)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		resp.Body.Close()
		return nil, fmt.Errorf("upstream returned %s", resp.Status)
	}
	return resp, nil
}

// FetchFeed retrieves and parses a feed. The body is size-capped while being
// read rather than trusted from Content-Length, which a hostile host controls.
func (c *Client) FetchFeed(ctx context.Context, rawURL string) (*Feed, error) {
	ctx, cancel := context.WithTimeout(ctx, feedTimeout)
	defer cancel()

	resp, err := c.Get(ctx, rawURL, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxFeedBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read feed: %w", err)
	}
	if len(body) > maxFeedBytes {
		return nil, fmt.Errorf("feed is larger than %d bytes", maxFeedBytes)
	}
	return ParseFeed(body)
}
