package moviescan

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// MovieIdentity is the parsed identity of a movie file: a cleaned title and an
// optional release year. Both are derived purely from the path (no I/O), so a
// re-scan reconverges existing identities when the parsing logic improves. The
// matcher uses Title+Year to disambiguate same-titled films against TMDB.
type MovieIdentity struct {
	Title string
	Year  int // 0 when no year could be parsed
}

var (
	// reYearParen matches a release year wrapped in parens/brackets — the strong
	// signal (Title (1999) / Title [1999]), preferred over a bare year token so a
	// title that itself contains a year (Blade Runner 2049 (2017)) resolves to the
	// parenthesized one.
	reYearParen = regexp.MustCompile(`[\(\[]((?:19|20)\d{2})[\)\]]`)
	// reYearBare matches an unwrapped year token (Title.1999.1080p). Used only when
	// there's no parenthesized year.
	reYearBare = regexp.MustCompile(`\b((?:19|20)\d{2})\b`)
)

// movieJunkTokens are scene/edition tokens dropped from a movie title when there
// is no year to cut at. Movies carry more edition noise than TV (extended,
// unrated, director's cut…), so this is a superset of the TV quality set.
var movieJunkTokens = map[string]bool{
	"2160p": true, "1080p": true, "720p": true, "480p": true, "4k": true, "uhd": true,
	"x264": true, "x265": true, "h264": true, "h265": true, "hevc": true, "avc": true,
	"web": true, "web-dl": true, "webdl": true, "webrip": true, "bluray": true, "brrip": true,
	"bdrip": true, "dvdrip": true, "hdrip": true, "remux": true, "hdtv": true,
	"proper": true, "repack": true, "extended": true, "unrated": true, "remastered": true,
	"imax": true, "hdr": true, "hdr10": true, "dts": true, "ac3": true, "aac": true,
	"atmos": true, "truehd": true, "ddp5": true, "5": true, "1": true,
}

var (
	reLeadingTag  = regexp.MustCompile(`^\s*[\[\{][^\]\}]*[\]\}]\s*`)
	reLeadingSite = regexp.MustCompile(`(?i)^\s*www\.[^\s]+\s*-\s*`)
)

// ParseMovie derives a MovieIdentity from a file path. It first parses the
// filename; if that yields no usable title or no year, it falls back to the
// parent folder name (the common "Movie (2019)/movie.mkv" layout) — but never to
// the library root itself, so a bare file directly under the root doesn't adopt
// the root folder's name. Returns nil when no title can be recovered.
func ParseMovie(absPath, root string) *MovieIdentity {
	base := strings.TrimSuffix(filepath.Base(absPath), filepath.Ext(absPath))
	ftitle, fyear := parseTitleYear(base)

	// A filename carrying its own year is self-describing and wins outright.
	if fyear != 0 {
		return &MovieIdentity{Title: ftitle, Year: fyear}
	}

	// No year in the filename: the parent folder is the authoritative source for
	// the "Movie (2019)/clip.mkv" layout (a generic stem next to a Title-Year
	// folder). Never consult the library root itself.
	dir := filepath.Dir(absPath)
	if filepath.Clean(dir) != filepath.Clean(root) {
		dtitle, dyear := parseTitleYear(filepath.Base(dir))
		if dyear != 0 && dtitle != "" {
			return &MovieIdentity{Title: dtitle, Year: dyear}
		}
		if ftitle == "" && dtitle != "" {
			return &MovieIdentity{Title: dtitle, Year: dyear}
		}
	}

	if ftitle == "" {
		return nil
	}
	return &MovieIdentity{Title: ftitle, Year: fyear}
}

// parseTitleYear splits a name into a cleaned title and a release year. A
// parenthesized year wins; otherwise the last in-range bare year token is used
// and everything before it is the title (which discards trailing scene/quality
// noise for free). With no year, the whole name is cleaned token-by-token.
func parseTitleYear(name string) (string, int) {
	maxYear := time.Now().Year() + 1

	// Prefer a parenthesized/bracketed year (strong signal).
	if locs := reYearParen.FindAllStringSubmatchIndex(name, -1); len(locs) > 0 {
		m := locs[len(locs)-1] // last occurrence
		y, _ := strconv.Atoi(name[m[2]:m[3]])
		if y >= 1888 && y <= maxYear {
			return cleanTitlePart(name[:m[0]], name), y
		}
	}

	// Else the last bare year token within plausible range.
	if locs := reYearBare.FindAllStringSubmatchIndex(name, -1); len(locs) > 0 {
		for i := len(locs) - 1; i >= 0; i-- {
			m := locs[i]
			y, _ := strconv.Atoi(name[m[2]:m[3]])
			if y >= 1888 && y <= maxYear && m[0] > 0 {
				return cleanTitlePart(name[:m[0]], name), y
			}
		}
	}

	return cleanTitle(name), 0
}

// cleanTitlePart cleans the substring before a year marker. If that comes out
// empty (the year sat at the very start, e.g. a film literally named "2012"),
// it falls back to cleaning the whole name so the title isn't lost.
func cleanTitlePart(part, whole string) string {
	if t := cleanTitle(part); t != "" {
		return t
	}
	return cleanTitle(whole)
}

// cleanTitle strips leading scene noise (www.site - / [group] / {id}), normalizes
// separators to spaces, and drops scene/quality/edition tokens.
func cleanTitle(raw string) string {
	s := reLeadingSite.ReplaceAllString(raw, "")
	for {
		ns := reLeadingTag.ReplaceAllString(s, "")
		if ns == s {
			break
		}
		s = ns
	}

	r := strings.NewReplacer(".", " ", "_", " ", "-", " ")
	s = r.Replace(s)

	words := strings.Fields(s)
	var out []string
	for _, w := range words {
		if movieJunkTokens[strings.ToLower(w)] {
			continue
		}
		out = append(out, w)
	}
	return strings.TrimSpace(strings.Join(out, " "))
}
