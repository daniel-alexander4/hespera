package web

import (
	"net/http"
	"strconv"
)

const (
	// listPageSize is how many cards a paginated browse list shows per page.
	listPageSize = 60
	// reviewListCap bounds the match-review backlog pages (worked top-down and
	// reloaded, so they cap-with-count rather than paginate).
	reviewListCap = 200
)

// pageNav is the per-list pagination state handed to the `pagination` partial.
// PrevPage/NextPage are precomputed so the template needs no arithmetic helpers.
type pageNav struct {
	Page       int
	TotalPages int
	PrevPage   int
	NextPage   int
	HasPrev    bool
	HasNext    bool
	BasePath   string // e.g. "/music/albums"
}

// pageParam reads the 1-based ?page= query param, defaulting to 1.
func pageParam(r *http.Request) int {
	if p, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && p > 1 {
		return p
	}
	return 1
}

// paginate clamps the requested page against the total row count and returns the
// nav state plus the SQL OFFSET.
func paginate(page, total int, basePath string) (nav pageNav, offset int) {
	totalPages := (total + listPageSize - 1) / listPageSize
	if totalPages < 1 {
		totalPages = 1
	}
	if page > totalPages {
		page = totalPages
	}
	if page < 1 {
		page = 1
	}
	return pageNav{
		Page:       page,
		TotalPages: totalPages,
		PrevPage:   page - 1,
		NextPage:   page + 1,
		HasPrev:    page > 1,
		HasNext:    page < totalPages,
		BasePath:   basePath,
	}, (page - 1) * listPageSize
}
