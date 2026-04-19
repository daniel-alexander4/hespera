package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"isomedia/internal/jobs"
	"isomedia/internal/scan"
)

func (h *Handler) settings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	h.render(w, "settings.html", map[string]any{
		"Title": "Settings",
	})
}

func (h *Handler) settingsJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	jobList, err := h.loadScanJobs(r.Context(), "", "", 0, 50)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.render(w, "settings_jobs.html", map[string]any{
		"Title": "Jobs",
		"Jobs":  jobList,
	})
}

func (h *Handler) settingsJobsJSON(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	status := strings.TrimSpace(r.URL.Query().Get("status"))
	jobType := strings.TrimSpace(r.URL.Query().Get("job_type"))
	jobIDStr := strings.TrimSpace(r.URL.Query().Get("job_id"))
	var jobID int64
	if jobIDStr != "" {
		v, err := strconv.ParseInt(jobIDStr, 10, 64)
		if err == nil && v > 0 {
			jobID = v
		}
	}
	limit := 50
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 && v <= 200 {
		limit = v
	}

	jobList, err := h.loadScanJobs(r.Context(), status, jobType, jobID, limit)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":   true,
		"data": jobList,
	})
}

func (h *Handler) settingsJobsCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	jobID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("job_id")), 10, 64)
	if err != nil || jobID <= 0 {
		jsonError(w, "invalid job_id", http.StatusBadRequest)
		return
	}
	if err := h.jobs.RequestCancel(jobID); err != nil {
		if errors.Is(err, jobs.ErrJobNotFound) {
			jsonError(w, "job not found", http.StatusNotFound)
			return
		}
		if errors.Is(err, jobs.ErrJobNotCancel) {
			jsonError(w, "job is not cancelable", http.StatusBadRequest)
			return
		}
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "message": "cancel requested"})
}

type scanJobRow struct {
	ID              int64  `json:"id"`
	LibraryID       int64  `json:"library_id"`
	JobType         string `json:"job_type"`
	Status          string `json:"status"`
	ProgressCurrent int64  `json:"progress_current"`
	ProgressTotal   int64  `json:"progress_total"`
	DurationMS      int64  `json:"duration_ms"`
	CancelRequested bool   `json:"cancel_requested"`
	CreatedAt       string `json:"created_at"`
	StartedAt       string `json:"started_at"`
	EndedAt         string `json:"ended_at"`
	Error           string `json:"error"`
}

func (h *Handler) loadScanJobs(ctx context.Context, status, jobType string, jobID int64, limit int) ([]scanJobRow, error) {
	query := `SELECT id, library_id, job_type, status, progress_current, progress_total, duration_ms, cancel_requested, created_at, started_at, ended_at, error FROM scan_jobs`
	var conditions []string
	var args []any

	if status != "" {
		conditions = append(conditions, "status=?")
		args = append(args, status)
	}
	if jobType != "" {
		conditions = append(conditions, "job_type=?")
		args = append(args, jobType)
	}
	if jobID > 0 {
		conditions = append(conditions, "id=?")
		args = append(args, jobID)
	}
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	query += " ORDER BY id DESC LIMIT ?"
	args = append(args, limit)

	rows, err := h.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]scanJobRow, 0, limit)
	for rows.Next() {
		var j scanJobRow
		var cancelReq int
		if err := rows.Scan(&j.ID, &j.LibraryID, &j.JobType, &j.Status, &j.ProgressCurrent, &j.ProgressTotal, &j.DurationMS, &cancelReq, &j.CreatedAt, &j.StartedAt, &j.EndedAt, &j.Error); err != nil {
			return nil, err
		}
		j.CancelRequested = cancelReq != 0
		out = append(out, j)
	}
	return out, rows.Err()
}

// Library management handlers

type libraryRow struct {
	ID       int64
	Name     string
	Type     string
	RootPath string
}

func (h *Handler) libraries(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	libs, err := h.loadLibraryList(r.Context())
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	h.render(w, "libraries.html", map[string]any{
		"Title":     "Libraries",
		"Libraries": libs,
		"MediaRoot": h.cfg.MediaRoot,
	})
}

func (h *Handler) librariesNew(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.render(w, "libraries_new.html", map[string]any{
			"Title":     "New Library",
			"MediaRoot": h.cfg.MediaRoot,
		})
	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		name := strings.TrimSpace(r.FormValue("name"))
		libType := strings.TrimSpace(r.FormValue("type"))
		root := strings.TrimSpace(r.FormValue("root_path"))

		if name == "" || libType == "" || root == "" {
			http.Error(w, "name, type, root_path are required", 400)
			return
		}
		if !validLibraryType(libType) {
			http.Error(w, "invalid type", 400)
			return
		}
		if !strings.HasPrefix(root, h.cfg.MediaRoot+"/") && root != h.cfg.MediaRoot {
			http.Error(w, fmt.Sprintf("root_path must be under %s", h.cfg.MediaRoot), 400)
			return
		}
		_, err := h.db.ExecContext(r.Context(),
			"INSERT INTO libraries (name, type, root_path) VALUES (?, ?, ?)",
			name, libType, root,
		)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		http.Redirect(w, r, "/libraries", http.StatusSeeOther)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (h *Handler) librariesScan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	idStr := strings.TrimSpace(r.FormValue("id"))
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid id", 400)
		return
	}

	scanner := scan.New(h.cfg, h.db)
	jobID, err := h.jobs.Enqueue("scan", id, "user", func(ctx context.Context, jobID, libraryID int64) error {
		return scanner.ScanMusic(ctx, jobID, libraryID)
	})
	if err != nil {
		if errors.Is(err, jobs.ErrQueueFull) {
			if requestWantsJSON(r) {
				jsonError(w, err.Error(), http.StatusServiceUnavailable)
				return
			}
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		if requestWantsJSON(r) {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if requestWantsJSON(r) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":      true,
			"message": "scan queued",
			"data":    map[string]any{"library_id": id, "job_id": jobID},
		})
		return
	}
	http.Redirect(w, r, "/libraries", http.StatusSeeOther)
}

func (h *Handler) librariesDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	id, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("id")), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	var name string
	if err := h.db.QueryRowContext(r.Context(), "SELECT name FROM libraries WHERE id=?", id).Scan(&name); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if _, err := h.db.ExecContext(r.Context(), "DELETE FROM libraries WHERE id=?", id); err != nil {
		if requestWantsJSON(r) {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if requestWantsJSON(r) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":      true,
			"message": "Library deleted.",
			"data":    map[string]any{"library_id": id, "name": name},
		})
		return
	}
	http.Redirect(w, r, "/libraries", http.StatusSeeOther)
}

func (h *Handler) loadLibraryList(ctx context.Context) ([]libraryRow, error) {
	rows, err := h.db.QueryContext(ctx,
		"SELECT id, name, type, root_path FROM libraries ORDER BY id DESC",
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var libs []libraryRow
	for rows.Next() {
		var x libraryRow
		if err := rows.Scan(&x.ID, &x.Name, &x.Type, &x.RootPath); err != nil {
			return nil, err
		}
		libs = append(libs, x)
	}
	return libs, rows.Err()
}

func validLibraryType(v string) bool {
	switch v {
	case "music", "movies", "tv", "photos", "home_videos":
		return true
	default:
		return false
	}
}

func formatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	m := int(d.Minutes())
	s := int(d.Seconds()) - m*60
	return fmt.Sprintf("%dm%ds", m, s)
}
