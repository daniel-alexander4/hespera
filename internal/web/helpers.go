package web

import (
	"fmt"
	"net/http"
	"path"
	"strconv"
	"strings"
)

// pathID extracts a positive int64 from the URL path after the given prefix.
// It applies path.Clean sanitization to prevent traversal. Returns a
// descriptive error for invalid, zero, or negative values.
func pathID(r *http.Request, prefix string) (int64, error) {
	seg := pathSegment(r, prefix)
	if seg == "" {
		return 0, fmt.Errorf("empty path segment after %q", prefix)
	}
	id, err := strconv.ParseInt(seg, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("non-numeric path segment %q: %w", seg, err)
	}
	if id <= 0 {
		return 0, fmt.Errorf("non-positive path id %d", id)
	}
	return id, nil
}

// pathSegment extracts a sanitized string segment from the URL path after the
// given prefix. It applies path.Clean sanitization and returns an empty string
// if nothing follows the prefix.
func pathSegment(r *http.Request, prefix string) string {
	s := strings.TrimPrefix(r.URL.Path, prefix)
	s = path.Clean("/" + s)
	s = strings.TrimPrefix(s, "/")
	return s
}
