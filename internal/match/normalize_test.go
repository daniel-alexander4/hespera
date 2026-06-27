package match

import "testing"

func TestNormalizeTitle(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"Abbey Road [Remastered]", "Abbey Road"},
		{"Dark Side of the Moon (2011 Remaster)", "Dark Side of the Moon"},
		{"Rumours  (Deluxe Edition)", "Rumours"},
		{"OK Computer", "OK Computer"},
		{"Album [Explicit]", "Album"},
		{"Album [Deluxe] [Remastered]", "Album"},
		{"Thriller (Special Edition)", "Thriller"},
		{"The Wall (Anniversary Edition)", "The Wall"},
		{"Back in Black (Remastered)", "Back in Black"},
		{"Let It Be - 2015 Remaster", "Let It Be"},
		{"Purple Rain (2020 Remastered Version)", "Purple Rain"},
		{"Nevermind (Super Deluxe)", "Nevermind"},
		{"Blonde on Blonde [Bonus Tracks]", "Blonde on Blonde"},
		{"Kind of Blue (Expanded Edition)", "Kind of Blue"},
		{"Album (Clean)", "Album"},
		// Live-show / date-stamp parentheticals (stripped).
		{"The End (4 February 2017, Birmingham)", "The End"},
		{"Reunion (Live)", "Reunion"},
		{"Made in Japan (Live at Osaka 1972)", "Made in Japan"},
		{"Some Album (2009)", "Some Album"},
		// Meaningful parentheticals (preserved — no year/live marker).
		{"Album (Part 1)", "Album (Part 1)"},
		{"Album (Acoustic)", "Album (Acoustic)"},
		{"Album (Maybe Tomorrow)", "Album (Maybe Tomorrow)"},
		{"Album (Decade)", "Album (Decade)"},
		{"(What's the Story) Morning Glory?", "(What's the Story) Morning Glory?"},
		{"1984 (For the Love of Big Brother)", "1984 (For the Love of Big Brother)"},
		{"", ""},
		{"  Spaced  ", "Spaced"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got := NormalizeTitle(tt.in)
			if got != tt.want {
				t.Fatalf("NormalizeTitle(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestNormalizeForDedup(t *testing.T) {
	tests := []struct {
		a, b string
		same bool
	}{
		{"Abbey Road", "Abbey Road [Remastered]", true},
		{"Abbey Road", "Abbey Road (Deluxe Edition)", true},
		{"Abbey Road", "abbey road [remastered]", true},
		{"OK Computer", "OK Computer", true},
		{"Abbey Road", "Let It Be", false},
	}
	for _, tt := range tests {
		t.Run(tt.a+" vs "+tt.b, func(t *testing.T) {
			na := NormalizeForDedup(tt.a)
			nb := NormalizeForDedup(tt.b)
			if (na == nb) != tt.same {
				t.Fatalf("NormalizeForDedup(%q)=%q, NormalizeForDedup(%q)=%q, same=%v want=%v",
					tt.a, na, tt.b, nb, na == nb, tt.same)
			}
		})
	}
}
