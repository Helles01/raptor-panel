package web

import (
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"strings"
)

var assetName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// AssetsHandler serves the frontend build (the files of /assets/) from fsys: only plain files directly in the folder,
// never a directory listing, a nested path or a dot file. The build names its files after their content, so they are
// cached for a year; manifest.json, which names the current files, is not.
func AssetsHandler(fsys fs.FS) http.Handler {
	files := http.FileServer(http.FS(fsys))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		name := strings.TrimPrefix(r.URL.Path, "/assets/")
		if name == r.URL.Path || name != path.Base(name) || !assetName.MatchString(name) {
			http.NotFound(w, r)
			return
		}
		if info, err := fs.Stat(fsys, name); err != nil || !info.Mode().IsRegular() {
			http.NotFound(w, r)
			return
		}

		if name == "manifest.json" {
			w.Header().Set("Cache-Control", "no-cache")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		// FileServer redirects "/index.html" style paths and works on the path below the mount point.
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/" + name
		files.ServeHTTP(w, r2)
	})
}
