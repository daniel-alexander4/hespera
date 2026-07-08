package match

import (
	"regexp"
	"strings"
	"unicode"
)

// Normalize lowercases, strips non-alphanumeric characters (except spaces),
// collapses whitespace, and trims the result.
func Normalize(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := false
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			prevSpace = false
		} else if !prevSpace {
			b.WriteByte(' ')
			prevSpace = true
		}
	}
	return strings.TrimSpace(b.String())
}

// LevenshteinDistance computes the edit distance between two strings.
func LevenshteinDistance(a, b string) int {
	ra := []rune(a)
	rb := []rune(b)
	la, lb := len(ra), len(rb)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}

	// Single-row DP.
	prev := make([]int, lb+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		curr := make([]int, lb+1)
		curr[0] = i
		for j := 1; j <= lb; j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			del := prev[j] + 1
			ins := curr[j-1] + 1
			sub := prev[j-1] + cost
			m := del
			if ins < m {
				m = ins
			}
			if sub < m {
				m = sub
			}
			curr[j] = m
		}
		prev = curr
	}
	return prev[lb]
}

// reAnnotation matches bracketed/parenthesized common suffixes.
var reAnnotation = regexp.MustCompile(`(?i)\s*[\[\(]\s*(?:` +
	`remaster(?:ed)?|` +
	`deluxe(?:\s+edition)?|` +
	`bonus\s+track(?:s|version)?|` +
	`expanded(?:\s+edition)?|` +
	`special\s+edition|` +
	`anniversary\s+edition|` +
	`super\s+deluxe|` +
	`explicit|` +
	`clean` +
	`)\s*[\]\)]`)

// reYearRemaster matches trailing year-remaster patterns like " - 2015 Remaster" or "(2020 Remastered Version)".
var reYearRemaster = regexp.MustCompile(`(?i)\s*(?:-\s*)?[\(\[]?\d{4}\s+remaster(?:ed)?(?:\s+version)?[\)\]]?\s*$`)

// reLiveAnnotation matches a trailing parenthesized/bracketed live-show or
// date-stamp annotation — e.g. "(4 February 2017, Birmingham)", "(Live at
// Wembley)", "(Live 1985)". It fires only when the parenthetical begins with
// "live" or contains a 4-digit year (19xx/20xx), so meaningful subtitles like
// "(Part 1)", "(Acoustic)", or "(Maybe Tomorrow)" are preserved. A month-name
// signal was deliberately omitted: month abbreviations collide with common
// words ("may"→"maybe", "dec"→"decade"), and real date stamps carry a year
// anyway. Anchored to end-of-string so leading parentheticals like "(What's the
// Story) Morning Glory?" are untouched.
var reLiveAnnotation = regexp.MustCompile(`(?i)\s*[\(\[]\s*(?:` +
	`live\b[^\)\]]*|` +
	`[^\)\]]*\b(?:19|20)\d{2}\b[^\)\]]*` +
	`)[\)\]]\s*$`)

// NormalizeTitle strips common annotations for display and dedup.
// Removes remaster, deluxe edition, explicit, trailing year-remaster patterns, etc.
// Preserves casing and letters.
func NormalizeTitle(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}

	// Strip bracketed/parenthesized annotations (may appear multiple times).
	for {
		next := reAnnotation.ReplaceAllString(s, "")
		next = strings.TrimSpace(next)
		if next == s {
			break
		}
		s = next
	}

	// Strip trailing year-remaster patterns.
	s = reYearRemaster.ReplaceAllString(s, "")

	// Strip a trailing live-show / date-stamp annotation.
	s = reLiveAnnotation.ReplaceAllString(s, "")

	// Collapse whitespace and trim.
	fields := strings.Fields(s)
	return strings.Join(fields, " ")
}

// NormalizeForDedup combines NormalizeTitle + Normalize (lowercase, strip non-alnum).
func NormalizeForDedup(s string) string {
	return Normalize(NormalizeTitle(s))
}

// titleContainmentBoost is the score credited when one title is a whole-word
// run inside the other (below 1.0 so an exact whole-string match still ranks
// higher, above the 0.80 video-match threshold so a containment clears it).
const titleContainmentBoost = 0.90

// TitleMatchSimilarity scores a candidate title against a query for the video
// matchers, where a library folder often carries the common/short name while
// TMDB's canonical title adds a leading article ("The IT Crowd"), a franchise
// prefix ("Tom Clancy's Jack Ryan"), or a subtitle suffix ("Pennyworth: The
// Origin of Batman's Butler"). Whole-string Levenshtein over-penalizes that
// length gap, so this returns max(NormalizedSimilarity, a containment boost):
// when the shorter title appears verbatim as a whole-word run inside the longer
// (either direction), the extra words don't sink the score. NOT used for music
// matching, which keeps NormalizedSimilarity's stricter whole-string measure.
func TitleMatchSimilarity(candidate, query string) float64 {
	sim := NormalizedSimilarity(candidate, query)
	if sim >= titleContainmentBoost {
		return sim
	}
	if titleContains(candidate, query) {
		return titleContainmentBoost
	}
	return sim
}

// titleContains reports whether the shorter of the two normalized titles appears
// as a whole-word contiguous run inside the longer — the leading-article /
// franchise-prefix / subtitle-suffix pattern. Space-padding makes the match
// word-boundary-safe ("Terminal" is not inside "The Terminator"). Gated to a
// specific-enough shorter title (≥2 tokens or ≥8 chars) so a generic single
// short word ("House", "Doctor") can't latch onto a longer unrelated show;
// those keep pure whole-string scoring.
func titleContains(a, b string) bool {
	short, long := Normalize(a), Normalize(b)
	if len([]rune(long)) < len([]rune(short)) {
		short, long = long, short
	}
	if !containmentEligible(short) {
		return false
	}
	return strings.Contains(" "+long+" ", " "+short+" ")
}

// containmentEligible gates the containment boost to titles specific enough that
// a whole-word match is a strong signal: ≥8 runes, or ≥2 tokens.
func containmentEligible(norm string) bool {
	if norm == "" {
		return false
	}
	if len([]rune(norm)) >= 8 {
		return true
	}
	return strings.Contains(norm, " ")
}

// NormalizedSimilarity returns a value in [0.0, 1.0] representing how similar
// two strings are after normalization. 1.0 means identical.
func NormalizedSimilarity(a, b string) float64 {
	na := Normalize(a)
	nb := Normalize(b)
	if na == nb {
		return 1.0
	}
	maxLen := len([]rune(na))
	if l := len([]rune(nb)); l > maxLen {
		maxLen = l
	}
	if maxLen == 0 {
		return 1.0
	}
	dist := LevenshteinDistance(na, nb)
	return 1.0 - float64(dist)/float64(maxLen)
}
