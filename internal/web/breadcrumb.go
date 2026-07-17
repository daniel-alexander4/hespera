package web

import "fmt"

// crumb is one hop in a page's breadcrumb trail. Every crumb is a link to an
// ancestor; the trail ends at the immediate parent (the current page is shown
// by the page's own <h1> right below). The layout renders `.Breadcrumb` above
// the content on every page that supplies one — in the content area (not the
// topbar), so it is visible and remote-focusable in couch mode too.
type crumb struct {
	Label string
	Href  string
}

// Shared roots so every trail spells the top-level sections identically.
var (
	bcHome       = crumb{"Home", "/"}
	bcMusic      = crumb{"Music", "/music"}
	bcTV         = crumb{"TV Shows", "/tv"}
	bcMovies     = crumb{"Movies", "/movies"}
	bcBooks      = crumb{"Books", "/books"}
	bcAudiobooks = crumb{"Audiobooks", "/audiobooks"}
	bcSettings   = crumb{"Settings", "/settings"}
)

// bcArtist builds the crumb linking to a local artist page.
func bcArtist(id int64, name string) crumb {
	return crumb{Label: name, Href: fmt.Sprintf("/music/artist/%d", id)}
}

// bcAlbum builds the crumb linking to a local album page.
func bcAlbum(id int64, title string) crumb {
	return crumb{Label: title, Href: fmt.Sprintf("/music/album/%d", id)}
}

// bcSeries builds the crumb linking to a local TV series page. The series id is
// the string TMDB id carried on tv_series_identities.series_id.
func bcSeries(id, name string) crumb {
	return crumb{Label: name, Href: "/tv/series/" + id}
}
