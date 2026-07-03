package match

import "testing"

func TestRequirePublicHTTPS(t *testing.T) {
	// Literal-IP hosts resolve without external DNS, so these are deterministic.
	reject := []string{
		"http://example.com/i.jpg",   // not https
		"https://127.0.0.1/i.jpg",    // loopback
		"https://[::1]/i.jpg",        // loopback v6
		"https://10.0.0.5/i.jpg",     // private
		"https://192.168.1.10/i.jpg", // private
		"https://169.254.169.254/i",  // link-local (cloud metadata)
		"https://0.0.0.0/i.jpg",      // unspecified
		"https:///i.jpg",             // no host
		"://bad",                     // unparseable scheme/host
	}
	for _, u := range reject {
		if err := requirePublicHTTPS(u); err == nil {
			t.Errorf("requirePublicHTTPS(%q) = nil, want rejection", u)
		}
	}

	// Public literal IPs (and https scheme) pass the guard.
	accept := []string{
		"https://1.1.1.1/i.jpg",
		"https://8.8.8.8/i.jpg",
	}
	for _, u := range accept {
		if err := requirePublicHTTPS(u); err != nil {
			t.Errorf("requirePublicHTTPS(%q) = %v, want nil", u, err)
		}
	}
}
