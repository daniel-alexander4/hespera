package config

import (
	"errors"
	"fmt"
	"os"
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
	TVHLSCacheMaxBytes    int64
	TVCacheMaxAge         time.Duration
}

func FromEnv() Config {
	listen := getenv("HESPERA_LISTEN", ":8080")
	dataDir := getenv("HESPERA_DATA_DIR", "/var/lib/hespera")
	dbPath := getenv("HESPERA_DB_PATH", dataDir+"/hespera.sqlite")
	mediaRoot := getenv("HESPERA_MEDIA_ROOT", "/media")

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
		AuthEnabled: parseBoolDefaultTrue(
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
	if !strings.HasPrefix(c.DataDir, "/") {
		return fmt.Errorf("HESPERA_DATA_DIR must be absolute: %q", c.DataDir)
	}
	if !strings.HasPrefix(c.DBPath, "/") {
		return fmt.Errorf("HESPERA_DB_PATH must be absolute: %q", c.DBPath)
	}
	if !strings.HasPrefix(c.MediaRoot, "/") {
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

func parseBoolDefaultTrue(v string) bool {
	v = strings.TrimSpace(strings.ToLower(v))
	switch v {
	case "", "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return true
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
