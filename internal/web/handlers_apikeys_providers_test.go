package web

import (
	"context"
	"net/http"
	"net/url"
	"testing"
)

// TestSettingsAPIKeysPerKeyForms verifies each key has an independent form: saving
// one provider key must not wipe the others (the per-form-dispatch guard), and a
// blank submit clears only that key.
func TestSettingsAPIKeysPerKeyForms(t *testing.T) {
	h, _ := newTestHandler(t)
	router := h.Router()

	save := func(field, val string) {
		if rec := postForm(t, router, "/settings/api-keys", url.Values{field: {val}}); rec.Code != http.StatusSeeOther {
			t.Fatalf("POST %s=%q: status %d, want 303", field, val, rec.Code)
		}
	}

	save("tmdb_api_key", "tmdb-secret")
	save("fanarttv_api_key", "fanart-secret")
	save("audiodb_api_key", "audiodb-secret")

	// All three coexist — saving fanart/audiodb did NOT clear tmdb.
	if got := h.effectiveTMDBKey(context.Background()); got != "tmdb-secret" {
		t.Fatalf("tmdb key = %q after saving others, want tmdb-secret (cross-wipe!)", got)
	}
	if got := h.effectiveFanartKey(context.Background()); got != "fanart-secret" {
		t.Fatalf("fanart key = %q", got)
	}
	if got := h.effectiveAudioDBKey(context.Background()); got != "audiodb-secret" {
		t.Fatalf("audiodb key = %q", got)
	}

	// Clearing fanart leaves the others intact.
	save("fanarttv_api_key", "")
	if got := h.effectiveFanartKey(context.Background()); got != "" {
		t.Fatalf("fanart key after clear = %q, want empty", got)
	}
	if got := h.effectiveTMDBKey(context.Background()); got != "tmdb-secret" {
		t.Fatalf("tmdb key = %q after clearing fanart, want tmdb-secret", got)
	}
	if got := h.effectiveAudioDBKey(context.Background()); got != "audiodb-secret" {
		t.Fatalf("audiodb key = %q after clearing fanart", got)
	}
}
