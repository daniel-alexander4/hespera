// Package billboard exposes the embedded Billboard Hot 100 per-year index:
// which artists and songs charted in a given year, with each song's peak
// position, weeks on chart, and the date it first appeared that year.
//
// The data is a gzipped JSON map (data.json.gz) built offline from the public
// weekly Hot 100 archive by gen.go (`go generate ./internal/billboard`). It is
// factual chart data, decoded once on first use and held in memory.
package billboard

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"io"
	"sort"
	"strconv"
	"sync"
)

//go:generate go run gen.go

//go:embed data.json.gz
var gzData []byte

// Song is one charting single by an artist in a year.
type Song struct {
	Title string `json:"title"`
	Peak  int    `json:"peak"`  // best (lowest) Hot 100 position reached
	Weeks int    `json:"weeks"` // total weeks on the chart
	Debut string `json:"debut"` // YYYY-MM-DD it first appeared that year
}

// Artist is one act that charted in a year, with all of its charting songs
// (sorted by peak, best first) and the act's overall best peak.
type Artist struct {
	Name  string `json:"artist"`
	Peak  int    `json:"peak"`
	Songs []Song `json:"songs"`
}

var (
	once    sync.Once
	byYear  map[int][]Artist
	years   []int // sorted ascending
	loadErr error
)

func load() {
	once.Do(func() {
		gr, err := gzip.NewReader(bytes.NewReader(gzData))
		if err != nil {
			loadErr = err
			return
		}
		defer gr.Close()
		raw, err := io.ReadAll(gr)
		if err != nil {
			loadErr = err
			return
		}
		var keyed map[string][]Artist
		if err := json.Unmarshal(raw, &keyed); err != nil {
			loadErr = err
			return
		}
		byYear = make(map[int][]Artist, len(keyed))
		for ys, list := range keyed {
			y, err := strconv.Atoi(ys)
			if err != nil {
				continue
			}
			byYear[y] = list
			years = append(years, y)
		}
		sort.Ints(years)
	})
}

// Year returns the artists who charted in year y, ordered by chart peak (the
// biggest acts first), or nil if the year is outside the dataset.
func Year(y int) []Artist {
	load()
	return byYear[y]
}

// Years returns the inclusive [min, max] year range covered by the dataset.
// ok is false if the dataset failed to load or is empty.
func Years() (min, max int, ok bool) {
	load()
	if loadErr != nil || len(years) == 0 {
		return 0, 0, false
	}
	return years[0], years[len(years)-1], true
}
