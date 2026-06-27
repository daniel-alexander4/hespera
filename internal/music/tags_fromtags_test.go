package music

import "testing"

func TestTrackMetaFromTags(t *testing.T) {
	tests := []struct {
		name      string
		tags      map[string]string
		path      string
		wantTitle string
		wantArt   string // artist
		wantAlb   string
		wantYear  int
		wantTrack int
		wantDisc  int
		wantComp  bool
	}{
		{
			name: "flac vorbis basic",
			tags: map[string]string{
				"title": "Hell Bent for Leather", "artist": "Judas Priest",
				"album": "Killing Machine", "date": "1978", "tracknumber": "5",
			},
			path:      "/m/05 Hell Bent for Leather.flac",
			wantTitle: "Hell Bent for Leather", wantArt: "Judas Priest",
			wantAlb: "Killing Machine", wantYear: 1978, wantTrack: 5,
		},
		{
			name: "m4a slash track and full date",
			tags: map[string]string{
				"title": "Breathe", "artist": "Pink Floyd", "album": "Dark Side",
				"date": "1973-03-01", "track": "2/10", "disc": "1/1",
			},
			path:      "/m/02 Breathe.m4a",
			wantTitle: "Breathe", wantArt: "Pink Floyd", wantAlb: "Dark Side",
			wantYear: 1973, wantTrack: 2, wantDisc: 1,
		},
		{
			name: "placeholder track artist falls back to a real album artist",
			tags: map[string]string{
				"title": "Hush", "artist": "Unknown Artist",
				"album_artist": "Deep Purple", "album": "Shades of Deep Purple", "date": "1968",
			},
			path:      "/m/02 Hush.mp3",
			wantTitle: "Hush", wantArt: "Deep Purple",
			wantAlb: "Shades of Deep Purple", wantYear: 1968,
		},
		{
			name: "albumartist marks compilation via Various Artists",
			tags: map[string]string{
				"title": "Song", "artist": "Some Band", "album": "Hits",
				"album_artist": "Various Artists",
			},
			path:      "/m/song.flac",
			wantTitle: "Song", wantArt: "Some Band", wantAlb: "Hits", wantComp: true,
		},
		{
			name: "compilation flag truthy",
			tags: map[string]string{
				"title": "X", "artist": "A", "album": "B", "compilation": "1",
			},
			path:      "/m/x.ogg",
			wantTitle: "X", wantArt: "A", wantAlb: "B", wantComp: true,
		},
		{
			name: "explicit not compilation overrides Various Artists albumartist",
			tags: map[string]string{
				"title": "X", "artist": "A", "album": "B",
				"album_artist": "Various Artists", "compilation": "0",
			},
			path:      "/m/x.flac",
			wantTitle: "X", wantArt: "A", wantAlb: "B", wantComp: false,
		},
		{
			name:      "missing tags fall back to filename title and Unknowns",
			tags:      map[string]string{},
			path:      "/m/Mystery Track.m4a",
			wantTitle: "Mystery Track", wantArt: "Unknown Artist", wantAlb: "Unknown Album",
		},
		{
			name: "generic compilation artist parsed from filename",
			tags: map[string]string{"artist": "Various Artists", "album": "Comp"},
			path: "/m/Queen - Bohemian Rhapsody.flac",
			// IsGenericCompilationArtist → parse "Artist - Title" from filename
			wantTitle: "Bohemian Rhapsody", wantArt: "Queen", wantAlb: "Comp", wantComp: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TrackMetaFromTags(tt.tags, tt.path)
			if got.Title != tt.wantTitle {
				t.Errorf("Title = %q, want %q", got.Title, tt.wantTitle)
			}
			if got.Artist != tt.wantArt {
				t.Errorf("Artist = %q, want %q", got.Artist, tt.wantArt)
			}
			if got.Album != tt.wantAlb {
				t.Errorf("Album = %q, want %q", got.Album, tt.wantAlb)
			}
			if got.Year != tt.wantYear {
				t.Errorf("Year = %d, want %d", got.Year, tt.wantYear)
			}
			if got.Track != tt.wantTrack {
				t.Errorf("Track = %d, want %d", got.Track, tt.wantTrack)
			}
			if got.Disc != tt.wantDisc {
				t.Errorf("Disc = %d, want %d", got.Disc, tt.wantDisc)
			}
			if got.IsCompilation != tt.wantComp {
				t.Errorf("IsCompilation = %v, want %v", got.IsCompilation, tt.wantComp)
			}
		})
	}
}

func TestSetArtNormalizes(t *testing.T) {
	// A 1x1 PNG; SetArt must detect the MIME from the bytes when none is given.
	png := []byte{
		0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A,
		0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1F, 0x15, 0xC4,
		0x89,
	}
	var m TrackMeta
	m.SetArt("", png)
	if !m.HasArt {
		t.Fatal("HasArt = false, want true")
	}
	if m.ArtMIME != "image/png" {
		t.Errorf("ArtMIME = %q, want image/png", m.ArtMIME)
	}

	// Empty bytes clear the art.
	var e TrackMeta
	e.SetArt("image/jpeg", nil)
	if e.HasArt {
		t.Error("HasArt = true for empty art, want false")
	}
}
