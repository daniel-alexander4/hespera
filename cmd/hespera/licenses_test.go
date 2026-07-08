package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestThirdPartyLicensesCurrent fails if any module in go.mod's require blocks
// — direct or indirect — is not listed in THIRD_PARTY_LICENSES.md. The indirect
// modules are the transitive deps that compile into the binary, so they ship
// with it and their attribution must be retained too (AGPLv3 distribution). This
// guard makes forgetting a dependency a build failure rather than silent drift.
// (It checks presence of the module path, not the verbatim license text. A new
// test-only module that `go mod tidy` parks in the indirect block will also be
// required here — over-attribution is harmless and the prompt is intentional.)
func TestThirdPartyLicensesCurrent(t *testing.T) {
	gomod, err := os.ReadFile("../../go.mod")
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	notice, err := os.ReadFile("../../THIRD_PARTY_LICENSES.md")
	if err != nil {
		t.Fatalf("read THIRD_PARTY_LICENSES.md: %v", err)
	}
	noticeStr := string(notice)

	// Every module inside a require ( … ) block — direct and indirect alike —
	// ships in the binary, so each must be attributed.
	reBlock := regexp.MustCompile(`(?s)require \((.*?)\)`)
	var missing []string
	for _, block := range reBlock.FindAllStringSubmatch(string(gomod), -1) {
		for _, line := range strings.Split(block[1], "\n") {
			// Drop the "// indirect" trailing comment but keep the module.
			if i := strings.Index(line, "//"); i >= 0 {
				line = line[:i]
			}
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			path := strings.Fields(line)[0] // module path is the first token
			if !strings.Contains(noticeStr, path) {
				missing = append(missing, path)
			}
		}
	}
	if len(missing) > 0 {
		t.Fatalf("dependencies missing from THIRD_PARTY_LICENSES.md (add them): %v", missing)
	}
}

// TestThirdPartyLicenseTexts fails if any module in go.mod's require blocks
// has no verbatim license text committed under third_party/licenses/<module>/.
// The texts are embedded into the binary (embed.go) and served at
// /about/licenses — MIT/BSD-style licenses require reproducing their text
// with binary distributions, and the primary artifact is a bare binary.
// Hermetic like TestThirdPartyLicensesCurrent: go.mod parse + directory walk,
// no tools or network. Regeneration: third_party/licenses/README.md.
func TestThirdPartyLicenseTexts(t *testing.T) {
	gomod, err := os.ReadFile("../../go.mod")
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	reBlock := regexp.MustCompile(`(?s)require \((.*?)\)`)
	var missing []string
	for _, block := range reBlock.FindAllStringSubmatch(string(gomod), -1) {
		for _, line := range strings.Split(block[1], "\n") {
			if i := strings.Index(line, "//"); i >= 0 {
				line = line[:i]
			}
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			module := strings.Fields(line)[0]
			// A module's text may live under a subpath of its module dir
			// (go-licenses saves golang.org/x/sys's under x/sys/unix), so any
			// regular file below the module path satisfies it.
			dir := filepath.Join("../../third_party/licenses", filepath.FromSlash(module))
			found := false
			_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
				if err == nil && !d.IsDir() {
					found = true
				}
				return nil
			})
			if !found {
				missing = append(missing, module)
			}
		}
	}
	if len(missing) > 0 {
		t.Fatalf("modules with no verbatim license text under third_party/licenses (see its README.md to regenerate): %v", missing)
	}
}
