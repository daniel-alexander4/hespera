package browser

import "testing"

func TestDisplayName(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"/usr/bin/google-chrome", "Google Chrome"},
		{"/usr/bin/google-chrome-stable", "Google Chrome"},
		{"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", "Google Chrome"},
		{"/usr/bin/chromium", "Chromium"},
		{"/usr/bin/chromium-browser", "Chromium"},
		{"/usr/bin/microsoft-edge", "Microsoft Edge"},
		{`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`, "Microsoft Edge"},
		{"/usr/bin/brave-browser", "Brave"},
		{"/opt/weird/thing", "thing"},
	}
	for _, tc := range tests {
		if got := displayName(tc.path); got != tc.want {
			t.Errorf("displayName(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestBrowserVersionRe(t *testing.T) {
	tests := []struct {
		out  string
		want string
	}{
		{"Google Chrome 149.0.7827.200\n", "149.0.7827.200"},
		{"Chromium 149.0.7827.200 for Linux Mint\n", "149.0.7827.200"},
		{"Brave Browser 1.71.123 Chromium: 130.0\n", "1.71.123"},
		{"no digits here", ""},
	}
	for _, tc := range tests {
		if got := browserVersionRe.FindString(tc.out); got != tc.want {
			t.Errorf("version of %q = %q, want %q", tc.out, got, tc.want)
		}
	}
}
