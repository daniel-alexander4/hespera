package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestThirdPartyLicensesCurrent fails if any module in go.mod's require blocks
// — direct or indirect — is not listed in THIRD_PARTY_LICENSES.md. The indirect
// modules are the transitive deps that compile into the binary, so they ship
// with it and their attribution must be retained too (GPLv3 distribution). This
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
