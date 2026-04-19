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

	// Collapse whitespace and trim.
	fields := strings.Fields(s)
	return strings.Join(fields, " ")
}

// NormalizeForDedup combines NormalizeTitle + Normalize (lowercase, strip non-alnum).
func NormalizeForDedup(s string) string {
	return Normalize(NormalizeTitle(s))
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
