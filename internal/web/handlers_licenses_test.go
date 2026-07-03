package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestAboutLicensesServesEmbeddedTexts pins the /about/licenses contract: the
// verbatim texts come from the binary's embedded tree (not disk), one header
// per module, plain text with nosniff.
func TestAboutLicensesServesEmbeddedTexts(t *testing.T) {
	h, _ := newTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/about/licenses", nil)
	rec := httptest.NewRecorder()
	h.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /about/licenses: %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("Content-Type = %q, want text/plain", ct)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("missing nosniff")
	}
	body := rec.Body.String()
	// A known module header plus its BSD-2 text phrase proves real texts, not
	// just type names.
	if !strings.Contains(body, "==== github.com/dhowden/tag ====") {
		t.Fatal("missing dhowden/tag module header")
	}
	if !strings.Contains(body, "Redistribution and use in source and binary forms") {
		t.Fatal("missing BSD license text body")
	}
	if !strings.Contains(body, "==== modernc.org/sqlite ====") {
		t.Fatal("missing modernc.org/sqlite module header")
	}
	if strings.Contains(body, "==== README.md") || strings.Contains(body, "Regenerate after adding") {
		t.Fatal("the tree README leaked into the served document")
	}
}
