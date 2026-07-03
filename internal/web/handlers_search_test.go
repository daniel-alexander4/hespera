package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// Global search: every section hits, prefix matches outrank substring matches,
// bio mentions dedupe against direct name hits, and short queries return
// nothing (min 2 chars).

func searchJSON(t *testing.T, router http.Handler, q string) []searchSection {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/search?q="+q, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("search %q: %d — %s", q, rec.Code, rec.Body.String())
	}
	var out struct {
		Sections []searchSection `json:"sections"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("parse: %v", err)
	}
	return out.Sections
}

func section(sections []searchSection, label string) *searchSection {
	for i := range sections {
		if sections[i].Label == label {
			return &sections[i]
		}
	}
	return nil
}

func TestSearchAllSections(t *testing.T) {
	h, db := newTestHandler(t)
	router := h.Router()

	// Music: artist "Rainbow", album "Rising", track "Stargazer", plus a bio
	// that mentions a band member.
	libRes, err := db.Exec("INSERT INTO libraries (name, type, root_path) VALUES ('M', 'music', ?)", h.cfg.MediaRoot)
	if err != nil {
		t.Fatal(err)
	}
	libID, _ := libRes.LastInsertId()
	ar, err := db.Exec("INSERT INTO music_artists (library_id, name, bio, bio_source_url) VALUES (?, 'Rainbow', 'Founded by Ritchie Blackmore after Deep Purple.', '')", libID)
	if err != nil {
		t.Fatal(err)
	}
	artistID, _ := ar.LastInsertId()
	al, err := db.Exec("INSERT INTO music_albums (library_id, artist_id, album_artist_id, title, year, is_compilation) VALUES (?, ?, ?, 'Rising', 1976, 0)", libID, artistID, artistID)
	if err != nil {
		t.Fatal(err)
	}
	albumID, _ := al.LastInsertId()
	if _, err := db.Exec("INSERT INTO music_tracks (library_id, artist_id, album_id, title, track_no, disc_no, abs_path, mime_type) VALUES (?, ?, ?, 'Stargazer', 1, 1, '/m/s.mp3', 'audio/mpeg')", libID, artistID, albumID); err != nil {
		t.Fatal(err)
	}

	// TV: matched series + cached name. Movies: matched file + cached title.
	tvRes, err := db.Exec("INSERT INTO libraries (name, type, root_path) VALUES ('TV', 'tv', ?)", h.cfg.MediaRoot)
	if err != nil {
		t.Fatal(err)
	}
	tvLib, _ := tvRes.LastInsertId()
	fres, err := db.Exec("INSERT INTO tv_series_files (library_id, abs_path, container) VALUES (?, ?, 'mkv')", tvLib, filepath.Join(h.cfg.MediaRoot, "s.mkv"))
	if err != nil {
		t.Fatal(err)
	}
	fid, _ := fres.LastInsertId()
	if _, err := db.Exec(`INSERT INTO tv_series_identities (file_id, provider, series_id, status, guessed_title, season_number, episode_numbers_csv)
		VALUES (?, 'tmdb', '888', 'matched', '', 1, '1')`, fid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO tv_series_metadata_cache (entity_key, lang, payload_json) VALUES ('show:888', 'en', '{"name":"Rain Dogs","first_air_date":"2023-03-06"}')`); err != nil {
		t.Fatal(err)
	}
	seedMovieFile(t, db, "Rain Man", 1988, "matched", 630, h.cfg.MediaRoot)
	if _, err := db.Exec(`INSERT INTO movie_metadata_cache (entity_key, lang, payload_json) VALUES ('movie:630', 'en', '{"title":"Rain Man","release_date":"1988-12-16"}')`); err != nil {
		t.Fatal(err)
	}

	// People + a character credit tied to the cached show.
	if _, err := db.Exec("INSERT INTO people (tmdb_id, name) VALUES (77, 'Daniel Rainwater')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO credits (person_id, media_type, media_id, character_name, billing_order) VALUES (77, 'tv', 888, 'Rainmaker', 1)"); err != nil {
		t.Fatal(err)
	}

	sections := searchJSON(t, router, "rain")
	for _, want := range []struct{ label, text, hrefPart string }{
		{"Artists", "Rainbow", "/music/artist/"},
		{"TV Shows", "Rain Dogs", "/tv/series/888"},
		{"Movies", "Rain Man", "/movie/630"},
		{"People", "Daniel Rainwater", "/person/77"},
	} {
		sec := section(sections, want.label)
		if sec == nil {
			t.Fatalf("missing section %q in %+v", want.label, sections)
		}
		found := false
		for _, r := range sec.Rows {
			if r.Text == want.text {
				found = true
				if want.hrefPart != "" && !contains(r.Href, want.hrefPart) {
					t.Fatalf("%s row href = %q, want it to contain %q", want.label, r.Href, want.hrefPart)
				}
			}
		}
		if !found {
			t.Fatalf("section %q missing %q: %+v", want.label, want.text, sec.Rows)
		}
	}

	// Songs act: the track row deep-links into the player at that track.
	songs := section(searchJSON(t, router, "starg"), "Songs")
	if songs == nil || len(songs.Rows) == 0 || !contains(songs.Rows[0].Href, "/music/player?album=") || !contains(songs.Rows[0].Href, "&track=") {
		t.Fatalf("song row should start playback, got %+v", songs)
	}

	// Character search resolves to the actor with role context.
	people := section(searchJSON(t, router, "rainmaker"), "People")
	if people == nil || len(people.Rows) == 0 || people.Rows[0].Context != "as Rainmaker in Rain Dogs" {
		t.Fatalf("character row context wrong: %+v", people)
	}

	// Bio mention: "blackmore" appears only in Rainbow's bio → labeled section,
	// and the artist is NOT also listed as a direct name hit.
	bioSecs := searchJSON(t, router, "blackmore")
	bio := section(bioSecs, "Mentioned in artist bios")
	if bio == nil || len(bio.Rows) != 1 || bio.Rows[0].Text != "Rainbow" {
		t.Fatalf("bio mention wrong: %+v", bioSecs)
	}
	if section(bioSecs, "Artists") != nil {
		t.Fatalf("no direct artist hit expected for 'blackmore': %+v", bioSecs)
	}

	// Prefix ranking: "rain" must put "Rainbow" (prefix) above any substring hit.
	if _, err := db.Exec("INSERT INTO music_artists (library_id, name, bio, bio_source_url) VALUES (?, 'The Rainmakers', '', '')", libID); err != nil {
		t.Fatal(err)
	}
	artists := section(searchJSON(t, router, "rain"), "Artists")
	if artists == nil || artists.Rows[0].Text != "Rainbow" {
		t.Fatalf("prefix match should rank first, got %+v", artists)
	}

	// Min 2 chars → empty.
	if got := searchJSON(t, router, "r"); len(got) != 0 {
		t.Fatalf("1-char query should return nothing, got %+v", got)
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
