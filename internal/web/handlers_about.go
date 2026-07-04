package web

import (
	"context"
	"encoding/json"
	"net/http"

	"hespera/internal/browser"
	"hespera/internal/video"
)

// About-page health panel: version/health of the three things Hespera's
// experience leans on — ffmpeg (server-side, it does the transcoding) and the
// app-window browser (app mode only; in server mode the browser is on the
// viewer's own device, which the server can't and shouldn't probe). The
// Hespera row itself is filled client-side by reusing the existing
// /update/check endpoint (the topbar pill's data), so there is no version
// logic duplicated here and this endpoint makes no network call. Hit only when
// the About card is opened (a client fetch), so the fresh subprocess probes
// are off the common settings load and off every hot path.

// ffmpegMinMajor is the recommended ffmpeg major version. The one concrete
// version-sensitive feature is iPhone tile-grid HEIC decode (ffmpeg 7+);
// everything else works on 4+. Below this the panel recommends (not demands)
// an upgrade, naming that specific benefit.
const ffmpegMinMajor = 7

type healthRow struct {
	Status  string `json:"status"` // ok | warn | missing | na
	Version string `json:"version,omitempty"`
	Name    string `json:"name,omitempty"`   // browser display name
	Detail  string `json:"detail"`           // the short "why" shown under the row
}

type aboutHealth struct {
	FFmpeg healthRow `json:"ffmpeg"`
	Chrome healthRow `json:"chrome"`
}

func (h *Handler) aboutHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	out := aboutHealth{
		FFmpeg: ffmpegHealth(r.Context()),
		Chrome: h.chromeHealth(),
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(out)
}

func ffmpegHealth(ctx context.Context) healthRow {
	present, version, major := video.FFmpegInfo(ctx)
	switch {
	case !present:
		return healthRow{
			Status: "missing",
			Detail: "ffmpeg isn't on the PATH. It's required for TV and movie playback (transcoding), video thumbnails, and audio analysis. Music and metadata still work without it.",
		}
	case major > 0 && major < ffmpegMinMajor:
		return healthRow{
			Status:  "warn",
			Version: version,
			Detail:  "Works for TV, movies, and music. Upgrading to ffmpeg 7 or newer adds decoding for iPhone tile-grid HEIC photos, which older builds can't render.",
		}
	default:
		return healthRow{
			Status:  "ok",
			Version: version,
			Detail:  "Handles TV/movie transcoding, thumbnails, and audio analysis. Up to date for every format Hespera uses.",
		}
	}
}

func (h *Handler) chromeHealth() healthRow {
	// In server mode the app window doesn't run here — the viewer's browser is
	// on another device, which the server can't inspect. Report that honestly
	// rather than probing the wrong machine.
	if !h.appMode {
		return healthRow{
			Status: "na",
			Detail: "You're viewing Hespera from another device, so its browser is up to that device. On the machine running Hespera, the app window uses a Chromium-family browser.",
		}
	}
	name, path, ok := browser.Find()
	if !ok {
		return healthRow{
			Status: "missing",
			Detail: "No Chromium-family browser (Chrome, Chromium, Edge, or Brave) was found. Hespera needs one for its app window — install Chromium or Chrome, or open Hespera in a browser tab.",
		}
	}
	return healthRow{
		Status:  "ok",
		Name:    name,
		Version: browser.Version(path),
		Detail:  "Hosts Hespera's app window and plays your media. Keeping it current keeps codec and playback support up to date.",
	}
}
