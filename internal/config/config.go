package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Listen    string
	DataDir   string
	DBPath    string
	MediaRoot string

	TMDBAPIKey             string
	FanartTVAPIKey         string
	TheAudioDBAPIKey       string
	OpenSubtitlesAPIKey    string
	OpenSubtitlesUserAgent string
	YouTubeAPIKey          string

	AuthEnabled       bool
	AuthSessionSecret string
	SSHAuthNamespace  string
	SSHKeygenPath     string

	FFmpegConcurrentLimit int
	FFmpegAcquireTimeout  time.Duration
	HLSSegmentConcurrency int
	TVHLSCacheMaxBytes    int64
	TVCacheMaxAge         time.Duration
}

func FromEnv() Config {
	listen := getenv("HESPERA_LISTEN", ":8080")
	dataDir := getenv("HESPERA_DATA_DIR", defaultDataDir())
	dbPath := getenv("HESPERA_DB_PATH", filepath.Join(dataDir, "hespera.sqlite"))
	mediaRoot := getenv("HESPERA_MEDIA_ROOT", defaultMediaRoot())

	return Config{
		Listen:                 listen,
		DataDir:                dataDir,
		DBPath:                 dbPath,
		MediaRoot:              mediaRoot,
		TMDBAPIKey:             getenv("HESPERA_TMDB_API_KEY", ""),
		FanartTVAPIKey:         getenv("HESPERA_FANARTTV_API_KEY", ""),
		TheAudioDBAPIKey:       getenv("HESPERA_THEAUDIODB_API_KEY", ""),
		OpenSubtitlesAPIKey:    getenv("HESPERA_OPENSUBTITLES_API_KEY", ""),
		OpenSubtitlesUserAgent: getenv("HESPERA_OPENSUBTITLES_USER_AGENT", ""),
		YouTubeAPIKey:          getenv("HESPERA_YOUTUBE_API_KEY", ""),
		AuthEnabled: parseBoolDefaultFalse(
			os.Getenv("AUTH_ENABLED"),
		),
		AuthSessionSecret: getenv("AUTH_SESSION_SECRET", ""),
		SSHAuthNamespace:  getenv("SSH_AUTH_NAMESPACE", "hespera"),
		SSHKeygenPath:     getenv("SSH_KEYGEN_PATH", "ssh-keygen"),
		FFmpegConcurrentLimit: parsePositiveIntDefault(
			os.Getenv("HESPERA_FFMPEG_CONCURRENCY"), 4,
		),
		FFmpegAcquireTimeout: parseDurationDefault(
			os.Getenv("HESPERA_FFMPEG_ACQUIRE_TIMEOUT"), 2*time.Second,
		),
		HLSSegmentConcurrency: parsePositiveIntDefault(
			os.Getenv("HESPERA_HLS_SEGMENT_CONCURRENCY"), 1,
		),
		TVHLSCacheMaxBytes: parsePositiveInt64Default(
			os.Getenv("HESPERA_TV_HLS_CACHE_MAX_BYTES"), 20<<30,
		),
		TVCacheMaxAge: parseDurationDefault(
			os.Getenv("HESPERA_TV_CACHE_MAX_AGE"), 72*time.Hour,
		),
	}
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.Listen) == "" {
		return errors.New("HESPERA_LISTEN is required")
	}
	if !filepath.IsAbs(c.DataDir) {
		return fmt.Errorf("HESPERA_DATA_DIR must be absolute: %q", c.DataDir)
	}
	if !filepath.IsAbs(c.DBPath) {
		return fmt.Errorf("HESPERA_DB_PATH must be absolute: %q", c.DBPath)
	}
	if !filepath.IsAbs(c.MediaRoot) {
		return fmt.Errorf("HESPERA_MEDIA_ROOT must be absolute: %q", c.MediaRoot)
	}
	if c.AuthEnabled && strings.TrimSpace(c.AuthSessionSecret) == "" {
		return errors.New("AUTH_SESSION_SECRET is required when AUTH_ENABLED=true")
	}
	if c.AuthEnabled && isWeakSessionSecret(c.AuthSessionSecret) {
		return errors.New("AUTH_SESSION_SECRET must be a non-placeholder value and at least 16 characters")
	}
	if c.FFmpegConcurrentLimit < 0 {
		return errors.New("HESPERA_FFMPEG_CONCURRENCY must be >= 0")
	}
	if c.HLSSegmentConcurrency < 0 {
		return errors.New("HESPERA_HLS_SEGMENT_CONCURRENCY must be >= 0")
	}
	if c.FFmpegAcquireTimeout < 0 {
		return errors.New("HESPERA_FFMPEG_ACQUIRE_TIMEOUT must be >= 0")
	}
	if c.TVHLSCacheMaxBytes < 0 {
		return errors.New("HESPERA_TV_HLS_CACHE_MAX_BYTES must be >= 0")
	}
	if c.TVCacheMaxAge < 0 {
		return errors.New("HESPERA_TV_CACHE_MAX_AGE must be >= 0")
	}
	return nil
}

func isWeakSessionSecret(v string) bool {
	s := strings.TrimSpace(strings.ToLower(v))
	if len(s) < 16 {
		return true
	}
	switch s {
	case "change-me", "changeme", "default", "secret", "password":
		return true
	default:
		return false
	}
}

func getenv(k, def string) string {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	return v
}

// defaultDataDir is a per-user, OS-appropriate, writable location for the SQLite
// DB, caches, and downloaded art — used when HESPERA_DATA_DIR is unset. The
// binary runs as the invoking user (no container, no root), so the old
// root-owned /var/lib/hespera default would be unwritable. Resolves to an
// absolute path on every platform (os.UserConfigDir → ~/.config, %AppData%,
// ~/Library/Application Support); the Docker image is unaffected because its
// compose sets HESPERA_DATA_DIR explicitly.
func defaultDataDir() string {
	if d, err := os.UserConfigDir(); err == nil && d != "" {
		return filepath.Join(d, "hespera")
	}
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return filepath.Join(h, ".hespera")
	}
	return filepath.Join(os.TempDir(), "hespera")
}

// defaultMediaRoot is the fallback media library root. There is no universal
// media location, so this just needs to be an absolute, valid path the server
// can boot with; the user points HESPERA_MEDIA_ROOT at their actual media. The
// home directory is absolute on every platform and a sane starting point.
func defaultMediaRoot() string {
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return h
	}
	return os.TempDir()
}

// parseBoolDefaultFalse parses an AUTH_ENABLED-style flag, defaulting to false
// when unset. Hespera ships as a loopback-only single-machine app where auth
// adds no protection (only localhost can connect), so the binary boots open by
// default; a user exposing it (or running the Docker server) opts in via the
// Settings toggle or AUTH_ENABLED=true. Docker's compose sets AUTH_ENABLED
// explicitly, so this default doesn't change its behavior.
func parseBoolDefaultFalse(v string) bool {
	v = strings.TrimSpace(strings.ToLower(v))
	switch v {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func parsePositiveIntDefault(v string, def int) int {
	raw := strings.TrimSpace(v)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

func parsePositiveInt64Default(v string, def int64) int64 {
	raw := strings.TrimSpace(v)
	if raw == "" {
		return def
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

func parseDurationDefault(v string, def time.Duration) time.Duration {
	raw := strings.TrimSpace(v)
	if raw == "" {
		return def
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return def
	}
	return d
}
