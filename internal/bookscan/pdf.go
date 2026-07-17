package bookscan

import (
	"bytes"
	"os"
	"strings"
	"unicode/utf16"
)

// pdfMetaScanLimit bounds how much of a PDF is read looking for the Info
// dictionary: the head plus the tail, where the trailer (and usually Info)
// live. Anything past that — encrypted files, xref-stream-only files with
// compressed Info objects — silently yields no metadata.
const pdfMetaScanLimit = 256 << 10

// PDFMeta best-effort extracts Title/Author from a PDF's Info dictionary.
// Deliberately shallow: a real PDF parser is out of scope (calibre itself
// shells out to poppler for this), and the reader doesn't need one — Chromium
// renders the PDF natively. Empty strings mean "derive from the filename".
func PDFMeta(filePath string) (title, author string) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", ""
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", ""
	}

	// Read head and tail windows (they overlap into one read on small files).
	var data []byte
	if st.Size() <= 2*pdfMetaScanLimit {
		data, _ = os.ReadFile(filePath)
	} else {
		head := make([]byte, pdfMetaScanLimit)
		n, _ := f.ReadAt(head, 0)
		tail := make([]byte, pdfMetaScanLimit)
		m, _ := f.ReadAt(tail, st.Size()-pdfMetaScanLimit)
		data = append(head[:n], tail[:m]...)
	}
	if !bytes.HasPrefix(data, []byte("%PDF")) {
		return "", ""
	}
	// Last occurrence wins: incremental updates append, so the newest Info
	// dictionary is nearest the end.
	return pdfInfoString(data, "/Title"), pdfInfoString(data, "/Author")
}

// pdfInfoString finds the last `/Key (literal)` or `/Key <hex>` value in data.
func pdfInfoString(data []byte, key string) string {
	for i := bytes.LastIndex(data, []byte(key)); i >= 0; i = bytes.LastIndex(data[:i], []byte(key)) {
		rest := data[i+len(key):]
		// Skip whitespace between the key and its value.
		j := 0
		for j < len(rest) && (rest[j] == ' ' || rest[j] == '\r' || rest[j] == '\n' || rest[j] == '\t') {
			j++
		}
		if j >= len(rest) {
			continue
		}
		var s string
		var ok bool
		switch rest[j] {
		case '(':
			s, ok = pdfLiteralString(rest[j:])
		case '<':
			s, ok = pdfHexString(rest[j:])
		}
		if ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// pdfLiteralString decodes a PDF literal string `(...)` with backslash escapes
// and balanced nested parentheses.
func pdfLiteralString(b []byte) (string, bool) {
	if len(b) == 0 || b[0] != '(' {
		return "", false
	}
	var out []byte
	depth := 1
	for i := 1; i < len(b) && i < 4096; i++ {
		c := b[i]
		switch c {
		case '\\':
			if i+1 < len(b) {
				i++
				switch b[i] {
				case 'n':
					out = append(out, '\n')
				case 'r':
					out = append(out, '\r')
				case 't':
					out = append(out, '\t')
				default:
					out = append(out, b[i])
				}
			}
		case '(':
			depth++
			out = append(out, c)
		case ')':
			depth--
			if depth == 0 {
				return decodePDFText(out), true
			}
			out = append(out, c)
		default:
			out = append(out, c)
		}
	}
	return "", false
}

// pdfHexString decodes a PDF hex string `<...>`.
func pdfHexString(b []byte) (string, bool) {
	end := bytes.IndexByte(b, '>')
	if len(b) == 0 || b[0] != '<' || end < 0 || end > 4096 {
		return "", false
	}
	hexDigits := make([]byte, 0, end-1)
	for _, c := range b[1:end] {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
			hexDigits = append(hexDigits, c)
		}
	}
	if len(hexDigits)%2 == 1 {
		hexDigits = append(hexDigits, '0') // PDF pads a trailing odd digit
	}
	raw := make([]byte, len(hexDigits)/2)
	for i := 0; i < len(raw); i++ {
		raw[i] = hexNibble(hexDigits[2*i])<<4 | hexNibble(hexDigits[2*i+1])
	}
	return decodePDFText(raw), true
}

func hexNibble(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	default:
		return c - 'A' + 10
	}
}

// decodePDFText maps a PDF text string's bytes to UTF-8: UTF-16BE when it
// carries the FEFF BOM, else treated as (approximately) Latin-1.
func decodePDFText(b []byte) string {
	if len(b) >= 2 && b[0] == 0xFE && b[1] == 0xFF {
		u := make([]uint16, 0, (len(b)-2)/2)
		for i := 2; i+1 < len(b); i += 2 {
			u = append(u, uint16(b[i])<<8|uint16(b[i+1]))
		}
		return string(utf16.Decode(u))
	}
	rs := make([]rune, len(b))
	for i, c := range b {
		rs[i] = rune(c)
	}
	return string(rs)
}
