package config

import (
	"os"
	"testing"
)

func TestFromEnvDefaults(t *testing.T) {
	// Clear any env vars that might interfere.
	for _, k := range []string{
		"HESPERA_LISTEN", "HESPERA_DATA_DIR", "HESPERA_DB_PATH",
		"HESPERA_MEDIA_ROOT", "AUTH_ENABLED", "AUTH_SESSION_SECRET",
	} {
		os.Unsetenv(k)
	}

	cfg := FromEnv()
	if cfg.Listen != ":8080" {
		t.Fatalf("expected Listen=:8080, got %q", cfg.Listen)
	}
	if cfg.DataDir != "/var/lib/hespera" {
		t.Fatalf("expected DataDir=/var/lib/hespera, got %q", cfg.DataDir)
	}
	if cfg.DBPath != "/var/lib/hespera/hespera.sqlite" {
		t.Fatalf("expected DBPath=/var/lib/hespera/hespera.sqlite, got %q", cfg.DBPath)
	}
	if cfg.MediaRoot != "/media" {
		t.Fatalf("expected MediaRoot=/media, got %q", cfg.MediaRoot)
	}
	if !cfg.AuthEnabled {
		t.Fatalf("expected AuthEnabled=true by default")
	}
	if cfg.FFmpegConcurrentLimit != 4 {
		t.Fatalf("expected FFmpegConcurrentLimit=4, got %d", cfg.FFmpegConcurrentLimit)
	}
}

func TestFromEnvCustom(t *testing.T) {
	os.Setenv("HESPERA_LISTEN", ":9090")
	os.Setenv("HESPERA_DATA_DIR", "/tmp/iso")
	os.Setenv("AUTH_ENABLED", "false")
	defer func() {
		os.Unsetenv("HESPERA_LISTEN")
		os.Unsetenv("HESPERA_DATA_DIR")
		os.Unsetenv("AUTH_ENABLED")
	}()

	cfg := FromEnv()
	if cfg.Listen != ":9090" {
		t.Fatalf("expected Listen=:9090, got %q", cfg.Listen)
	}
	if cfg.DataDir != "/tmp/iso" {
		t.Fatalf("expected DataDir=/tmp/iso, got %q", cfg.DataDir)
	}
	if cfg.AuthEnabled {
		t.Fatalf("expected AuthEnabled=false")
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
			name: "valid_with_auth",
			cfg: Config{
				Listen:            ":8080",
				DataDir:           "/tmp/data",
				DBPath:            "/tmp/data/db.sqlite",
				MediaRoot:         "/media",
				AuthEnabled:       true,
				AuthSessionSecret: "this-is-a-strong-secret-1234",
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
		{
			name: "auth_no_secret",
			cfg: Config{
				Listen:      ":8080",
				DataDir:     "/tmp/data",
				DBPath:      "/tmp/data/db.sqlite",
				MediaRoot:   "/media",
				AuthEnabled: true,
			},
			wantErr: true,
		},
		{
			name: "auth_weak_secret",
			cfg: Config{
				Listen:            ":8080",
				DataDir:           "/tmp/data",
				DBPath:            "/tmp/data/db.sqlite",
				MediaRoot:         "/media",
				AuthEnabled:       true,
				AuthSessionSecret: "changeme",
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

func TestIsWeakSessionSecret(t *testing.T) {
	if !isWeakSessionSecret("short") {
		t.Fatalf("expected short secret to be weak")
	}
	if !isWeakSessionSecret("changeme") {
		t.Fatalf("expected 'changeme' to be weak")
	}
	if isWeakSessionSecret("this-is-a-strong-secret-1234") {
		t.Fatalf("expected strong secret to not be weak")
	}
}
