package web

import (
	"net/http"
	"strconv"
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
