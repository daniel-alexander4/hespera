package moviescan

import "testing"

func TestParseMovie(t *testing.T) {
	const root = "/media/movies"
	tests := []struct {
		name      string
		path      string
		wantTitle string
		wantYear  int
	}{
		{"dotted scene name", "/media/movies/The.Matrix.1999.1080p.BluRay.x264.mkv", "The Matrix", 1999},
		{"parenthesized year", "/media/movies/Minority Report (2002).mkv", "Minority Report", 2002},
		{"movie in folder, bare file", "/media/movies/Deadpool (2016)/movie.mkv", "Deadpool", 2016},
		{"movie in folder, named file no year", "/media/movies/The Matrix (1999)/The Matrix.mkv", "The Matrix", 1999},
		{"title contains a year, real year in parens", "/media/movies/Blade Runner 2049 (2017).mkv", "Blade Runner 2049", 2017},
		{"no year", "/media/movies/Mickey 17.mkv", "Mickey 17", 0},
		{"sequel number, dotted", "/media/movies/Deadpool.2.2018.2160p.UHD.mkv", "Deadpool 2", 2018},
		{"bracketed group prefix", "/media/movies/[Group] Minority.Report.2002.720p.mkv", "Minority Report", 2002},
		{"bare file directly under root, no year", "/media/movies/randomclip.mkv", "randomclip", 0},
		{"year-only title", "/media/movies/2012.2009.1080p.mkv", "2012", 2009},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseMovie(tt.path, root)
			if got == nil {
				t.Fatalf("ParseMovie(%q) = nil, want title %q year %d", tt.path, tt.wantTitle, tt.wantYear)
			}
			if got.Title != tt.wantTitle {
				t.Errorf("title = %q, want %q", got.Title, tt.wantTitle)
			}
			if got.Year != tt.wantYear {
				t.Errorf("year = %d, want %d", got.Year, tt.wantYear)
			}
		})
	}
}

// ParseMovie must not adopt the library root folder's name for a bare file.
func TestParseMovieRootNotAdopted(t *testing.T) {
	got := ParseMovie("/media/movies/file.mkv", "/media/movies")
	if got == nil {
		t.Fatal("want non-nil for a parseable stem")
	}
	if got.Title == "movies" {
		t.Errorf("title adopted the library root name %q", got.Title)
	}
}
