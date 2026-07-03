package web

import (
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"path"
	"sort"

	"hespera"
)

// aboutLicenses streams every embedded third-party license text as one
// plain-text document with a "==== <module> ====" header per module, linked
// from Settings → About. The verbatim texts ride inside the binary
// (embed.go's third_party/licenses tree) because the primary artifact is a
// bare self-contained binary that can't carry loose notice files — MIT/BSD
// licenses require reproducing their text with binary distributions.
func (h *Handler) aboutLicenses(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	root := hespera.ThirdPartyLicensesFS()
	var files []string
	_ = fs.WalkDir(root, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || p == "README.md" {
			return nil
		}
		files = append(files, p)
		return nil
	})
	sort.Strings(files)

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	fmt.Fprint(w, "Third-party license texts for every module compiled into Hespera.\n")
	fmt.Fprint(w, "License types and provenance: THIRD_PARTY_LICENSES.md in the source tree.\n")
	for _, p := range files {
		fmt.Fprintf(w, "\n\n==== %s ====\n\n", path.Dir(p))
		f, err := root.Open(p)
		if err != nil {
			continue
		}
		_, _ = io.Copy(w, f)
		_ = f.Close()
	}
}
