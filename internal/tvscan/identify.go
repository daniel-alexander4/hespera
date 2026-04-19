package tvscan

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

type EpisodeIdentity struct {
	ShowTitle      string
	SeasonNumber   int
	EpisodeNumbers []int
	Confidence     float64
	Method         string // "sxe", "x_format", "season_dir"
}

var (
	reSXE     = regexp.MustCompile(`(?i)S(\d{1,2})((?:E\d{1,3}){1,8})\b`)
	reXFormat = regexp.MustCompile(`(?i)(\d{1,2})x(\d{1,3})\b`)
	reEpNums  = regexp.MustCompile(`(?i)E(\d{1,3})`)
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
		epBlock := base[m[4]:m[5]]
		episodes := parseEpisodeNumbers(epBlock)
		title := cleanTitle(base[:m[0]])

		if title != "" {
			return &EpisodeIdentity{
				ShowTitle:      title,
				SeasonNumber:   season,
				EpisodeNumbers: episodes,
				Confidence:     0.72,
				Method:         "sxe",
			}
		}
		// No title from filename — try parent dir
		seasonFromDir, _ := ParseSeasonDir(filepath.Base(dir))
		if seasonFromDir > 0 {
			parentTitle := cleanTitle(filepath.Base(filepath.Dir(dir)))
			return &EpisodeIdentity{
				ShowTitle:      parentTitle,
				SeasonNumber:   season,
				EpisodeNumbers: episodes,
				Confidence:     0.55,
				Method:         "sxe",
			}
		}
		return &EpisodeIdentity{
			SeasonNumber:   season,
			EpisodeNumbers: episodes,
			Confidence:     0.55,
			Method:         "sxe",
		}
	}

	// Try X format: 1x05
	if m := reXFormat.FindStringSubmatchIndex(base); m != nil {
		season, _ := strconv.Atoi(base[m[2]:m[3]])
		ep, _ := strconv.Atoi(base[m[4]:m[5]])
		title := cleanTitle(base[:m[0]])

		if title != "" {
			return &EpisodeIdentity{
				ShowTitle:      title,
				SeasonNumber:   season,
				EpisodeNumbers: []int{ep},
				Confidence:     0.72,
				Method:         "x_format",
			}
		}
		return &EpisodeIdentity{
			SeasonNumber:   season,
			EpisodeNumbers: []int{ep},
			Confidence:     0.55,
			Method:         "x_format",
		}
	}

	// Try season dir fallback — no episode pattern in filename
	dirName := filepath.Base(dir)
	if seasonNum, ok := ParseSeasonDir(dirName); ok {
		parentTitle := cleanTitle(filepath.Base(filepath.Dir(dir)))
		return &EpisodeIdentity{
			ShowTitle:    parentTitle,
			SeasonNumber: seasonNum,
			Confidence:   0.30,
			Method:       "season_dir",
		}
	}

	return nil
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

var reSeasonDir = regexp.MustCompile(`(?i)^season\s*(\d{1,2})$`)

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
