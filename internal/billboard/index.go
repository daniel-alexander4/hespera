// Package billboard exposes a weekly Hot 100 index: for each covered year, the
// full week-by-week grid of charts — every weekly chart with the ordered list of
// (position, song, artist).
//
// The data is NOT embedded or shipped with Hespera. It is a gzipped JSON map
// (DataDir/billboard/data.json.gz) fetched at runtime by BuildIndex from the
// public weekly archive, only when the user enables the "Rediscover a Year"
// feature (which carries a licensing notice — the chart data is a third party's
// intellectual property). The package loads it lazily from disk on first use and
// reloads when the file changes, so a fetch that lands after startup is picked up
// without a restart. When the file is absent the feature reports no data rather
// than failing.
package billboard

import (
	"bytes"
	"compress/gzip"
	"encoding/csv"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// archiveURL is the public Hot 100 weekly archive (1958–2021). Factual chart
// data; columns: chart_date,current_position,title,performer,previous_position,
// peak_position,weeks_on_chart.
const archiveURL = "https://raw.githubusercontent.com/utdata/rwd-billboard-data/main/data-out/hot100_archive_1958_2021.csv"

// ChartEntry is one song's placement on a single weekly chart.
type ChartEntry struct {
	Pos    int    `json:"p"` // chart position that week (1 = top)
	Title  string `json:"t"`
	Artist string `json:"a"`
}

// WeeklyChart is one weekly chart: a chart date and its ordered entries.
type WeeklyChart struct {
	Date    string       `json:"d"` // YYYY-MM-DD chart date
	Entries []ChartEntry `json:"e"` // ordered by position ascending (1..N)
}

// index is a parsed dataset cached in memory, tagged with the source file's
// stat so a changed file (a re-fetch) is reloaded.
type index struct {
	byYear map[int][]WeeklyChart
	years  []int // sorted ascending
	path   string
	size   int64
	mtime  time.Time
}

var (
	mu     sync.RWMutex
	cached *index
)

// DataFile is the on-disk location of the fetched weekly index.
func DataFile(dataDir string) string {
	return filepath.Join(dataDir, "billboard", "data.json.gz")
}

// ensure loads (or reloads) the dataset from disk, returning nil when the file is
// absent or unreadable — the caller treats nil as "no data" (feature disabled or
// not yet fetched). Absence is never cached, so a later fetch is picked up.
func ensure(dataDir string) *index {
	path := DataFile(dataDir)
	fi, err := os.Stat(path)
	if err != nil {
		return nil
	}
	mu.RLock()
	c := cached
	mu.RUnlock()
	if c != nil && c.path == path && c.size == fi.Size() && c.mtime.Equal(fi.ModTime()) {
		return c
	}
	nc, err := loadFile(path)
	if err != nil {
		return nil
	}
	nc.path, nc.size, nc.mtime = path, fi.Size(), fi.ModTime()
	mu.Lock()
	cached = nc
	mu.Unlock()
	return nc
}

func loadFile(path string) (*index, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	gr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	defer gr.Close()
	data, err := io.ReadAll(gr)
	if err != nil {
		return nil, err
	}
	var keyed map[string][]WeeklyChart
	if err := json.Unmarshal(data, &keyed); err != nil {
		return nil, err
	}
	nc := &index{byYear: make(map[int][]WeeklyChart, len(keyed))}
	for ys, list := range keyed {
		y, err := strconv.Atoi(ys)
		if err != nil {
			continue
		}
		nc.byYear[y] = list
		nc.years = append(nc.years, y)
	}
	sort.Ints(nc.years)
	return nc, nil
}

// WeeklyCharts returns year y's weekly charts in chronological order, or nil if
// the year is outside the dataset (or the dataset isn't present).
func WeeklyCharts(dataDir string, y int) []WeeklyChart {
	c := ensure(dataDir)
	if c == nil {
		return nil
	}
	return c.byYear[y]
}

// YearSong is one distinct song that appeared on the Hot 100 during a given
// year, with the best position it reached and how many weekly charts it appeared
// on that year.
type YearSong struct {
	Title  string `json:"t"`
	Artist string `json:"a"`
	Peak   int    `json:"p"` // best (lowest-numbered) weekly position that year
	Weeks  int    `json:"w"` // distinct weekly charts it appeared on that year
}

// YearChart returns every distinct song that charted on the Hot 100 during year
// y — the "everything that charted this year" list — ordered by peak position
// (best first), ties broken by more weeks-on-chart then title. It is derived
// entirely from the weekly grid (no extra data or licensing surface). Returns
// nil when the year (or the dataset) is absent.
func YearChart(dataDir string, y int) []YearSong {
	weeks := WeeklyCharts(dataDir, y)
	if len(weeks) == 0 {
		return nil
	}
	idx := make(map[string]*YearSong, 1024)
	order := make([]*YearSong, 0, 1024)
	for _, wk := range weeks {
		for _, e := range wk.Entries {
			k := strings.ToLower(strings.TrimSpace(e.Title)) + "\x1f" + strings.ToLower(strings.TrimSpace(e.Artist))
			s := idx[k]
			if s == nil {
				s = &YearSong{Title: e.Title, Artist: e.Artist, Peak: e.Pos}
				idx[k] = s
				order = append(order, s)
			}
			if e.Pos < s.Peak {
				s.Peak = e.Pos
			}
			s.Weeks++
		}
	}
	out := make([]YearSong, len(order))
	for i, s := range order {
		out[i] = *s
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Peak != out[j].Peak {
			return out[i].Peak < out[j].Peak
		}
		if out[i].Weeks != out[j].Weeks {
			return out[i].Weeks > out[j].Weeks
		}
		return out[i].Title < out[j].Title
	})
	return out
}

// Years returns the inclusive [min, max] year range covered by the dataset. ok
// is false when the dataset is absent or empty (the feature is off or the data
// hasn't been fetched yet).
func Years(dataDir string) (min, max int, ok bool) {
	c := ensure(dataDir)
	if c == nil || len(c.years) == 0 {
		return 0, 0, false
	}
	return c.years[0], c.years[len(c.years)-1], true
}

// BuildIndex fetches the public weekly archive (or reads localCSV when non-empty)
// and writes the gzipped per-year weekly index to DataFile(dataDir). It is the
// runtime equivalent of the old offline generator — invoked by a background job
// when the user enables the feature, so the data lands in the user's DataDir
// (never in Hespera's repo or distributed binary). Temp-file + rename, so a
// concurrent reader never sees a half-written file; the in-memory cache is
// invalidated so the next read reloads.
func BuildIndex(dataDir, localCSV string) error {
	var r io.ReadCloser
	if localCSV != "" {
		f, err := os.Open(localCSV)
		if err != nil {
			return err
		}
		r = f
	} else {
		client := &http.Client{Timeout: 5 * time.Minute}
		resp, err := client.Get(archiveURL)
		if err != nil {
			return err
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return &os.PathError{Op: "get", Path: archiveURL, Err: errStatus(resp.StatusCode)}
		}
		r = resp.Body
	}
	defer r.Close()

	out, err := parseArchive(r)
	if err != nil {
		return err
	}

	dir := filepath.Join(dataDir, "billboard")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "data-*.json.gz")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	gw := gzip.NewWriter(tmp)
	encErr := json.NewEncoder(gw).Encode(out)
	closeErr := gw.Close()
	tmp.Close()
	if encErr != nil || closeErr != nil {
		os.Remove(tmpName)
		if encErr != nil {
			return encErr
		}
		return closeErr
	}
	if err := os.Rename(tmpName, DataFile(dataDir)); err != nil {
		os.Remove(tmpName)
		return err
	}
	mu.Lock()
	cached = nil
	mu.Unlock()
	return nil
}

// parseArchive reads the Hot 100 CSV and groups it into the per-year weekly grid.
func parseArchive(r io.Reader) (map[string][]WeeklyChart, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	header, err := cr.Read()
	if err != nil {
		return nil, err
	}
	if len(header) < 4 {
		return nil, &os.PathError{Op: "parse", Path: "archive", Err: errStatus(0)}
	}
	// year -> chart_date -> ordered entries
	years := map[string]map[string][]ChartEntry{}
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(rec) < 4 {
			continue
		}
		date, posStr, title, performer := rec[0], rec[1], rec[2], rec[3]
		if len(date) < 4 || title == "" || performer == "" {
			continue
		}
		pos, _ := strconv.Atoi(posStr)
		if pos <= 0 {
			continue
		}
		y := date[:4]
		wm := years[y]
		if wm == nil {
			wm = map[string][]ChartEntry{}
			years[y] = wm
		}
		wm[date] = append(wm[date], ChartEntry{Pos: pos, Title: title, Artist: performer})
	}
	out := map[string][]WeeklyChart{}
	for y, wm := range years {
		dates := make([]string, 0, len(wm))
		for d := range wm {
			dates = append(dates, d)
		}
		sort.Strings(dates) // ISO dates sort chronologically
		weeks := make([]WeeklyChart, 0, len(dates))
		for _, d := range dates {
			entries := wm[d]
			sort.Slice(entries, func(i, j int) bool { return entries[i].Pos < entries[j].Pos })
			weeks = append(weeks, WeeklyChart{Date: d, Entries: entries})
		}
		out[y] = weeks
	}
	return out, nil
}

type errStatus int

func (e errStatus) Error() string { return "unexpected status " + strconv.Itoa(int(e)) }
