//go:build ignore

// Command gen builds the embedded Billboard Hot 100 weekly index
// (data.json.gz) consumed by package billboard.
//
// It reads the public Billboard Hot 100 weekly archive (1958–2021) and keeps
// the full weekly grid: for each weekly chart it records the ordered list of
// (position, song, artist). The result is grouped by year and written as a
// gzipped JSON map keyed by year string — each year is the list of its weekly
// charts in chronological order. Per-song facts (peak, weeks-on-chart, debut)
// are derived from this grid at read time, so the grid is the single source.
//
// Data source (factual chart data; weekly Hot 100 archive):
//
//	https://raw.githubusercontent.com/utdata/rwd-billboard-data/main/data-out/hot100_archive_1958_2021.csv
//
// Columns: chart_date,current_position,title,performer,previous_position,peak_position,weeks_on_chart
//
// Usage:
//
//	go run gen.go                 # downloads the archive, writes data.json.gz
//	go run gen.go -in archive.csv # use a local copy instead of downloading
//
// Regenerate with `go generate ./internal/billboard`.
package main

import (
	"compress/gzip"
	"encoding/csv"
	"encoding/json"
	"flag"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"time"
)

const archiveURL = "https://raw.githubusercontent.com/utdata/rwd-billboard-data/main/data-out/hot100_archive_1958_2021.csv"

// Compact JSON keys keep the embedded grid small (it repeats per weekly row).
type chartEntry struct {
	Pos    int    `json:"p"`
	Title  string `json:"t"`
	Artist string `json:"a"`
}

type weeklyChart struct {
	Date    string       `json:"d"` // YYYY-MM-DD chart date
	Entries []chartEntry `json:"e"` // ordered by position ascending
}

func main() {
	in := flag.String("in", "", "path to the Hot 100 archive CSV (downloads the public archive if empty)")
	out := flag.String("out", "data.json.gz", "output path for the gzipped per-year weekly index")
	flag.Parse()

	var r io.ReadCloser
	if *in != "" {
		f, err := os.Open(*in)
		if err != nil {
			log.Fatalf("open %s: %v", *in, err)
		}
		r = f
	} else {
		log.Printf("downloading %s", archiveURL)
		client := &http.Client{Timeout: 5 * time.Minute}
		resp, err := client.Get(archiveURL)
		if err != nil {
			log.Fatalf("download: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			log.Fatalf("download: HTTP %d", resp.StatusCode)
		}
		r = resp.Body
	}
	defer r.Close()

	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	header, err := cr.Read()
	if err != nil {
		log.Fatalf("read header: %v", err)
	}
	if len(header) < 7 {
		log.Fatalf("unexpected header: %v", header)
	}

	// year -> chart_date -> ordered entries
	type weekKey = string
	years := map[string]map[weekKey][]chartEntry{}

	rows := 0
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Fatalf("read row %d: %v", rows, err)
		}
		rows++
		date, posStr, title, performer := rec[0], rec[1], rec[2], rec[3]
		if len(date) < 4 || title == "" || performer == "" {
			continue
		}
		pos, _ := strconv.Atoi(posStr)
		if pos <= 0 {
			continue
		}
		year := date[:4]
		wm := years[year]
		if wm == nil {
			wm = map[weekKey][]chartEntry{}
			years[year] = wm
		}
		wm[date] = append(wm[date], chartEntry{Pos: pos, Title: title, Artist: performer})
	}
	log.Printf("read %d weekly rows across %d years", rows, len(years))

	out2 := map[string][]weeklyChart{}
	for year, wm := range years {
		dates := make([]string, 0, len(wm))
		for d := range wm {
			dates = append(dates, d)
		}
		sort.Strings(dates) // ISO dates sort chronologically
		weeks := make([]weeklyChart, 0, len(dates))
		for _, d := range dates {
			entries := wm[d]
			sort.Slice(entries, func(i, j int) bool { return entries[i].Pos < entries[j].Pos })
			weeks = append(weeks, weeklyChart{Date: d, Entries: entries})
		}
		out2[year] = weeks
	}

	f, err := os.Create(*out)
	if err != nil {
		log.Fatalf("create %s: %v", *out, err)
	}
	defer f.Close()
	gw := gzip.NewWriter(f)
	enc := json.NewEncoder(gw)
	if err := enc.Encode(out2); err != nil {
		log.Fatalf("encode: %v", err)
	}
	if err := gw.Close(); err != nil {
		log.Fatalf("gzip close: %v", err)
	}
	log.Printf("wrote %s", *out)
}
