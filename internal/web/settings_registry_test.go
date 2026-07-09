package web

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestManagedSettingsCoverSettingsForms is the drift guard between the two
// places that enumerate app_settings keys: the managedSettings registry (the
// hescli `config` verb's list) and the web settings forms. A key added to one
// side and forgotten on the other is invisible until someone reaches for it —
// this fails the build instead, the same shape as TestThirdPartyLicensesCurrent
// guards the license notice against go.mod.
//
// The template is the enumeration point for the web side because a
// web-editable setting must, by definition, have a form input inside one of
// the settings page's accordion cards. The `integrity_present`/`lyrics_present`
// hidden fields are submission sentinels (an unchecked checkbox posts
// nothing), not settings, and are filtered out. The `name="settings"` on the
// <details> accordion elements is filtered too — an HTML grouping attribute,
// not a form field.
func TestManagedSettingsCoverSettingsForms(t *testing.T) {
	tpl, err := os.ReadFile("../../web/templates/settings.html")
	if err != nil {
		t.Fatalf("read settings.html: %v", err)
	}
	nameAttr := regexp.MustCompile(`name="([^"]+)"`)
	formKeys := map[string]bool{}
	for _, m := range nameAttr.FindAllStringSubmatch(string(tpl), -1) {
		name := m[1]
		// Filtered non-settings names: *_present checkbox-submission sentinels,
		// the <details name="settings"> accordion-grouping attribute, and the
		// action-form fields of the Libraries/Jobs cards (library id, job id —
		// they post to action endpoints, not app_settings).
		if strings.HasSuffix(name, "_present") || name == "settings" || name == "id" || name == "job_id" {
			continue
		}
		formKeys[name] = true
	}

	registryKeys := map[string]bool{}
	for _, spec := range managedSettings {
		registryKeys[spec.Key] = true
	}

	var missingFromRegistry, missingFromForms []string
	for k := range formKeys {
		if !registryKeys[k] {
			missingFromRegistry = append(missingFromRegistry, k)
		}
	}
	for k := range registryKeys {
		if !formKeys[k] {
			missingFromForms = append(missingFromForms, k)
		}
	}
	sort.Strings(missingFromRegistry)
	sort.Strings(missingFromForms)
	if len(missingFromRegistry) > 0 {
		t.Errorf("settings-page keys missing from managedSettings (add a registry row so hescli sees them): %v", missingFromRegistry)
	}
	if len(missingFromForms) > 0 {
		t.Errorf("managedSettings keys with no settings-page form (add an input or move the key): %v", missingFromForms)
	}
}

// TestManagedSettingsCoverHescliCompletion is the same drift guard for the
// third enumeration of app_settings keys: the static `cfgkeys` value set in
// hescli's bash-completion script (cmd/hescli/main.go, bashCompletion). That
// list is a deliberate literal — sharing the registry across the
// cmd/hescli↔internal/web boundary would drag the web package into the CLI
// binary — so this test is what keeps it honest: a key added to
// managedSettings without updating cfgkeys (or vice versa) fails the build
// instead of silently missing from tab completion. (Caught live 2026-07-08:
// job_resume_enabled shipped without the completion entry.)
func TestManagedSettingsCoverHescliCompletion(t *testing.T) {
	src, err := os.ReadFile("../../cmd/hescli/main.go")
	if err != nil {
		t.Fatalf("read cmd/hescli/main.go: %v", err)
	}
	m := regexp.MustCompile(`local cfgkeys="([^"]+)"`).FindSubmatch(src)
	if m == nil {
		t.Fatalf("cfgkeys value set not found in cmd/hescli/main.go — completion script restructured? Update this guard")
	}
	completionKeys := map[string]bool{}
	for _, k := range strings.Fields(string(m[1])) {
		completionKeys[k] = true
	}

	registryKeys := map[string]bool{}
	for _, spec := range managedSettings {
		registryKeys[spec.Key] = true
	}

	var missingFromCompletion, missingFromRegistry []string
	for k := range registryKeys {
		if !completionKeys[k] {
			missingFromCompletion = append(missingFromCompletion, k)
		}
	}
	for k := range completionKeys {
		if !registryKeys[k] {
			missingFromRegistry = append(missingFromRegistry, k)
		}
	}
	sort.Strings(missingFromCompletion)
	sort.Strings(missingFromRegistry)
	if len(missingFromCompletion) > 0 {
		t.Errorf("managedSettings keys missing from hescli completion cfgkeys (cmd/hescli/main.go): %v", missingFromCompletion)
	}
	if len(missingFromRegistry) > 0 {
		t.Errorf("hescli completion cfgkeys not in managedSettings (stale entry?): %v", missingFromRegistry)
	}
}
