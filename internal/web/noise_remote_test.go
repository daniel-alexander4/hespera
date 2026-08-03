package web

import (
	"context"
	"testing"
)

// TestEffectiveNoiseRemoteURLWhitelistsScheme is a stored-XSS guard, not a
// tidiness check. The value becomes an href on the home page of every device in
// the household, and Hespera has no authentication — so anything that can write
// app_settings (the settings form, `hescli config set`) could otherwise plant a
// javascript: URL that runs for everyone who opens the app.
//
// Validated at READ time, the sanitizeLangSetting idiom, so a value written by
// a path that never saw the form is held to the same rule.
func TestEffectiveNoiseRemoteURLWhitelistsScheme(t *testing.T) {
	h, db := newTestHandler(t)
	ctx := context.Background()

	set := func(v string) {
		if _, err := db.Exec(
			"INSERT INTO app_settings (key,value) VALUES ('noise_remote_url',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", v,
		); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("http and https pass through", func(t *testing.T) {
		for _, in := range []string{"http://raspberrypi:8090", "https://noise.example:443/x"} {
			set(in)
			if got := h.effectiveNoiseRemoteURL(ctx); got != in {
				t.Errorf("%q: got %q", in, got)
			}
		}
	})

	t.Run("dangerous and unusable values yield nothing", func(t *testing.T) {
		bad := []string{
			"javascript:alert(1)",
			"JavaScript:alert(1)",
			"data:text/html,<script>alert(1)</script>",
			"file:///etc/passwd",
			"vbscript:msgbox(1)",
			"raspberrypi:8090", // no scheme: parses as scheme "raspberrypi"
			"not a url at all",
			"",
			"   ",
			"http://", // no host
		}
		for _, in := range bad {
			set(in)
			if got := h.effectiveNoiseRemoteURL(ctx); got != "" {
				t.Errorf("%q should yield \"\", got %q", in, got)
			}
		}
	})

	t.Run("unset yields nothing", func(t *testing.T) {
		if _, err := db.Exec("DELETE FROM app_settings WHERE key='noise_remote_url'"); err != nil {
			t.Fatal(err)
		}
		if got := h.effectiveNoiseRemoteURL(ctx); got != "" {
			t.Errorf("got %q, want empty so the card stays hidden", got)
		}
	})
}
