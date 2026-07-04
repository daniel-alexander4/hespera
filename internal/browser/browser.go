// Package browser opens Hespera's UI in a chromeless app-mode window.
//
// We prefer a Chromium-family browser's --app mode, which gives a dedicated,
// address-bar-free window that looks like a native desktop app while reusing an
// engine that's already installed. On macOS/Windows, if none is found we fall
// back to opening the URL as an ordinary tab in the default browser (Safari has
// no app mode, so a Safari-only Mac takes the tab fallback — by design; Windows
// always has Edge, so its fallback is theoretical). On Linux there is NO
// fallback: the app window is Chromium-family or nothing — falling back to
// xdg-open handed the window to whatever the default browser was (Firefox, with
// none of the app chrome), which is worse than an error telling the user to
// install chromium or browse to the printed URL.
package browser

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// fileExists reports whether an absolute path is a regular, runnable file.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// Open launches url as an app-mode window, or a default-browser tab if no
// app-mode-capable browser is found. It returns the started command (already
// running) so the caller can wait on it — or stop it to close the window — or an
// error if nothing could launch.
//
// userDataDir, when non-empty, runs the app window in a dedicated browser profile.
// That forces a NEW browser process that OWNS the window instead of delegating to
// an already-running Chrome and exiting immediately — so the caller can reliably
// close the window on quit by stopping the returned process. Empty → shared
// default profile (the window may then be owned by an existing instance and not
// independently closable).
func Open(url, userDataDir string) (*exec.Cmd, error) {
	if path, ok := findChromium(); ok {
		appArgs := []string{"--app=" + url, "--new-window"}
		if userDataDir != "" {
			// A unique profile guarantees this launch owns its window; the extra
			// flags keep the fresh profile from showing first-run/setup prompts.
			appArgs = append(appArgs,
				"--user-data-dir="+userDataDir,
				"--no-first-run",
				"--no-default-browser-check")
		}
		if runtime.GOOS == "linux" {
			// Set a stable WM_CLASS so the panel can match this window to
			// hespera.desktop (StartupWMClass=Hespera) and show the themed icon
			// instead of the small icon Chromium derives from the page favicon.
			appArgs = append(appArgs, "--class=Hespera")
		}
		cmd := exec.Command(path, appArgs...)
		if err := cmd.Start(); err == nil {
			return cmd, nil
		}
		// fall through to the tab fallback if the app-mode launch failed
	}

	if runtime.GOOS == "linux" {
		return nil, errors.New("no Chromium-family browser found (install google-chrome or chromium for the app window)")
	}
	name, args := tabOpener(url)
	cmd := exec.Command(name, args...)
	return cmd, cmd.Start()
}

// findChromium returns the first Chromium-family browser found for the OS.
func findChromium() (string, bool) {
	for _, c := range chromiumCandidates() {
		if path, err := exec.LookPath(c); err == nil {
			return path, true
		}
		if fileExists(c) { // absolute paths (macOS .app bundles)
			return c, true
		}
	}
	return "", false
}

// Find reports the Chromium-family browser Hespera would launch for the app
// window — its display name (e.g. "Google Chrome", "Chromium") and executable
// path — or ok=false when none is installed. It's the same discovery Open uses,
// exported for the About page's health panel (which browser hosts the window).
func Find() (name, path string, ok bool) {
	path, ok = findChromium()
	if !ok {
		return "", "", false
	}
	return displayName(path), path, true
}

// Version runs `<browser> --version` and returns the reported version string
// (e.g. "Google Chrome 149.0.7827.200" → "149.0.7827.200"), or "" if it can't
// be determined. Cheap and pure (no profile, no window), so no semaphore.
func Version(path string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		return ""
	}
	if m := browserVersionRe.FindString(string(out)); m != "" {
		return m
	}
	return ""
}

var browserVersionRe = regexp.MustCompile(`\d+\.\d+(?:\.\d+)*`)

// displayName derives a human name from a browser executable path. The base
// name is stable across OSes (google-chrome, chromium, "Google Chrome" bundle).
func displayName(path string) string {
	base := strings.ToLower(filepath.Base(path))
	switch {
	case strings.Contains(base, "google chrome"), strings.Contains(base, "google-chrome"):
		return "Google Chrome"
	case strings.Contains(base, "chromium"):
		return "Chromium"
	case strings.Contains(base, "edge"), strings.Contains(base, "msedge"):
		return "Microsoft Edge"
	case strings.Contains(base, "brave"):
		return "Brave"
	default:
		return filepath.Base(path)
	}
}

// chromiumCandidates lists browser binaries to try, per OS.
func chromiumCandidates() []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
		}
	case "windows":
		return []string{
			`C:\Program Files\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
			"chrome.exe", "msedge.exe",
		}
	default: // linux and friends
		return []string{
			"google-chrome", "google-chrome-stable", "chromium", "chromium-browser",
			"microsoft-edge", "brave-browser",
		}
	}
}

// tabOpener returns the OS command that opens a URL in the default browser.
func tabOpener(url string) (string, []string) {
	switch runtime.GOOS {
	case "darwin":
		return "open", []string{url}
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler", url}
	default:
		return "xdg-open", []string{url}
	}
}
