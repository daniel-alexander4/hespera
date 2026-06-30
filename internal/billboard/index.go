// Package billboard exposes the embedded Billboard Hot 100 weekly index: for
// each covered year, the full week-by-week grid of charts — every weekly Hot
// 100 with the ordered list of (position, song, artist).
//
// The data is a gzipped JSON map (data.json.gz) built offline from the public
// weekly Hot 100 archive by gen.go (`go generate ./internal/billboard`). It is
// factual chart data, decoded once on first use and held in memory. Per-song
// facts (peak, weeks, debut) are derived from the grid by callers, so the
// weekly grid is the single source of truth.
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

// ChartEntry is one song's placement on a single weekly chart.
type ChartEntry struct {
	Pos    int    `json:"p"` // Hot 100 position that week (1 = top)
	Title  string `json:"t"`
	Artist string `json:"a"`
}

// WeeklyChart is one weekly Hot 100: a chart date and its ordered entries.
type WeeklyChart struct {
	Date    string       `json:"d"` // YYYY-MM-DD chart date
	Entries []ChartEntry `json:"e"` // ordered by position ascending (1..N)
}

var (
	once    sync.Once
	byYear  map[int][]WeeklyChart
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
		var keyed map[string][]WeeklyChart
		if err := json.Unmarshal(raw, &keyed); err != nil {
			loadErr = err
			return
		}
		byYear = make(map[int][]WeeklyChart, len(keyed))
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

// WeeklyCharts returns year y's weekly Hot 100 charts in chronological order,
// or nil if the year is outside the dataset.
func WeeklyCharts(y int) []WeeklyChart {
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
