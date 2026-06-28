package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
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
	Query      string // preserved extra query (e.g. "q=foo"), so page links keep the filter
}

// searchBox is the data the `searchForm` partial renders: the form's action path
// and the current query (to prefill the input and drive the Clear link).
type searchBox struct {
	Action string
	Q      string
}

// pageParam reads the 1-based ?page= query param, defaulting to 1.
func pageParam(r *http.Request) int {
	if p, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && p > 1 {
		return p
	}
	return 1
}

// searchParam reads the ?q= browse-list filter, trimmed.
func searchParam(r *http.Request) string {
	return strings.TrimSpace(r.URL.Query().Get("q"))
}

// likeContains turns a search term into a case-insensitive LIKE pattern for a
// lower(col) LIKE ? contains-match. Empty for an empty term.
func likeContains(q string) string {
	if q == "" {
		return ""
	}
	return "%" + strings.ToLower(q) + "%"
}

// paginate clamps the requested page against the total row count and returns the
// nav state plus the SQL OFFSET. total is the full (filtered, when a ?q= search
// is active) count. For a filtered list, chain withQuery so prev/next keep the
// active filter.
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

// withQuery preserves an active ?q= search term in the page-link query, so a
// paginated, filtered list's prev/next keep the filter. No-op for an empty term.
func (n pageNav) withQuery(q string) pageNav {
	if q != "" {
		n.Query = url.Values{"q": {q}}.Encode()
	}
	return n
}
