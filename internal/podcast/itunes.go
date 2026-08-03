package podcast

// Apple's iTunes Search API — the discovery half of a podcast explorer.
//
// Keyless and public, which is why it is first: it needs no account, no secret
// and no settings entry, so search works on a fresh install with nothing
// configured. Apple's directory is also the one most podcasters actually submit
// to, so its coverage is the closest thing to a default.
//
// Unlike a feed, this host is FIXED — itunes.apple.com, compiled in, exactly
// like TMDB or MusicBrainz. It still goes through the same guarded client,
// which costs nothing and means there is only one way out of this package.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
)

// itunesSearchURL is a package var so tests can point it at a stub — the
// githubLatestURL / labsURL seam this codebase already uses for fixed
// providers.
var itunesSearchURL = "https://itunes.apple.com/search"

// directoryLimit bounds a result page. Apple will return 200; a wall of 200
// shows is not a page anyone reads, and every row carries an image the browser
// then hotlinks.
const directoryLimit = 24

// maxDirectoryBytes bounds the JSON response. Apple is trustworthy, but a
// response body is still a response body.
const maxDirectoryBytes = 4 << 20

// DirectoryResult is one show from a directory search, reduced to what the
// explorer renders and what subscribing needs.
type DirectoryResult struct {
	Name     string
	Author   string
	FeedURL  string
	ImageURL string
	Episodes int
	Genre    string
}

// itunesResponse is the wire shape. Apple sends far more per row; naming only
// what is used means a field they add or drop never breaks the parse.
type itunesResponse struct {
	Results []struct {
		CollectionName string   `json:"collectionName"`
		ArtistName     string   `json:"artistName"`
		FeedURL        string   `json:"feedUrl"`
		Artwork600     string   `json:"artworkUrl600"`
		Artwork100     string   `json:"artworkUrl100"`
		TrackCount     int      `json:"trackCount"`
		Genres         []string `json:"genres"`
	} `json:"results"`
}

// SearchDirectory searches Apple's podcast directory.
//
// A row with no feedUrl is dropped: Apple returns some entries that exist in
// the store but expose no feed, and a Subscribe button that cannot work is
// worse than an absent row.
func (c *Client) SearchDirectory(ctx context.Context, term string) ([]DirectoryResult, error) {
	term = strings.TrimSpace(term)
	if term == "" {
		return nil, errors.New("enter something to search for")
	}
	q := url.Values{
		"media": {"podcast"},
		"term":  {term},
		"limit": {fmt.Sprint(directoryLimit)},
	}
	resp, err := c.Get(ctx, itunesSearchURL+"?"+q.Encode(), "")
	if err != nil {
		return nil, fmt.Errorf("podcast directory: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDirectoryBytes+1))
	if err != nil {
		return nil, fmt.Errorf("podcast directory: %w", err)
	}
	if len(body) > maxDirectoryBytes {
		return nil, errors.New("podcast directory returned an implausibly large response")
	}

	var out itunesResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("podcast directory returned something unreadable: %w", err)
	}

	results := make([]DirectoryResult, 0, len(out.Results))
	for _, r := range out.Results {
		feed := strings.TrimSpace(r.FeedURL)
		if feed == "" {
			continue
		}
		// Held to the same rule as a hand-typed URL. Apple is trusted, but the
		// feed address is still third-party data and this is the one place it
		// enters the system.
		clean, err := ValidateURL(feed)
		if err != nil {
			continue
		}
		name := strings.TrimSpace(r.CollectionName)
		if name == "" {
			continue
		}
		art := r.Artwork600
		if strings.TrimSpace(art) == "" {
			art = r.Artwork100
		}
		var genre string
		if len(r.Genres) > 0 {
			genre = r.Genres[0]
		}
		results = append(results, DirectoryResult{
			Name:     name,
			Author:   strings.TrimSpace(r.ArtistName),
			FeedURL:  clean,
			ImageURL: strings.TrimSpace(art),
			Episodes: r.TrackCount,
			Genre:    genre,
		})
	}
	return results, nil
}
