package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Update check — the topbar version pill (nib's update.go, ported). It compares
// the running version to the newest published GitHub release and, when one is
// newer, reports the release asset matching this machine's OS/arch. It downloads
// nothing on its own and replaces nothing — the client navigates to the asset URL
// so the browser downloads it; installing is the user's step. `?auto=1` (the
// once-per-session startup check) respects the update_check_enabled toggle and
// answers without any network call when it's off; a bare request (the pill click)
// always checks.

// githubLatestURL is GitHub's "latest release" API for Hespera. A package var so
// tests can point it at a stub. No releases are published there yet — until they
// are, the check answers "no releases" and the pill stays in its unknown state.
var githubLatestURL = "https://api.github.com/repos/daniel-alexander4/hespera/releases/latest"

type updateResponse struct {
	Enabled     bool   `json:"enabled"` // false only on the auto path with the toggle off (no check ran)
	Current     string `json:"current"`
	Latest      string `json:"latest,omitempty"` // empty when no release is published yet
	Available   bool   `json:"updateAvailable"`
	URL         string `json:"url,omitempty"`         // release page
	DownloadURL string `json:"downloadUrl,omitempty"` // asset matching this OS/arch, if present
	Managed     bool   `json:"managed"`               // installed under a system path — the asset is a .deb, not a raw binary
}

func (h *Handler) updateCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	resp := updateResponse{Enabled: true, Current: h.version, Managed: managedInstall()}
	if r.URL.Query().Get("auto") == "1" && !h.effectiveUpdateCheckEnabled(r.Context()) {
		resp.Enabled = false
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
		return
	}
	rel, err := latestRelease(r.Context())
	if err != nil {
		jsonErr(w, http.StatusBadGateway, "could not reach the update server", "update check failed", "handler", "updateCheck", "err", err)
		return
	}
	if rel != nil {
		resp.Latest = strings.TrimPrefix(rel.Tag, "v")
		resp.URL = rel.URL
		resp.Available = versionLess(resp.Current, resp.Latest)
		if resp.Available {
			resp.DownloadURL = assetURL(runtime.GOOS, runtime.GOARCH, resp.Managed, rel.Assets)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

type release struct {
	Tag    string
	URL    string
	Assets []releaseAsset
}

type releaseAsset struct {
	Name string
	URL  string
}

// latestRelease returns Hespera's newest published release, or (nil, nil) when
// none exists yet (404 — which a not-yet-created repo also answers).
func latestRelease(ctx context.Context) (*release, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, githubLatestURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil // no releases published yet
	}
	if resp.StatusCode != http.StatusOK {
		return nil, errStatusCode(resp.StatusCode)
	}
	var raw struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
		Assets  []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&raw); err != nil {
		return nil, err
	}
	rel := &release{Tag: raw.TagName, URL: raw.HTMLURL}
	for _, a := range raw.Assets {
		rel.Assets = append(rel.Assets, releaseAsset{Name: a.Name, URL: a.URL})
	}
	return rel, nil
}

type errStatusCode int

func (e errStatusCode) Error() string { return "update server returned HTTP " + strconv.Itoa(int(e)) }

// assetURL picks the release asset for this OS/arch. Managed (dpkg) installs want
// the matching .deb (hespera_<ver>_<goarch>.deb); standalone installs want the raw
// binary (hespera-<ver>-<goos>-<goarch>[.exe]) — build.sh's exact naming. It
// matches on the os/arch tokens rather than reconstructing the full name, so the
// version part can drift — but the hespera prefix is required: releases also
// carry the hesplay client's .debs/binaries with the same _<arch>.deb and
// -<os>-<arch> tokens, and offering one of those would "update" a server into
// a music player. Empty when nothing matches (the client falls back to the
// release page).
func assetURL(goos, goarch string, managed bool, assets []releaseAsset) string {
	for _, a := range assets {
		if managed {
			if strings.HasPrefix(a.Name, "hespera_") && strings.HasSuffix(a.Name, "_"+goarch+".deb") {
				return a.URL
			}
			continue
		}
		if strings.HasPrefix(a.Name, "hespera-") && strings.Contains(a.Name, "-"+goos+"-"+goarch) && !strings.HasSuffix(a.Name, ".deb") {
			return a.URL
		}
	}
	return ""
}

// versionLess reports whether semver a < b, comparing major.minor.patch
// numerically. Missing or non-numeric parts count as 0, so "dev" sorts below
// any release.
func versionLess(a, b string) bool {
	pa, pb := parseVer(a), parseVer(b)
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			return pa[i] < pb[i]
		}
	}
	return false
}

func parseVer(v string) [3]int {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	for i, part := range strings.SplitN(v, ".", 3) {
		out[i], _ = strconv.Atoi(strings.TrimSpace(part))
	}
	return out
}

// managedInstall reports whether Hespera is running from a system path (the
// dpkg-installed /usr/bin/hespera), where updates come as a .deb rather than a
// raw binary.
func managedInstall() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return strings.HasPrefix(exe, "/usr/")
}
