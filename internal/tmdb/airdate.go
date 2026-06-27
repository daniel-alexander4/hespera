package tmdb

import (
	"sort"
	"strconv"
	"strings"
)

// airDateIndex maps a "YYYY-MM-DD" air date to the (season, episode) pairs that
// aired that day, built from TMDB season metadata. Specials (season 0) are
// excluded because they frequently share an air date with a regular-season
// premiere and would otherwise shadow the real episode.
type airDateIndex map[string][]seasonEpisode

type seasonEpisode struct {
	season  int
	episode int
}

// add folds one season's episodes into the index. Season 0 and episodes with no
// air date are skipped.
func (idx airDateIndex) add(seasonNum int, episodes []TVEpisode) {
	if seasonNum < 1 {
		return
	}
	for _, ep := range episodes {
		if ep.AirDate == "" {
			continue
		}
		idx[ep.AirDate] = append(idx[ep.AirDate], seasonEpisode{seasonNum, ep.EpisodeNumber})
	}
}

// resolve maps an air date to its episode CSV, but only when every episode that
// aired that day belongs to a single season. A date that hits no episode, or
// episodes spanning more than one season, is refused (ok=false) — leaving the
// file unresolved is safer than guessing the wrong episode.
func (idx airDateIndex) resolve(date string) (season int, csv string, ok bool) {
	hits := idx[date]
	if len(hits) == 0 {
		return 0, "", false
	}
	season = hits[0].season
	eps := make([]int, 0, len(hits))
	for _, h := range hits {
		if h.season != season {
			return 0, "", false
		}
		eps = append(eps, h.episode)
	}
	sort.Ints(eps)
	return season, intsToCSV(eps), true
}

// intsToCSV joins episode numbers as a comma-separated string, dropping
// duplicates while preserving (sorted) order.
func intsToCSV(nums []int) string {
	parts := make([]string, 0, len(nums))
	prev := 0
	for i, n := range nums {
		if i > 0 && n == prev {
			continue
		}
		parts = append(parts, strconv.Itoa(n))
		prev = n
	}
	return strings.Join(parts, ",")
}
