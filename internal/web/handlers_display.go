package web

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"hespera/internal/display"
)

// displayScale answers the client boot script's "which scale class is the
// display this window sits on?" — the auto display-scale read. x/y are the
// window's screenX/screenY on the virtual desktop; the answer comes from the
// physical size xrandr reports for the display containing that point (see
// internal/display). Empty class = unknown (server mode, no xrandr, headless)
// and the client keeps its current scale.
func (h *Handler) displayScale(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	class := ""
	// Only meaningful in app mode: in server mode the browser is a remote
	// machine, and matching it against the server's own displays would hand
	// every client the server's scale.
	if h.appMode {
		x, _ := strconv.Atoi(r.URL.Query().Get("x"))
		y, _ := strconv.Atoi(r.URL.Query().Get("y"))
		class = h.displayClassAt(r.Context(), x, y)
	}
	writeJSON(w, http.StatusOK, map[string]string{"class": class})
}

// Runtime display-mode control (Settings → Features).
//
// A TV that negotiates HDMI badly leaves the kiosk showing the wrong mode, and
// until now the only cure was editing the kernel command line on the boot
// partition and rebooting. These three endpoints let the mode be picked at
// runtime through xrandr — unprivileged, no reboot.
//
// The safety property is that a bad pick destroys the very screen you would use
// to undo it, and unlike the power button there is no physical gesture that
// recovers. So an applied mode is provisional: the server arms a revert and
// puts the screen back unless the change is confirmed inside the window. The
// timer lives here, not in the page, because the page is on the screen that
// just went dark — and a kiosk browser that crashes would otherwise strand the
// box in an unreadable mode until someone SSHes in.
//
// That also makes persistence safe. display_mode is written only by the keep
// endpoint, so a stored mode is by construction one a human confirmed they
// could see — which is what licenses re-applying it unattended at boot.
const (
	// displayRevertWindow is how long a change stays provisional. Long enough to
	// find and press a button on a TV from across a room; short enough that
	// waiting it out isn't a punishment when the screen went black.
	displayRevertWindow = 15 * time.Second
)

// The kiosk's X session is started by the autologin shell, which routinely
// comes up after this system service, so the boot-time re-apply waits for it —
// including for an output that isn't attached yet. Vars, not consts, so the
// tests don't have to sit through a real minute of polling.
var (
	displayBootWait = 60 * time.Second
	displayBootPoll = 2 * time.Second
)

// displayPending is an applied-but-unconfirmed mode change and the mode to put
// back if it isn't confirmed.
type displayPending struct {
	applied    string // the display_mode value that was applied
	prevOutput string
	prevMode   display.Mode
	timer      *time.Timer
}

// displayModes lists the outputs and modes the picker can offer, or says why
// this machine can't offer any. Loopback-gated for the same reason the power
// button is: a LAN device must never be shown a control aimed at the screen
// attached to the server.
func (h *Handler) displayModes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !isLoopbackRequest(r) {
		http.Error(w, "display settings are only available on the machine Hespera runs on", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	if !h.effectiveDisplayControlEnabled(ctx) {
		http.Error(w, "display control is turned off in Settings → Features", http.StatusForbidden)
		return
	}
	if ok, reason := h.displayAvailable(); !ok {
		writeJSON(w, http.StatusOK, map[string]any{"available": false, "reason": reason})
		return
	}
	outs, err := h.displayOutputs(ctx)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"available": false,
			"reason":    "Hespera couldn't read this machine's screens: " + err.Error(),
		})
		return
	}
	type modeJSON struct {
		Value     string `json:"value"`
		Label     string `json:"label"`
		Current   bool   `json:"current"`
		Preferred bool   `json:"preferred"`
	}
	type outputJSON struct {
		Name  string     `json:"name"`
		Modes []modeJSON `json:"modes"`
	}
	var rows []outputJSON
	for _, o := range outs {
		// A disconnected output can't be set at all — xrandr refuses, and
		// offering it would be the one option most likely to be picked by
		// someone whose screen is already dark.
		if !o.Connected || len(o.Modes) == 0 {
			continue
		}
		row := outputJSON{Name: o.Name}
		for _, m := range o.Modes {
			row.Modes = append(row.Modes, modeJSON{
				Value:     display.FormatSetting(o.Name, m),
				Label:     modeLabel(m),
				Current:   m.Current,
				Preferred: m.Preferred,
			})
		}
		rows = append(rows, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"available": true, "outputs": rows})
}

// modeLabel renders a mode for a human: "1920×1080 @ 60 Hz".
func modeLabel(m display.Mode) string {
	return strconv.Itoa(m.W) + "×" + strconv.Itoa(m.H) + " @ " +
		strconv.FormatFloat(m.Rate, 'g', -1, 64) + " Hz"
}

// displayModeApply switches the screen to the requested mode and arms the
// revert. It does not persist anything — see displayModeKeep.
func (h *Handler) displayModeApply(w http.ResponseWriter, r *http.Request) {
	if !h.allowLocalPost(w, r, "changing the display mode") {
		return
	}
	ctx := r.Context()
	if !h.effectiveDisplayControlEnabled(ctx) {
		http.Error(w, "display control is turned off in Settings → Features", http.StatusForbidden)
		return
	}
	if ok, reason := h.displayAvailable(); !ok {
		http.Error(w, reason, http.StatusServiceUnavailable)
		return
	}
	name, mode, ok := display.ParseSetting(r.FormValue("mode"))
	if !ok {
		http.Error(w, "that isn't a display mode Hespera recognises", http.StatusBadRequest)
		return
	}
	outs, err := h.displayOutputs(ctx)
	if err != nil {
		httpError(w, 500, "could not read this machine's screens", "display outputs failed", "handler", "displayModeApply", "err", err)
		return
	}
	out := findOutput(outs, name)
	if out == nil || !out.Connected {
		http.Error(w, "nothing is connected to "+name+" any more", http.StatusBadRequest)
		return
	}
	// Re-check the mode is still offered rather than trusting the page: the
	// picker's list was built when the card was opened, and a screen can be
	// swapped between then and now.
	if !out.Offers(mode) {
		http.Error(w, name+" doesn't offer that mode any more", http.StatusBadRequest)
		return
	}
	prev, ok := out.CurrentMode()
	if !ok {
		http.Error(w, "Hespera can't tell what mode "+name+" is in, so it can't undo a change to it", http.StatusConflict)
		return
	}

	applied := display.FormatSetting(name, mode)
	// Claim the pending slot before the xrandr call, so two quick presses can't
	// both apply and leave the second one's revert pointing at the first one's
	// mode instead of the original.
	h.displayMu.Lock()
	if h.displayPending != nil {
		h.displayMu.Unlock()
		http.Error(w, "another display change is still waiting to be confirmed", http.StatusConflict)
		return
	}
	h.displayPending = &displayPending{applied: applied, prevOutput: name, prevMode: prev}
	h.displayMu.Unlock()

	// Deliberately not r.Context(): changing the mode can disturb the browser
	// that asked for it, and a cancelled request must not kill xrandr halfway.
	if err := h.displaySetMode(context.Background(), name, mode); err != nil {
		h.clearDisplayPending(applied)
		slog.Error("display mode change failed", "output", name, "mode", applied, "err", err)
		http.Error(w, "could not change the display mode: "+err.Error(), http.StatusInternalServerError)
		return
	}

	h.displayMu.Lock()
	if p := h.displayPending; p != nil && p.applied == applied {
		p.timer = time.AfterFunc(displayRevertWindow, func() { h.revertDisplayMode(applied) })
	}
	h.displayMu.Unlock()

	slog.Info("display mode applied provisionally", "output", name, "mode", applied,
		"reverts_in", displayRevertWindow.String())
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":             true,
		"applied":        applied,
		"previous":       display.FormatSetting(name, prev),
		"revert_seconds": int(displayRevertWindow.Seconds()),
	})
}

// displayModeConfirm settles a provisional change: keep=1 disarms the revert
// and stores the mode as the one Hespera re-applies at every boot, keep=0 puts
// the old mode back immediately instead of waiting out the window.
//
// The mode is echoed back by the client and re-checked here, so a stale page
// confirming a change that has already been undone is refused rather than
// silently storing a mode nobody is looking at.
func (h *Handler) displayModeConfirm(w http.ResponseWriter, r *http.Request) {
	if !h.allowLocalPost(w, r, "confirming the display mode") {
		return
	}
	ctx := r.Context()
	if !h.effectiveDisplayControlEnabled(ctx) {
		http.Error(w, "display control is turned off in Settings → Features", http.StatusForbidden)
		return
	}
	applied := strings.TrimSpace(r.FormValue("mode"))
	if r.FormValue("keep") == "0" {
		h.revertDisplayMode(applied)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "reverted": applied})
		return
	}
	h.displayMu.Lock()
	p := h.displayPending
	if p == nil || p.applied != applied {
		h.displayMu.Unlock()
		http.Error(w, "that display change has already been undone", http.StatusConflict)
		return
	}
	if p.timer != nil {
		p.timer.Stop()
	}
	h.displayPending = nil
	h.displayMu.Unlock()

	if err := h.saveAPIKey(ctx, "display_mode", applied); err != nil {
		httpError(w, 500, "could not save the display mode", "save display_mode failed", "handler", "displayModeKeep", "err", err)
		return
	}
	slog.Info("display mode confirmed and saved", "mode", applied)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "saved": applied})
}

// clearDisplayPending drops the pending change if it is still the one named.
func (h *Handler) clearDisplayPending(applied string) {
	h.displayMu.Lock()
	if h.displayPending != nil && h.displayPending.applied == applied {
		h.displayPending = nil
	}
	h.displayMu.Unlock()
}

// revertDisplayMode puts the screen back after an unconfirmed change.
//
// The applied-mode check is the point: by the time a timer fires the change it
// was armed for may have been kept, or replaced by another. Reverting on the
// strength of a timer alone would undo a mode the user is happily watching —
// the same trap the player's Up Next countdown documents.
func (h *Handler) revertDisplayMode(applied string) {
	h.displayMu.Lock()
	p := h.displayPending
	if p == nil || p.applied != applied {
		h.displayMu.Unlock()
		return
	}
	h.displayPending = nil
	h.displayMu.Unlock()

	if err := h.displaySetMode(context.Background(), p.prevOutput, p.prevMode); err != nil {
		slog.Error("display mode revert failed", "output", p.prevOutput, "err", err)
		return
	}
	slog.Warn("display mode reverted — the change was never confirmed",
		"output", p.prevOutput, "restored", display.FormatSetting(p.prevOutput, p.prevMode))
}

// applySavedDisplayMode re-applies the confirmed mode after a restart, so a
// kiosk doesn't fall back to whatever X negotiated on every reboot. Runs in a
// goroutine from New: it waits for the X session, which on an autologin box
// starts after this service does.
//
// Two guards keep an unattended apply honest — there is nobody to confirm it
// here. Only a mode a human already confirmed is ever stored, and it is applied
// only if the output still advertises it, so a swapped screen is left alone
// rather than forced into a mode it never claimed to support.
func (h *Handler) applySavedDisplayMode() {
	ctx := context.Background()
	if !h.effectiveDisplayControlEnabled(ctx) {
		return
	}
	saved := h.effectiveDisplayMode(ctx)
	if saved == "" {
		return
	}
	name, mode, ok := display.ParseSetting(saved)
	if !ok {
		slog.Warn("saved display mode is not a mode Hespera recognises — ignoring", "value", saved)
		return
	}
	deadline := time.Now().Add(displayBootWait)
	for {
		if avail, _ := h.displayAvailable(); avail {
			if outs, err := h.displayOutputs(ctx); err == nil {
				if out := findOutput(outs, name); out != nil && out.Connected {
					if !out.Offers(mode) {
						slog.Warn("saved display mode is no longer offered — leaving the screen as it is",
							"output", name, "mode", saved)
						return
					}
					if err := h.displaySetMode(ctx, name, mode); err != nil {
						slog.Error("could not re-apply the saved display mode", "mode", saved, "err", err)
						return
					}
					slog.Info("re-applied the saved display mode", "mode", saved)
					return
				}
			}
		}
		if time.Now().After(deadline) {
			slog.Info("saved display mode not applied — no screen came up in time", "mode", saved)
			return
		}
		time.Sleep(displayBootPoll)
	}
}

// findOutput returns the named output, or nil.
func findOutput(outs []display.Output, name string) *display.Output {
	for i := range outs {
		if outs[i].Name == name {
			return &outs[i]
		}
	}
	return nil
}
