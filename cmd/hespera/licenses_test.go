package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestThirdPartyLicensesCurrent fails if a direct dependency in go.mod is not
// listed in THIRD_PARTY_LICENSES.md. GPLv3 distribution requires retaining each
// third-party component's attribution, so adding a dependency must update the
// notice — this guard makes forgetting it a build failure rather than silent
// drift. (It checks presence of the module path, not the license text itself.)
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

	// Direct deps = lines inside a require ( … ) block that aren't // indirect.
	reBlock := regexp.MustCompile(`(?s)require \((.*?)\)`)
	var missing []string
	for _, block := range reBlock.FindAllStringSubmatch(string(gomod), -1) {
		for _, line := range strings.Split(block[1], "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.Contains(line, "// indirect") {
				continue
			}
			path := strings.Fields(line)[0] // module path is the first token
			if !strings.Contains(noticeStr, path) {
				missing = append(missing, path)
			}
		}
	}
	if len(missing) > 0 {
		t.Fatalf("direct dependencies missing from THIRD_PARTY_LICENSES.md (add them): %v", missing)
	}
}
