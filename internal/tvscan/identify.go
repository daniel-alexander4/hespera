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
	Method         string // "sxe", "x_format", "airdate", "season_ep", "season_dir"
	// AirDate is set only for Method=="airdate": a "YYYY-MM-DD" date parsed from
	// the filename. SeasonNumber/EpisodeNumbers are unknown at scan time and are
	// resolved later by the TMDB matcher against episode air dates.
	AirDate string
}

var (
	// reSXE matches SxE with optional separators between S and the episode block
	// (S01E01, S01.E01, S01 E01, S01_E01), consecutive multi-episode (S01E01E02),
	// and ranges (S01E01-E02, S01E01-02). The episode-block continuation requires
	// an explicit "E" (consecutive) or a "-" (range) so trailing quality tokens
	// like ".1080p" are never eaten as episodes.
	reSXE = regexp.MustCompile(`(?i)\bS(\d{1,2})[ ._-]?(E\d{1,3}(?:(?:[ ._]?E\d{1,3})|(?:-E?\d{1,3}))*)`)
	// reXFormat matches NxM (1x05) plus ranges (1x05-06, 1x05-x06). Guards against
	// resolution strings: a leading word boundary and a 2-3 digit episode mean
	// 1280x720 / 720x480 / 4x4 never parse as season/episode.
	reXFormat = regexp.MustCompile(`(?i)\b(\d{1,2})x(\d{2,3})(?:-x?(\d{2,3}))?\b`)
	// reAirDate matches an unambiguous year-first date (YYYY-MM-DD / YYYY.MM.DD)
	// for date-based dailies. Year-last and slash forms are deliberately refused
	// as ambiguous (DD.MM vs MM.DD) — a wrong episode is worse than a miss.
	reAirDate = regexp.MustCompile(`\b(20\d{2})[.\-](\d{2})[.\-](\d{2})\b`)

	// Season-dir-only episode markers — only consulted when the file's parent is a
	// season directory, so the season is known and these weak signals can't
	// collide with years/resolutions/title-numbers elsewhere in a library.
	reEpisodeWord = regexp.MustCompile(`(?i)\b(?:episode|ep)[ ._-]?(\d{1,3})\b`)
	reNofM        = regexp.MustCompile(`(?i)\b(\d{1,3})[ ._]?of[ ._]?\d{1,3}\b`)
	reEOnly       = regexp.MustCompile(`(?i)\bE(\d{1,3})\b`)
	reDashNum     = regexp.MustCompile(`[ ._]-[ ._]*(\d{1,3})(?:[ ._]|$)`)
	rePureNum     = regexp.MustCompile(`^(\d{1,2})$`)

	reNum = regexp.MustCompile(`\d{1,3}`)
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

	// Most-specific first: an explicit SxE/x-format/air-date marker in the
	// filename always wins over the weaker season-dir-relative and folder-only
	// fallbacks below.
	if m := reSXE.FindStringSubmatchIndex(base); m != nil {
		season, _ := strconv.Atoi(base[m[2]:m[3]])
		episodes := parseEpisodeBlock(base[m[4]:m[5]])
		title, conf := resolveTitle(dir, cleanTitle(base[:m[0]]))
		return &EpisodeIdentity{
			ShowTitle:      title,
			SeasonNumber:   season,
			EpisodeNumbers: episodes,
			Confidence:     conf,
			Method:         "sxe",
		}
	}

	if m := reXFormat.FindStringSubmatchIndex(base); m != nil {
		season, _ := strconv.Atoi(base[m[2]:m[3]])
		ep, _ := strconv.Atoi(base[m[4]:m[5]])
		episodes := []int{ep}
		if m[6] >= 0 { // range end present: 1x05-06
			if end, err := strconv.Atoi(base[m[6]:m[7]]); err == nil {
				episodes = expandRange(ep, end)
			}
		}
		title, conf := resolveTitle(dir, cleanTitle(base[:m[0]]))
		return &EpisodeIdentity{
			ShowTitle:      title,
			SeasonNumber:   season,
			EpisodeNumbers: episodes,
			Confidence:     conf,
			Method:         "x_format",
		}
	}

	// Year-first air date: date-based dailies (The Daily Show 2024-01-15). Season/
	// episode stay unknown here — the matcher resolves the date against TMDB
	// episode air dates.
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

	// Season-dir-relative markers: only when the parent is a season directory, so
	// the season is authoritative and these weak filename signals (Episode N,
	// E-only, N of M, "- 01 -", a bare number) can't misfire across the library.
	if seasonNum, ok := ParseSeasonDir(filepath.Base(dir)); ok {
		if ep, found := seasonDirEpisode(base); found {
			return &EpisodeIdentity{
				ShowTitle:      showTitleFromSeasonDir(dir),
				SeasonNumber:   seasonNum,
				EpisodeNumbers: []int{ep},
				Confidence:     0.60,
				Method:         "season_ep",
			}
		}
		// No episode marker at all — record the season, leave the episode unknown.
		return &EpisodeIdentity{
			ShowTitle:    showTitleFromSeasonDir(dir),
			SeasonNumber: seasonNum,
			Confidence:   0.30,
			Method:       "season_dir",
		}
	}

	return nil
}

// seasonDirEpisode extracts an episode number from a filename whose season is
// already known from its directory. Patterns are tried most-specific first.
// Packed forms (101, 0101) and bare 3-digit runs are deliberately not treated as
// a pure-number episode — they collide with SxE-packed/years/absolute numbering,
// and a miss is better than a wrong episode.
func seasonDirEpisode(base string) (int, bool) {
	if m := reEpisodeWord.FindStringSubmatch(base); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n, true
	}
	if m := reNofM.FindStringSubmatch(base); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n, true
	}
	if m := reEOnly.FindStringSubmatch(base); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n, true
	}
	if m := reDashNum.FindStringSubmatch(base); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n, true
	}
	if m := rePureNum.FindStringSubmatch(strings.TrimSpace(base)); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n, true
	}
	return 0, false
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

// parseEpisodeBlock turns a matched SxE episode block into episode numbers:
// consecutive markers (E01E02 → 1,2) stay as listed; a two-number block joined
// by '-' is an inclusive range (E01-E03 → 1,2,3).
func parseEpisodeBlock(block string) []int {
	nums := reNum.FindAllString(block, -1)
	ints := make([]int, 0, len(nums))
	for _, s := range nums {
		n, _ := strconv.Atoi(s)
		ints = append(ints, n)
	}
	if len(ints) == 2 && strings.Contains(block, "-") {
		return expandRange(ints[0], ints[1])
	}
	return ints
}

// expandRange returns the inclusive range a..b. A descending or implausibly wide
// range (>50) is treated as just the two endpoints rather than expanded.
func expandRange(a, b int) []int {
	if b < a || b-a > 50 {
		return []int{a, b}
	}
	out := make([]int, 0, b-a+1)
	for i := a; i <= b; i++ {
		out = append(out, i)
	}
	return out
}

// reSeasonDir matches per-season subdirectory names: the short form (s1, S01),
// the long English form (Season 1, season03, Season.1, Season_1), and the
// common non-English forms (Series 2, Saison 1, Staffel 3, Temporada 4). The
// "Specials" folder (season 0) is handled separately in ParseSeasonDir. A bare
// number or an "Extras" folder deliberately does NOT match — those are not
// season directories.
var reSeasonDir = regexp.MustCompile(`(?i)^(?:s|season|series|saison|staffel|temporada)[ ._]*(\d{1,2})$`)

func ParseSeasonDir(dirName string) (int, bool) {
	d := strings.TrimSpace(dirName)
	if strings.EqualFold(d, "Specials") {
		return 0, true
	}
	m := reSeasonDir.FindStringSubmatch(d)
	if m == nil {
		return 0, false
	}
	n, _ := strconv.Atoi(m[1])
	return n, true
}

// junkDirs are subdirectories that hold non-episode extras. They are skipped by
// the scanner ONLY when nested inside a show — a top-level folder of the same
// name (the show "Extras", "Trailer Park Boys" under "Trailers"… ) is kept.
var junkDirs = map[string]bool{
	"sample": true, "samples": true, "featurettes": true, "trailers": true, "extras": true,
}

// IsJunkDirName reports whether a directory name is an extras/sample container.
func IsJunkDirName(name string) bool {
	return junkDirs[strings.ToLower(strings.TrimSpace(name))]
}

// reSampleFile matches a delimited "sample" token in a non-leading position —
// the release-tag marker on sample clips (Show.S01E01.720p.sample.mkv). It is
// deliberately narrow: "sample" is never a leading title word, so a real show is
// never skipped. (trailer/featurette are handled as directories only, so
// "Trailer Park Boys" survives.)
var reSampleFile = regexp.MustCompile(`(?i)[ ._-]sample([ ._-]|$)`)

// IsJunkFile reports whether a filename stem is a sample/extra clip.
func IsJunkFile(stem string) bool {
	return reSampleFile.MatchString(stem)
}

// reLeadingTag strips a leading [group] or {id} tag; reLeadingSite strips a
// leading "www.site -" prefix. Both are scene-release noise ahead of the title.
var (
	reLeadingTag  = regexp.MustCompile(`^\s*[\[\{][^\]\}]*[\]\}]\s*`)
	reLeadingSite = regexp.MustCompile(`(?i)^\s*www\.[^\s]+\s*-\s*`)
)

func cleanTitle(raw string) string {
	s := raw
	// Strip leading scene noise (a www.site - prefix, then any [group]/{id} tags)
	// before separator normalization, while the brackets are still intact.
	s = reLeadingSite.ReplaceAllString(s, "")
	for {
		ns := reLeadingTag.ReplaceAllString(s, "")
		if ns == s {
			break
		}
		s = ns
	}

	// Replace dots, underscores, hyphens with spaces.
	r := strings.NewReplacer(".", " ", "_", " ", "-", " ")
	s = r.Replace(s)

	// Remove quality tokens.
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
