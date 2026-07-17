package bookscan

import (
	"archive/zip"
	"fmt"
	"path"
	"sort"
	"strings"
)

var cbzImageExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true,
	".webp": true, ".avif": true, ".bmp": true,
}

// CBZPages lists a comic archive's image entries in natural reading order —
// digit runs compare numerically, so page9 sorts before page10 even without
// zero-padding. Junk entries (directories, dotfiles, __MACOSX resource forks,
// non-images) are skipped.
func CBZPages(filePath string) ([]string, error) {
	zr, err := zip.OpenReader(filePath)
	if err != nil {
		return nil, fmt.Errorf("open cbz: %w", err)
	}
	defer zr.Close()

	var pages []string
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		base := path.Base(f.Name)
		if strings.HasPrefix(base, ".") || strings.HasPrefix(f.Name, "__MACOSX/") {
			continue
		}
		if cbzImageExts[strings.ToLower(path.Ext(base))] {
			pages = append(pages, f.Name)
		}
	}
	if len(pages) == 0 {
		return nil, fmt.Errorf("cbz contains no images")
	}
	sort.Slice(pages, func(i, j int) bool { return naturalLess(pages[i], pages[j]) })
	return pages, nil
}

// naturalLess orders strings case-insensitively with digit runs compared as
// numbers ("page2" < "page10").
func naturalLess(a, b string) bool {
	a, b = strings.ToLower(a), strings.ToLower(b)
	for a != "" && b != "" {
		if isDigit(a[0]) != isDigit(b[0]) {
			return a < b
		}
		ad, an := splitLead(a)
		bd, bn := splitLead(b)
		if isDigit(a[0]) {
			// Numeric runs compare as numbers: shorter zero-trimmed run is
			// smaller; equal lengths compare lexicographically. Equal values
			// (e.g. "007" vs "7") fall through to the remainders.
			at, bt := strings.TrimLeft(ad, "0"), strings.TrimLeft(bd, "0")
			if len(at) != len(bt) {
				return len(at) < len(bt)
			}
			if at != bt {
				return at < bt
			}
		} else if ad != bd {
			return ad < bd
		}
		a, b = an, bn
	}
	return a == "" && b != ""
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// splitLead splits off the leading run of s — all digits or all non-digits.
func splitLead(s string) (lead, rest string) {
	if s == "" {
		return "", ""
	}
	d := isDigit(s[0])
	i := 1
	for i < len(s) && isDigit(s[i]) == d {
		i++
	}
	return s[:i], s[i:]
}
