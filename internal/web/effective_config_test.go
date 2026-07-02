package web

import (
	"testing"

	"hespera/internal/config"
)

func TestResolveEffectiveConfigMediaRoot(t *testing.T) {
	db := openTestDB(t)
	envRoot := t.TempDir()
	goodRoot := t.TempDir()
	base := config.Config{MediaRoot: envRoot}

	// No override → env value stands.
	if got := resolveEffectiveConfig(base, db).MediaRoot; got != envRoot {
		t.Fatalf("no override: got %q, want %q", got, envRoot)
	}

	// A valid absolute, existing dir overrides.
	if _, err := db.Exec("INSERT INTO app_settings (key, value) VALUES ('media_root', ?)", goodRoot); err != nil {
		t.Fatal(err)
	}
	if got := resolveEffectiveConfig(base, db).MediaRoot; got != goodRoot {
		t.Fatalf("valid override: got %q, want %q", got, goodRoot)
	}

	// A non-existent path falls back to the env value (never bricks the boot).
	if _, err := db.Exec("UPDATE app_settings SET value='/no/such/hespera/dir' WHERE key='media_root'"); err != nil {
		t.Fatal(err)
	}
	if got := resolveEffectiveConfig(base, db).MediaRoot; got != envRoot {
		t.Fatalf("missing dir falls back: got %q, want %q", got, envRoot)
	}

	// A relative path is rejected → fall back to env.
	if _, err := db.Exec("UPDATE app_settings SET value='relative/path' WHERE key='media_root'"); err != nil {
		t.Fatal(err)
	}
	if got := resolveEffectiveConfig(base, db).MediaRoot; got != envRoot {
		t.Fatalf("relative path falls back: got %q, want %q", got, envRoot)
	}
}
