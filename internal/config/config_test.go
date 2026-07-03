package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFromEnvDefaults(t *testing.T) {
	// Clear any env vars that might interfere.
	for _, k := range []string{
		"HESPERA_LISTEN", "HESPERA_DATA_DIR", "HESPERA_DB_PATH",
		"HESPERA_MEDIA_ROOT",
	} {
		os.Unsetenv(k)
	}

	cfg := FromEnv()
	if cfg.Listen != "127.0.0.1:8080" {
		t.Fatalf("expected loopback default Listen=127.0.0.1:8080, got %q", cfg.Listen)
	}
	// The defaults are now per-user and OS-appropriate (no container, runs as the
	// invoking user). Assert they match the resolvers and are absolute, rather
	// than a hard-coded Unix path.
	if want := defaultDataDir(); cfg.DataDir != want {
		t.Fatalf("expected DataDir=%q, got %q", want, cfg.DataDir)
	}
	if want := filepath.Join(defaultDataDir(), "hespera.sqlite"); cfg.DBPath != want {
		t.Fatalf("expected DBPath=%q, got %q", want, cfg.DBPath)
	}
	if want := defaultMediaRoot(); cfg.MediaRoot != want {
		t.Fatalf("expected MediaRoot=%q, got %q", want, cfg.MediaRoot)
	}
	for _, p := range []string{cfg.DataDir, cfg.DBPath, cfg.MediaRoot} {
		if !filepath.IsAbs(p) {
			t.Fatalf("default path must be absolute, got %q", p)
		}
	}
	if cfg.FFmpegConcurrentLimit != 4 {
		t.Fatalf("expected FFmpegConcurrentLimit=4, got %d", cfg.FFmpegConcurrentLimit)
	}
}

func TestFromEnvCustom(t *testing.T) {
	os.Setenv("HESPERA_LISTEN", ":9090")
	os.Setenv("HESPERA_DATA_DIR", "/tmp/iso")
	defer func() {
		os.Unsetenv("HESPERA_LISTEN")
		os.Unsetenv("HESPERA_DATA_DIR")
	}()

	cfg := FromEnv()
	if cfg.Listen != ":9090" {
		t.Fatalf("expected Listen=:9090, got %q", cfg.Listen)
	}
	if cfg.DataDir != "/tmp/iso" {
		t.Fatalf("expected DataDir=/tmp/iso, got %q", cfg.DataDir)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{
			name: "valid_no_auth",
			cfg: Config{
				Listen:    ":8080",
				DataDir:   "/tmp/data",
				DBPath:    "/tmp/data/db.sqlite",
				MediaRoot: "/media",
			},
			wantErr: false,
		},
		{
			name: "relative_data_dir",
			cfg: Config{
				Listen:    ":8080",
				DataDir:   "relative/path",
				DBPath:    "/tmp/db.sqlite",
				MediaRoot: "/media",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error=%v, wantErr=%v", err, tt.wantErr)
			}
		})
	}
}
