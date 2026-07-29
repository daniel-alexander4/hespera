package web

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

// powerOffTimeout bounds the systemctl call. The request is a D-Bus hand-off to
// logind that returns as soon as the job is enqueued, so this only ever fires
// when something is genuinely wedged — it must not leave the handler hanging.
const powerOffTimeout = 10 * time.Second

// systemctlPowerOff halts the machine through systemd.
//
// Hespera runs unprivileged (the shipped unit is User=%i), and the service has
// no active login session, so logind's allow_active path does not apply: this
// succeeds only where the operator has granted the polkit action
// org.freedesktop.login1.power-off to the server's user. That grant is
// deliberately NOT shipped in the .deb — installing a rule that lets the web UI
// halt its host would be a silent privilege escalation on every machine Hespera
// is installed on. Without it systemctl exits non-zero and the caller reports
// the failure rather than leaving a dead button.
func systemctlPowerOff() error {
	ctx, cancel := context.WithTimeout(context.Background(), powerOffTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemctl", "poweroff").CombinedOutput()
	if err != nil {
		if detail := strings.TrimSpace(string(out)); detail != "" {
			return fmt.Errorf("systemctl poweroff: %w: %s", err, detail)
		}
		return fmt.Errorf("systemctl poweroff: %w", err)
	}
	return nil
}

// poweroff halts the machine Hespera runs on (the home screen's power button).
//
// Distinct from /shutdown, which stops this process and leaves the box running.
// Powering off the machine is the point on an appliance-style install — a TV box
// with no keyboard, where the only other way to stop it is pulling the plug.
// Going through systemd means the service is stopped, the database flushed and
// the library filesystem unmounted cleanly on the way down.
//
// Gated by the same opt-in setting that renders the button, so a stale page (or
// a curl on the box) can't halt an install whose owner never enabled it.
//
// Deliberately synchronous, unlike /shutdown's respond-then-quit: the one error
// that matters — a missing privilege grant — is invisible if we answer 200 and
// fail in the background, and a success needs no reply, since the screen is
// about to go dark either way.
func (h *Handler) poweroff(w http.ResponseWriter, r *http.Request) {
	if !h.allowLocalPost(w, r, "power off") {
		return
	}
	if !h.effectivePowerButtonEnabled(r.Context()) {
		http.Error(w, "the power button is turned off in Settings → Features", http.StatusForbidden)
		return
	}
	if h.powerOff == nil {
		http.Error(w, "power off not available", http.StatusServiceUnavailable)
		return
	}
	if err := h.powerOff(); err != nil {
		slog.Error("poweroff failed", "err", err)
		http.Error(w, "could not power off: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("powering off"))
}
