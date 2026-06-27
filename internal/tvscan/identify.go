package tvscan

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type EpisodeIdentity struct {
	ShowTitle      string
	SeasonNumber   int
	EpisodeNumbers []int
	Confidence     float64
	Method         string // "sxe", "x_format", "airdate", "season_dir"
	// AirDate is set only for Method=="airdate": a "YYYY-MM-DD" date parsed from
	// the filename. SeasonNumber/EpisodeNumbers are unknown at scan time and are
	// resolved later by the TMDB matcher against episode air dates.
	AirDate string
}

var (
	reSXE     = regexp.MustCompile(`(?i)S(\d{1,2})((?:E\d{1,3}){1,8})\b`)
	reXFormat = regexp.MustCompile(`(?i)(\d{1,2})x(\d{1,3})\b`)
	reEpNums  = regexp.MustCompile(`(?i)E(\d{1,3})`)
	// reAirDate matches an unambiguous year-first date (YYYY-MM-DD / YYYY.MM.DD)
	// for date-based dailies. Year-last and slash forms are deliberately refused
	// as ambiguous (DD.MM vs MM.DD) — a wrong episode is worse than a miss.
	reAirDate = regexp.MustCompile(`\b(20\d{2})[.\-](\d{2})[.\-](\d{2})\b`)
)

var qualityTokens = map[string]bool{
	"2160p": true, "1080p": true, "720p": true, "480p": true,
	"x264": true, "x265": true, "h264": true, "h265": true,
	"web-dl": true, "bluray": true, "remux": true,
	"proper": true, "repack": true, "hdtv": true, "webrip": true,
}

func IdentifyFile(absPath string) *EpisodeIdentity {
	dir := filepath.Dir(absPath)
	base := strings.TrimSuffix(filepath.Base(absPath), filepath.Ext(absPath))

	// Try SXE pattern first: S01E01, S02E01E02
	if m := reSXE.FindStringSubmatchIndex(base); m != nil {
		season, _ := strconv.Atoi(base[m[2]:m[3]])
		episodes := parseEpisodeNumbers(base[m[4]:m[5]])
		title, conf := resolveTitle(dir, cleanTitle(base[:m[0]]))
		return &EpisodeIdentity{
			ShowTitle:      title,
			SeasonNumber:   season,
			EpisodeNumbers: episodes,
			Confidence:     conf,
			Method:         "sxe",
		}
	}

	// Try X format: 1x05
	if m := reXFormat.FindStringSubmatchIndex(base); m != nil {
		season, _ := strconv.Atoi(base[m[2]:m[3]])
		ep, _ := strconv.Atoi(base[m[4]:m[5]])
		title, conf := resolveTitle(dir, cleanTitle(base[:m[0]]))
		return &EpisodeIdentity{
			ShowTitle:      title,
			SeasonNumber:   season,
			EpisodeNumbers: []int{ep},
			Confidence:     conf,
			Method:         "x_format",
		}
	}

	// Try a year-first air date: date-based dailies (The Daily Show 2024-01-15).
	// Runs after SxE/x_format so an explicit episode marker always wins, and
	// before the weak season-dir fallback. Season/episode stay unknown here —
	// the matcher resolves the date against TMDB episode air dates.
	if m := reAirDate.FindStringSubmatchIndex(base); m != nil {
		if date, ok := parseAirDate(base[m[2]:m[3]], base[m[4]:m[5]], base[m[6]:m[7]]); ok {
			title, _ := resolveTitle(dir, cleanTitle(base[:m[0]]))
			return &EpisodeIdentity{
				ShowTitle:    title,
				SeasonNumber: -1,
				Confidence:   0.50,
				Method:       "airdate",
				AirDate:      date,
			}
		}
	}

	// Try season dir fallback — no episode pattern in filename
	if seasonNum, ok := ParseSeasonDir(filepath.Base(dir)); ok {
		return &EpisodeIdentity{
			ShowTitle:    showTitleFromSeasonDir(dir),
			SeasonNumber: seasonNum,
			Confidence:   0.30,
			Method:       "season_dir",
		}
	}

	return nil
}

// resolveTitle picks the show title for an episode whose season/episode were
// parsed from the filename. A season-directory layout (Show/Season 1/, Show/s1/)
// is authoritative for show identity, so the show folder — the season dir's
// parent — wins over any title parsed from the filename. That unifies a show
// whose files use inconsistent naming (e.g. one with a "(2025)" year and one
// without) under a single grouping key, while a library that distinguishes two
// same-named shows by folder year keeps them apart. With no season directory,
// the filename title is used. Confidence is 0.72 when the filename carried a
// title, 0.55 when it had to be inferred from the directory.
func resolveTitle(dir, fileTitle string) (string, float64) {
	if dirTitle := showTitleFromSeasonDir(dir); dirTitle != "" {
		if fileTitle != "" {
			return dirTitle, 0.72
		}
		return dirTitle, 0.55
	}
	if fileTitle != "" {
		return fileTitle, 0.72
	}
	return "", 0.55
}

// showTitleFromSeasonDir returns the show title for a file whose parent is a
// season directory: the season dir's own parent. Returns "" when the parent is
// not a season directory, or when the show folder resolves to a filesystem
// root rather than a real show name (so callers never manufacture a title from
// an arbitrary container folder).
func showTitleFromSeasonDir(dir string) string {
	if _, ok := ParseSeasonDir(filepath.Base(dir)); !ok {
		return ""
	}
	base := filepath.Base(filepath.Dir(dir))
	if base == "/" || base == "." {
		return ""
	}
	return cleanTitle(base)
}

// parseAirDate validates a year/month/day triple and returns it normalized to
// "YYYY-MM-DD". It rejects impossible dates (e.g. month 13, day 45) so a stray
// numeric run in a release string isn't mistaken for an air date.
func parseAirDate(year, month, day string) (string, bool) {
	norm := year + "-" + month + "-" + day
	if _, err := time.Parse("2006-01-02", norm); err != nil {
		return "", false
	}
	return norm, true
}

func parseEpisodeNumbers(block string) []int {
	matches := reEpNums.FindAllStringSubmatch(block, -1)
	eps := make([]int, 0, len(matches))
	for _, m := range matches {
		n, _ := strconv.Atoi(m[1])
		eps = append(eps, n)
	}
	return eps
}

// reSeasonDir matches both the long form (Season 1, season03) and the short
// form (s1, S01) used for per-season subdirectories.
var reSeasonDir = regexp.MustCompile(`(?i)^s(?:eason)?\s*(\d{1,2})$`)

func ParseSeasonDir(dirName string) (int, bool) {
	m := reSeasonDir.FindStringSubmatch(strings.TrimSpace(dirName))
	if m == nil {
		return 0, false
	}
	n, _ := strconv.Atoi(m[1])
	return n, true
}

func cleanTitle(raw string) string {
	// Replace dots, underscores, hyphens with spaces
	r := strings.NewReplacer(".", " ", "_", " ", "-", " ")
	s := r.Replace(raw)

	// Remove quality tokens
	words := strings.Fields(s)
	var out []string
	for _, w := range words {
		if qualityTokens[strings.ToLower(w)] {
			continue
		}
		out = append(out, w)
	}
	return strings.TrimSpace(strings.Join(out, " "))
}
