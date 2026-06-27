package match

import "testing"

func TestScoreCandidate(t *testing.T) {
	tests := []struct {
		name      string
		candidate Candidate
		title     string
		artist    string
		year      int
		minScore  float64
		maxScore  float64
	}{
		{
			name: "perfect match",
			candidate: Candidate{
				Title:       "Abbey Road",
				ArtistName:  "The Beatles",
				PrimaryType: "Album",
				MBScore:     100,
				Year:        1969,
			},
			title: "Abbey Road", artist: "The Beatles", year: 1969,
			minScore: 90, maxScore: 96,
		},
		{
			name: "matched threshold boundary",
			candidate: Candidate{
				Title:       "Abbey Road",
				ArtistName:  "The Beatles",
				PrimaryType: "Album",
				MBScore:     80,
				Year:        1969,
			},
			title: "Abbey Road", artist: "The Beatles", year: 1969,
			minScore: 70, maxScore: 96,
		},
		{
			name: "single type penalty",
			candidate: Candidate{
				Title:       "Hey Jude",
				ArtistName:  "The Beatles",
				PrimaryType: "Single",
				MBScore:     100,
				Year:        1968,
			},
			title: "Hey Jude", artist: "The Beatles", year: 1968,
			minScore: 80, maxScore: 90,
		},
		{
			name: "different artist",
			candidate: Candidate{
				Title:       "Abbey Road",
				ArtistName:  "Radiohead",
				PrimaryType: "Album",
				MBScore:     50,
				Year:        1969,
			},
			title: "Abbey Road", artist: "The Beatles", year: 1969,
			minScore: 40, maxScore: 65,
		},
		{
			name: "completely wrong",
			candidate: Candidate{
				Title:       "Thriller",
				ArtistName:  "Michael Jackson",
				PrimaryType: "Album",
				MBScore:     30,
				Year:        1982,
			},
			title: "Abbey Road", artist: "The Beatles", year: 1969,
			minScore: 0, maxScore: 30,
		},
		{
			name: "year off by 1",
			candidate: Candidate{
				Title:       "OK Computer",
				ArtistName:  "Radiohead",
				PrimaryType: "Album",
				MBScore:     95,
				Year:        1997,
			},
			title: "OK Computer", artist: "Radiohead", year: 1998,
			minScore: 85, maxScore: 96,
		},
		{
			name: "no year available",
			candidate: Candidate{
				Title:       "OK Computer",
				ArtistName:  "Radiohead",
				PrimaryType: "Album",
				MBScore:     90,
				Year:        0,
			},
			title: "OK Computer", artist: "Radiohead", year: 1997,
			minScore: 80, maxScore: 96,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score := ScoreCandidate(tt.candidate, tt.title, tt.artist, tt.year)
			if score < tt.minScore || score > tt.maxScore {
				t.Fatalf("ScoreCandidate() = %.1f, want [%.1f, %.1f]", score, tt.minScore, tt.maxScore)
			}
		})
	}
}

func TestBestCandidate(t *testing.T) {
	candidates := []Candidate{
		{Title: "Thriller", ArtistName: "Michael Jackson", PrimaryType: "Album", MBScore: 60, Year: 1982},
		{Title: "Abbey Road", ArtistName: "The Beatles", PrimaryType: "Album", MBScore: 100, Year: 1969},
		{Title: "Let It Be", ArtistName: "The Beatles", PrimaryType: "Album", MBScore: 80, Year: 1970},
	}

	best, score, ok := BestCandidate(candidates, "Abbey Road", "The Beatles", 1969)
	if !ok {
		t.Fatal("BestCandidate returned !ok")
	}
	if best.Title != "Abbey Road" {
		t.Fatalf("BestCandidate title = %q, want %q", best.Title, "Abbey Road")
	}
	if score < 70 {
		t.Fatalf("BestCandidate score = %.1f, want >= 70", score)
	}

	// Empty candidates.
	_, _, ok = BestCandidate(nil, "x", "y", 0)
	if ok {
		t.Fatal("BestCandidate returned ok for nil candidates")
	}
}

func TestTypeBonus(t *testing.T) {
	tests := []struct {
		typ  string
		sec  []string
		want float64
	}{
		{"Album", nil, 10},
		{"Single", nil, 2},
		{"Broadcast", nil, 4},
		{"Live", nil, 4},
		{"Remix", nil, 4},
		{"Compilation", nil, 4},
		{"EP", nil, 6}, // EP is now penalized below a clean Album
		{"", nil, 10},
		// Secondary types penalize even when primary is Album.
		{"Album", []string{"Live"}, 4},
		{"Album", []string{"Compilation"}, 4},
		{"Album", []string{"Compilation", "Live"}, 0}, // stacks, floored at 0
		{"Album", []string{"Soundtrack"}, 10},         // legit primary use, not penalized
		{"EP", []string{"Compilation"}, 0},
	}
	for _, tt := range tests {
		got := typeBonus(tt.typ, tt.sec)
		if got != tt.want {
			t.Fatalf("typeBonus(%q, %v) = %f, want %f", tt.typ, tt.sec, got, tt.want)
		}
	}
}

// TestBestCandidateStudioOverAltEditions mirrors the real Painkiller case: the
// studio album and several art-less alt editions (single, live, compilation)
// all tie on MusicBrainz score, and the studio album must win so the matcher
// selects the release-group that actually has cover art.
func TestBestCandidateStudioOverAltEditions(t *testing.T) {
	studio := Candidate{ReleaseGroupID: "studio", Title: "Painkiller", ArtistName: "Judas Priest", PrimaryType: "Album", MBScore: 100, Year: 1990}
	candidates := []Candidate{
		{ReleaseGroupID: "single", Title: "Painkiller", ArtistName: "Judas Priest", PrimaryType: "Single", MBScore: 100, Year: 1990},
		{ReleaseGroupID: "live", Title: "Painkiller", ArtistName: "Judas Priest", PrimaryType: "Album", SecondaryTypes: []string{"Live"}, MBScore: 100, Year: 1991},
		studio,
		{ReleaseGroupID: "ep", Title: "Painkiller", ArtistName: "Judas Priest", PrimaryType: "EP", MBScore: 100, Year: 1990},
		{ReleaseGroupID: "comp", Title: "Painkiller", ArtistName: "Judas Priest", PrimaryType: "Album", SecondaryTypes: []string{"Compilation"}, MBScore: 100, Year: 1990},
	}
	best, _, ok := BestCandidate(candidates, "Painkiller", "Judas Priest", 1990)
	if !ok || best.ReleaseGroupID != "studio" {
		t.Fatalf("BestCandidate = %q (ok=%v), want studio", best.ReleaseGroupID, ok)
	}
}

// TestBestCandidateAliasMatch mirrors the real Hell Bent for Leather case: the
// album is filed in MusicBrainz under its UK title "Killing Machine" with the US
// title "Hell Bent for Leather" only as an alias, while a same-named 1978 Single
// matches the local title exactly. With aliases populated, the album must win
// over the single; without them, the single would win.
func TestBestCandidateAliasMatch(t *testing.T) {
	single := Candidate{ReleaseGroupID: "single", Title: "Hell Bent for Leather", ArtistName: "Judas Priest", PrimaryType: "Single", MBScore: 94, Year: 1978}
	album := Candidate{ReleaseGroupID: "album", Title: "Killing Machine", ArtistName: "Judas Priest", PrimaryType: "Album", MBScore: 100, Year: 1978, Aliases: []string{"Hell Bent for Leather"}}

	best, _, ok := BestCandidate([]Candidate{single, album}, "Hell Bent for Leather", "Judas Priest", 1978)
	if !ok || best.ReleaseGroupID != "album" {
		t.Fatalf("with alias: BestCandidate = %q (ok=%v), want album", best.ReleaseGroupID, ok)
	}

	// Without the alias, the same-named single wins — confirming the alias is
	// what flips the selection.
	albumNoAlias := album
	albumNoAlias.Aliases = nil
	best, _, _ = BestCandidate([]Candidate{single, albumNoAlias}, "Hell Bent for Leather", "Judas Priest", 1978)
	if best.ReleaseGroupID != "single" {
		t.Fatalf("without alias: BestCandidate = %q, want single", best.ReleaseGroupID)
	}
}

func TestTypeDemotion(t *testing.T) {
	cases := []struct {
		name      string
		primary   string
		secondary []string
		want      float64
	}{
		{"clean album", "Album", nil, 0},
		{"soundtrack (not penalized)", "Soundtrack", nil, 0},
		{"single", "Single", nil, 8},
		{"ep", "EP", nil, 4},
		{"album + live secondary", "Album", []string{"Live"}, 6},
		{"album + compilation secondary", "Album", []string{"Compilation"}, 6},
		{"primary live", "Live", nil, 6},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := typeDemotion(c.primary, c.secondary); got != c.want {
				t.Fatalf("typeDemotion(%q,%v) = %v, want %v", c.primary, c.secondary, got, c.want)
			}
		})
	}
}

// TestBestMatchCandidateNonDemoting covers the watched case: a real Live album
// (Deep Purple's "Made in Japan") whose raw score falls just under the threshold
// purely because of the secondary-type penalty must still match, while the
// penalty is preserved for ranking among eligible siblings.
func TestBestMatchCandidateNonDemoting(t *testing.T) {
	// A live album with an exact title/artist match but a modest MB score: the
	// -6 Live penalty drops its raw score below threshold, yet its content
	// (title+artist+mb+year, crediting a clean type) clears the gate.
	live := Candidate{ReleaseGroupID: "live", Title: "Made in Japan", ArtistName: "Deep Purple",
		PrimaryType: "Album", SecondaryTypes: []string{"Live"}, MBScore: 40, Year: 1972}

	if s := ScoreCandidate(live, "Made in Japan", "Deep Purple", 1972); s >= matchThreshold {
		t.Fatalf("precondition: live raw score = %.2f, want < %v (penalty must push it under)", s, matchThreshold)
	}

	t.Run("rescues a near-threshold live album", func(t *testing.T) {
		best, _, ok := BestMatchCandidate([]Candidate{live}, "Made in Japan", "Deep Purple", 1972)
		if !ok || best.ReleaseGroupID != "live" {
			t.Fatalf("BestMatchCandidate = %q (ok=%v), want live matched", best.ReleaseGroupID, ok)
		}
	})

	t.Run("prefers the clean studio album among eligibles", func(t *testing.T) {
		studio := Candidate{ReleaseGroupID: "studio", Title: "Machine Head", ArtistName: "Deep Purple",
			PrimaryType: "Album", MBScore: 100, Year: 1972}
		liveStrong := Candidate{ReleaseGroupID: "live", Title: "Machine Head", ArtistName: "Deep Purple",
			PrimaryType: "Album", SecondaryTypes: []string{"Live"}, MBScore: 100, Year: 1972}
		best, _, ok := BestMatchCandidate([]Candidate{liveStrong, studio}, "Machine Head", "Deep Purple", 1972)
		if !ok || best.ReleaseGroupID != "studio" {
			t.Fatalf("BestMatchCandidate = %q (ok=%v), want studio", best.ReleaseGroupID, ok)
		}
	})

	t.Run("does not rescue weak-content candidates", func(t *testing.T) {
		// Right title but wrong artist → content stays far under the gate even
		// crediting a clean type, so no spurious match.
		wrong := Candidate{ReleaseGroupID: "wrong", Title: "Made in Japan", ArtistName: "Some Other Band",
			PrimaryType: "Album", SecondaryTypes: []string{"Live"}, MBScore: 40, Year: 1972}
		if _, _, ok := BestMatchCandidate([]Candidate{wrong}, "Made in Japan", "Deep Purple", 1972); ok {
			t.Fatal("BestMatchCandidate matched a wrong-artist candidate, want no match")
		}
	})

	t.Run("empty is no match", func(t *testing.T) {
		if _, _, ok := BestMatchCandidate(nil, "x", "y", 0); ok {
			t.Fatal("BestMatchCandidate(nil) returned ok")
		}
	})
}

func TestCandidatesAboveThreshold(t *testing.T) {
	candidates := []Candidate{
		{ReleaseGroupID: "live", Title: "Painkiller", ArtistName: "Judas Priest", PrimaryType: "Album", SecondaryTypes: []string{"Live"}, MBScore: 100, Year: 1991},
		{ReleaseGroupID: "studio", Title: "Painkiller", ArtistName: "Judas Priest", PrimaryType: "Album", MBScore: 100, Year: 1990},
		{ReleaseGroupID: "single", Title: "Painkiller", ArtistName: "Judas Priest", PrimaryType: "Single", MBScore: 100, Year: 1990},
		{ReleaseGroupID: "wrong", Title: "Thriller", ArtistName: "Michael Jackson", PrimaryType: "Album", MBScore: 100, Year: 1982},
	}
	got := CandidatesAboveThreshold(candidates, "Painkiller", "Judas Priest", 1990)

	// The wrong-album candidate scores well below threshold and is excluded.
	for _, sc := range got {
		if sc.Candidate.ReleaseGroupID == "wrong" {
			t.Fatal("different-album candidate should be below threshold")
		}
	}
	if len(got) == 0 {
		t.Fatal("expected at least the studio candidate above threshold")
	}
	// First element matches BestCandidate's pick (the clean studio album).
	best, _, _ := BestCandidate(candidates, "Painkiller", "Judas Priest", 1990)
	if got[0].Candidate.ReleaseGroupID != best.ReleaseGroupID {
		t.Fatalf("first = %q, want BestCandidate %q", got[0].Candidate.ReleaseGroupID, best.ReleaseGroupID)
	}
	if got[0].Candidate.ReleaseGroupID != "studio" {
		t.Fatalf("first = %q, want studio", got[0].Candidate.ReleaseGroupID)
	}
	// Descending score order.
	for i := 1; i < len(got); i++ {
		if got[i].Score > got[i-1].Score {
			t.Fatalf("not sorted descending at %d: %.1f > %.1f", i, got[i].Score, got[i-1].Score)
		}
	}
}

func TestIsCleanAlbum(t *testing.T) {
	tests := []struct {
		name string
		c    Candidate
		want bool
	}{
		{"studio album", Candidate{PrimaryType: "Album"}, true},
		{"album + live secondary", Candidate{PrimaryType: "Album", SecondaryTypes: []string{"Live"}}, false},
		{"album + compilation", Candidate{PrimaryType: "Album", SecondaryTypes: []string{"Compilation"}}, false},
		{"album + soundtrack (allowed)", Candidate{PrimaryType: "Album", SecondaryTypes: []string{"Soundtrack"}}, true},
		{"EP", Candidate{PrimaryType: "EP"}, false},
		{"single", Candidate{PrimaryType: "Single"}, false},
		{"live primary", Candidate{PrimaryType: "Live"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isCleanAlbum(tt.c); got != tt.want {
				t.Fatalf("isCleanAlbum() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestYearBonus(t *testing.T) {
	tests := []struct {
		mbYear, localYear int
		want              float64
	}{
		{1969, 1969, 4},
		{1969, 1970, 4},
		{1969, 1972, 2},
		{1969, 1975, 0},
		{0, 1969, 0},
		{1969, 0, 0},
	}
	for _, tt := range tests {
		got := yearBonus(tt.mbYear, tt.localYear)
		if got != tt.want {
			t.Fatalf("yearBonus(%d, %d) = %f, want %f", tt.mbYear, tt.localYear, got, tt.want)
		}
	}
}
