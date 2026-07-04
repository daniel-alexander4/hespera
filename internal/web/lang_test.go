package web

import "testing"

func TestLangKey(t *testing.T) {
	cases := []struct{ in, want string }{
		{"en", "en"},
		{"EN", "en"},
		{" eng ", "en"},
		{"en-US", "en"},
		{"pt-br", "pt"},
		{"ger", "de"}, // ISO 639-2/B
		{"deu", "de"}, // ISO 639-2/T
		{"fre", "fr"},
		{"fra", "fr"},
		{"zho", "zh"},
		{"chi", "zh"},
		{"xx", "xx"},   // unknown 2-letter passes through
		{"qaa", "qaa"}, // unknown 3-letter passes through
		{"", ""},
	}
	for _, c := range cases {
		if got := langKey(c.in); got != c.want {
			t.Errorf("langKey(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestLangsMatch(t *testing.T) {
	cases := []struct {
		pref, stream string
		want         bool
	}{
		{"en", "eng", true},
		{"eng", "en", true},
		{"de", "ger", true},
		{"de", "deu", true},
		{"en", "en-US", true},
		{"en", "spa", false},
		{"", "eng", false}, // no preference matches nothing
		{"en", "", false},  // untagged stream never matches
		{"", "", false},
		{"qaa", "qaa", true}, // unknown codes still match each other
	}
	for _, c := range cases {
		if got := langsMatch(c.pref, c.stream); got != c.want {
			t.Errorf("langsMatch(%q, %q) = %v, want %v", c.pref, c.stream, got, c.want)
		}
	}
}

func TestSanitizeLangSetting(t *testing.T) {
	cases := []struct{ in, want string }{
		{"en", "en"},
		{" EN ", "en"},
		{"eng", "eng"},
		{"pt-br", "pt-br"},
		{"", ""},
		{"english", ""},          // not a code
		{"e", ""},                // too short
		{"en; DROP TABLE x", ""}, // garbage (e.g. stored via hescli) degrades to no preference
		{"12", ""},               // digits aren't a code
	}
	for _, c := range cases {
		if got := sanitizeLangSetting(c.in); got != c.want {
			t.Errorf("sanitizeLangSetting(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
