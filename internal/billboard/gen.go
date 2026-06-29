//go:build ignore

// Command gen builds the embedded Billboard Hot 100 per-year index
// (data.json.gz) consumed by package billboard.
//
// It reads the public Billboard Hot 100 weekly archive (1958–present) and
// collapses the ~330k weekly chart rows into one entry per (year, artist,
// song), keeping each song's peak position, total weeks on chart, and the
// date it first appeared during that year. The result is grouped by artist
// within each year and written as a gzipped JSON map keyed by year.
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

type song struct {
	Title string `json:"title"`
	Peak  int    `json:"peak"`
	Weeks int    `json:"weeks"`
	Debut string `json:"debut"`
}

type artist struct {
	Name  string `json:"artist"`
	Peak  int    `json:"peak"`
	Songs []song `json:"songs"`
}

func main() {
	in := flag.String("in", "", "path to the Hot 100 archive CSV (downloads the public archive if empty)")
	out := flag.String("out", "data.json.gz", "output path for the gzipped per-year index")
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

	// year -> artist -> title -> aggregate
	type agg struct {
		peak, weeks int
		debut       string
	}
	years := map[string]map[string]map[string]*agg{}

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
		date, title, performer := rec[0], rec[2], rec[3]
		if len(date) < 4 || title == "" || performer == "" {
			continue
		}
		year := date[:4]
		peak, _ := strconv.Atoi(rec[5])
		weeks, _ := strconv.Atoi(rec[6])
		if peak <= 0 {
			peak, _ = strconv.Atoi(rec[1]) // fall back to the week position
		}

		am := years[year]
		if am == nil {
			am = map[string]map[string]*agg{}
			years[year] = am
		}
		tm := am[performer]
		if tm == nil {
			tm = map[string]*agg{}
			am[performer] = tm
		}
		a := tm[title]
		if a == nil {
			a = &agg{peak: peak, weeks: weeks, debut: date}
			tm[title] = a
			continue
		}
		if peak > 0 && (a.peak == 0 || peak < a.peak) {
			a.peak = peak
		}
		if weeks > a.weeks {
			a.weeks = weeks
		}
		if date < a.debut {
			a.debut = date
		}
	}
	log.Printf("read %d weekly rows across %d years", rows, len(years))

	out2 := map[string][]artist{}
	for year, am := range years {
		list := make([]artist, 0, len(am))
		for name, tm := range am {
			songs := make([]song, 0, len(tm))
			best := 0
			for title, a := range tm {
				songs = append(songs, song{Title: title, Peak: a.peak, Weeks: a.weeks, Debut: a.debut})
				if a.peak > 0 && (best == 0 || a.peak < best) {
					best = a.peak
				}
			}
			sort.Slice(songs, func(i, j int) bool {
				if songs[i].Peak != songs[j].Peak {
					return songs[i].Peak < songs[j].Peak
				}
				return songs[i].Title < songs[j].Title
			})
			list = append(list, artist{Name: name, Peak: best, Songs: songs})
		}
		sort.Slice(list, func(i, j int) bool {
			if list[i].Peak != list[j].Peak {
				return list[i].Peak < list[j].Peak
			}
			return list[i].Name < list[j].Name
		})
		out2[year] = list
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
