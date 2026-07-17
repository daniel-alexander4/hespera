// Package bookscan scans ebook/comic libraries (the `books` vertical) and
// parses the Tier-1 formats clean-room: EPUB and CBZ are plain zip archives
// readable with archive/zip + encoding/xml, and PDF metadata is a bounded
// best-effort scan of the Info dictionary. No calibre source is used — its
// reader is itself a Chromium, so Hespera's app window renders these formats
// natively; this package only extracts identity metadata, reading order, and
// cover images. MOBI/AZW3/FB2/CBR are Tier 2: ingested as rows (filename
// title, placeholder cover) but not parsed or readable.
package bookscan

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"io"
	"net/url"
	"path"
	"strings"
)

// EPUBMeta is what the scanner and the reader need from an EPUB: identity
// metadata, the ordered spine (zip entry names in reading order), and the
// cover image's zip entry.
type EPUBMeta struct {
	Title      string
	Author     string
	CoverEntry string   // zip entry name of the cover image; '' when none
	Spine      []string // zip entry names in reading order
}

type epubContainer struct {
	Rootfiles []struct {
		FullPath  string `xml:"full-path,attr"`
		MediaType string `xml:"media-type,attr"`
	} `xml:"rootfiles>rootfile"`
}

type epubOPF struct {
	Metadata struct {
		Title   []string `xml:"title"`
		Creator []string `xml:"creator"`
		Metas   []struct {
			Name    string `xml:"name,attr"`
			Content string `xml:"content,attr"`
		} `xml:"meta"`
	} `xml:"metadata"`
	Manifest struct {
		Items []struct {
			ID         string `xml:"id,attr"`
			Href       string `xml:"href,attr"`
			MediaType  string `xml:"media-type,attr"`
			Properties string `xml:"properties,attr"`
		} `xml:"item"`
	} `xml:"manifest"`
	Spine struct {
		ItemRefs []struct {
			IDRef string `xml:"idref,attr"`
		} `xml:"itemref"`
	} `xml:"spine"`
}

// ParseEPUB reads an EPUB's container → OPF chain and returns its metadata,
// spine, and cover entry. Every returned entry name is verified to exist in
// the archive, so the reader can serve them by exact-name lookup (no paths
// ever touch the filesystem — zip-slip is impossible by construction).
func ParseEPUB(filePath string) (*EPUBMeta, error) {
	zr, err := zip.OpenReader(filePath)
	if err != nil {
		return nil, fmt.Errorf("open epub: %w", err)
	}
	defer zr.Close()

	names := make(map[string]bool, len(zr.File))
	for _, f := range zr.File {
		names[f.Name] = true
	}

	var c epubContainer
	if err := readZipXML(&zr.Reader, "META-INF/container.xml", &c); err != nil {
		return nil, fmt.Errorf("epub container: %w", err)
	}
	opfPath := ""
	for _, rf := range c.Rootfiles {
		if rf.MediaType == "" || strings.Contains(rf.MediaType, "oebps-package") {
			opfPath = rf.FullPath
			break
		}
	}
	if opfPath == "" || !names[opfPath] {
		return nil, fmt.Errorf("epub container names no package document")
	}

	var opf epubOPF
	if err := readZipXML(&zr.Reader, opfPath, &opf); err != nil {
		return nil, fmt.Errorf("epub package: %w", err)
	}
	opfDir := path.Dir(opfPath)

	// Manifest hrefs are URL-escaped, relative to the OPF's directory.
	resolve := func(href string) string {
		if u, err := url.PathUnescape(href); err == nil {
			href = u
		}
		p := path.Clean(path.Join(opfDir, href))
		if !names[p] {
			return ""
		}
		return p
	}

	meta := &EPUBMeta{}
	if len(opf.Metadata.Title) > 0 {
		meta.Title = strings.TrimSpace(opf.Metadata.Title[0])
	}
	if len(opf.Metadata.Creator) > 0 {
		meta.Author = strings.TrimSpace(opf.Metadata.Creator[0])
	}

	itemHref := make(map[string]string, len(opf.Manifest.Items))
	for _, it := range opf.Manifest.Items {
		itemHref[it.ID] = it.Href
		// EPUB 3 marks the cover with a manifest property.
		if meta.CoverEntry == "" && strings.Contains(it.Properties, "cover-image") {
			meta.CoverEntry = resolve(it.Href)
		}
	}
	// EPUB 2 points at the cover via <meta name="cover" content="<item id>">.
	if meta.CoverEntry == "" {
		for _, m := range opf.Metadata.Metas {
			if strings.EqualFold(m.Name, "cover") {
				if href, ok := itemHref[m.Content]; ok {
					meta.CoverEntry = resolve(href)
				}
				break
			}
		}
	}

	for _, ref := range opf.Spine.ItemRefs {
		if href, ok := itemHref[ref.IDRef]; ok {
			if p := resolve(href); p != "" {
				meta.Spine = append(meta.Spine, p)
			}
		}
	}
	if len(meta.Spine) == 0 {
		return nil, fmt.Errorf("epub has an empty spine")
	}
	return meta, nil
}

func readZipXML(zr *zip.Reader, name string, v any) error {
	rc, err := openZipEntry(zr, name)
	if err != nil {
		return err
	}
	defer rc.Close()
	dec := xml.NewDecoder(io.LimitReader(rc, 8<<20)) // no package doc is 8MiB
	return dec.Decode(v)
}

func openZipEntry(zr *zip.Reader, name string) (io.ReadCloser, error) {
	for _, f := range zr.File {
		if f.Name == name {
			return f.Open()
		}
	}
	return nil, fmt.Errorf("zip entry %q not found", name)
}

// ZipEntry opens one entry of a zip-based book (EPUB/CBZ) by exact name for
// the reader/asset handlers. The closer releases both the entry and the
// archive.
func ZipEntry(filePath, name string) (io.ReadCloser, error) {
	zr, err := zip.OpenReader(filePath)
	if err != nil {
		return nil, fmt.Errorf("open archive: %w", err)
	}
	rc, err := openZipEntry(&zr.Reader, name)
	if err != nil {
		zr.Close()
		return nil, err
	}
	return &zipEntryCloser{rc: rc, zr: zr}, nil
}

type zipEntryCloser struct {
	rc io.ReadCloser
	zr *zip.ReadCloser
}

func (z *zipEntryCloser) Read(p []byte) (int, error) { return z.rc.Read(p) }
func (z *zipEntryCloser) Close() error {
	err := z.rc.Close()
	if cerr := z.zr.Close(); err == nil {
		err = cerr
	}
	return err
}
