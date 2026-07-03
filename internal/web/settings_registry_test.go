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
// The templates are the enumeration point for the web side because a
// web-editable setting must, by definition, have a form input there. The
// `integrity_present`/`lyrics_present` hidden fields are submission sentinels
// (an unchecked checkbox posts nothing), not settings, and are filtered out.
func TestManagedSettingsCoverSettingsForms(t *testing.T) {
	tpl, err := os.ReadFile("../../web/templates/settings_apikeys.html")
	if err != nil {
		t.Fatalf("read settings_apikeys.html: %v", err)
	}

	nameAttr := regexp.MustCompile(`name="([^"]+)"`)
	formKeys := map[string]bool{}
	for _, m := range nameAttr.FindAllStringSubmatch(string(tpl), -1) {
		name := m[1]
		if strings.HasSuffix(name, "_present") { // checkbox-submission sentinels
			continue
		}
		formKeys[name] = true
	}

	registryKeys := map[string]bool{}
	for _, spec := range managedSettings {
		registryKeys[spec.Key] = true
	}

	// media_root is registry-managed but its form lives on the Libraries page,
	// not the API-keys page — account for it there explicitly.
	if !registryKeys["media_root"] {
		t.Error("media_root missing from managedSettings")
	}
	libTpl, err := os.ReadFile("../../web/templates/libraries.html")
	if err != nil {
		t.Fatalf("read libraries.html: %v", err)
	}
	if !strings.Contains(string(libTpl), `name="media_root"`) {
		t.Error(`media_root form input missing from libraries.html`)
	}
	delete(registryKeys, "media_root")

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
