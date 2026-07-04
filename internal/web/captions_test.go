package web

import (
	"context"
	"testing"
)

func TestCaptionStyleVars(t *testing.T) {
	h, db := newTestHandler(t)
	ctx := context.Background()

	// Defaults (no rows): pre-settings look.
	if got := string(h.captionStyleVars(ctx)); got != "--cap-scale:1;--cap-bg:rgba(0,0,0,0.6);--cap-raise:0rem" {
		t.Errorf("defaults: %q", got)
	}

	for k, v := range map[string]string{
		"subtitle_size":     "xlarge",
		"subtitle_bg":       "none",
		"subtitle_position": "high",
	} {
		if _, err := db.Exec("INSERT INTO app_settings(key,value) VALUES(?,?)", k, v); err != nil {
			t.Fatalf("seed %s: %v", k, err)
		}
	}
	if got := string(h.captionStyleVars(ctx)); got != "--cap-scale:1.5;--cap-bg:rgba(0,0,0,0);--cap-raise:5rem" {
		t.Errorf("configured: %q", got)
	}

	// Garbage stored past the form (hescli writes unvalidated) degrades to the
	// default at read time — nothing user-controlled reaches the template.CSS.
	if _, err := db.Exec("UPDATE app_settings SET value='</style><script>' WHERE key='subtitle_size'"); err != nil {
		t.Fatalf("poison: %v", err)
	}
	if got := h.effectiveSubtitleSize(ctx); got != "normal" {
		t.Errorf("poisoned size = %q, want normal", got)
	}
	if got := string(h.captionStyleVars(ctx)); got != "--cap-scale:1;--cap-bg:rgba(0,0,0,0);--cap-raise:5rem" {
		t.Errorf("poisoned vars: %q", got)
	}
}
