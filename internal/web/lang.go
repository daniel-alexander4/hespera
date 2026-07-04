package web

import "strings"

// iso639Alias maps ISO 639-2 three-letter codes (both bibliographic and
// terminological variants — ffprobe stream tags use these, usually the B form:
// "ger", "fre") to the ISO 639-1 two-letter code users type in Settings. Only
// languages likely to appear as media audio/subtitle tags; an unmapped code
// simply compares as itself.
var iso639Alias = map[string]string{
	"eng": "en", "spa": "es", "ger": "de", "deu": "de", "fre": "fr", "fra": "fr",
	"ita": "it", "jpn": "ja", "chi": "zh", "zho": "zh", "rus": "ru", "por": "pt",
	"dut": "nl", "nld": "nl", "swe": "sv", "nor": "no", "dan": "da", "fin": "fi",
	"pol": "pl", "kor": "ko", "ara": "ar", "heb": "he", "hin": "hi", "tur": "tr",
	"cze": "cs", "ces": "cs", "gre": "el", "ell": "el", "hun": "hu", "ukr": "uk",
	"tha": "th", "vie": "vi", "ind": "id", "rum": "ro", "ron": "ro", "bul": "bg",
	"hrv": "hr", "srp": "sr", "slo": "sk", "slk": "sk", "slv": "sl", "cat": "ca",
	"ice": "is", "isl": "is", "lit": "lt", "lav": "lv", "est": "et", "may": "ms",
	"msa": "ms", "per": "fa", "fas": "fa", "tam": "ta", "tel": "te", "ben": "bn",
	"urd": "ur", "mal": "ml", "kan": "kn", "mar": "mr", "guj": "gu", "pan": "pa",
}

// langKey canonicalizes a language code for comparison: lowercased, any region
// suffix stripped ("en-US" → "en", "pt-br" → "pt"), and ISO 639-2 three-letter
// forms folded to their 639-1 two-letter equivalent where known ("eng" → "en").
// Unknown codes pass through, so two files tagged with the same exotic code
// still match each other.
func langKey(code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	if i := strings.IndexByte(code, '-'); i >= 0 {
		code = code[:i]
	}
	if two, ok := iso639Alias[code]; ok {
		return two
	}
	return code
}

// langsMatch reports whether a user language preference matches a stream's
// language tag. Empty on either side never matches — an untagged stream can't
// satisfy a preference, and no preference prefers nothing.
func langsMatch(pref, streamLang string) bool {
	if pref == "" || streamLang == "" {
		return false
	}
	return langKey(pref) == langKey(streamLang)
}

// sanitizeLangSetting normalizes a language-preference setting value: lowercased
// and trimmed, and anything that isn't a plausible ISO-ish code becomes ""
// (no preference). Unlike normalizeLang it does NOT default to "en" — an empty
// preference is meaningful here.
func sanitizeLangSetting(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if !langPattern.MatchString(s) {
		return ""
	}
	return s
}
