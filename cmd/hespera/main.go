package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"hespera/internal/config"
	"hespera/internal/db"
	"hespera/internal/video"
	"hespera/internal/web"
)

// version is set at build time via -ldflags "-X main.version=…" (see build.sh);
// it stamps the startup log and the static-asset cache-buster.
var version = "dev"

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))
	slog.Info("starting", "version", version)

	cfg := config.FromEnv()
	if err := cfg.Validate(); err != nil {
		slog.Error("config validation failed", "err", err)
		os.Exit(1)
	}

	video.SetConcurrency(cfg.FFmpegConcurrentLimit, cfg.FFmpegAcquireTimeout)

	// Create the data dir on first run — the binary runs as the invoking user
	// (no container pre-creating /var/lib/hespera), so the default per-user dir
	// won't exist yet and SQLite can't create its file in a missing directory.
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		slog.Error("create data dir failed", "dir", cfg.DataDir, "err", err)
		os.Exit(1)
	}

	dbConn, err := db.Open(cfg.DBPath)
	if err != nil {
		slog.Error("db open failed", "err", err)
		os.Exit(1)
	}
	defer dbConn.Close()

	if err := db.Migrate(dbConn); err != nil {
		slog.Error("db migrate failed", "err", err)
		os.Exit(1)
	}

	h, err := web.New(web.Deps{
		Cfg:     cfg,
		DB:      dbConn,
		Version: version,
	})
	if err != nil {
		slog.Error("web handler initialization failed", "err", err)
		os.Exit(1)
	}

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           h.Router(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		slog.Info("listening", "addr", cfg.Listen)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("listen failed", "err", err)
			os.Exit(1)
		}
	}()

	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		slog.Warn("http shutdown", "err", err)
	}
	h.Shutdown()
	slog.Info("shutdown complete")
}
