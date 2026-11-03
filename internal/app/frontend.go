package app

import (
	"crypto/sha512"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"sort"

	"github.com/Helles01/raptor-panel/internal/api/web"
)

// Frontend is the build of the React frontend as the pages need it.
type Frontend struct {
	Shell   *web.Shell
	Assets  fs.FS
	Locales *web.Locales
}

// LoadFrontend reads the build in dir (web/public/assets after `yarn build:production`). It checks the build at the start, so that a
// missing or changed file is an error with a name when the panel starts, and not a white page that a browser blocks without saying why.
func LoadFrontend(dir string) (Frontend, error) {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return Frontend{}, fmt.Errorf("the frontend build is not in %q: build it with scripts/frontend-check.sh or set web.assets_dir", dir)
	}
	return LoadFrontendFS(os.DirFS(dir))
}

// LoadFrontendFS is LoadFrontend for a build that is in a file system.
func LoadFrontendFS(fsys fs.FS) (Frontend, error) {
	f, err := fsys.Open("manifest.json")
	if err != nil {
		return Frontend{}, errors.New("the frontend build has no manifest.json")
	}
	manifest, err := web.LoadManifest(f)
	f.Close()
	if err != nil {
		return Frontend{}, err
	}

	names := make([]string, 0, len(manifest))
	for n := range manifest {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := checkEntry(fsys, name, manifest[name]); err != nil {
			return Frontend{}, err
		}
	}

	shell, err := web.NewShell(manifest, web.ShellOptions{})
	if err != nil {
		return Frontend{}, err
	}
	locales, err := web.EmbeddedLocales()
	if err != nil {
		return Frontend{}, err
	}
	return Frontend{Shell: shell, Assets: fsys, Locales: locales}, nil
}

// checkEntry makes sure that the file an entry names is there, is a plain file, and, if the entry has an integrity value, is the file that
// value was made for.
func checkEntry(fsys fs.FS, name string, e web.ManifestEntry) error {
	file := path.Base(e.Src)
	info, err := fs.Stat(fsys, file)
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("the manifest names %s for %q and the build has no such file", file, name)
	}
	if e.Integrity == "" {
		return nil
	}
	body, err := fs.ReadFile(fsys, file)
	if err != nil {
		return fmt.Errorf("read %s: %w", file, err)
	}
	sum := sha512.Sum384(body)
	if want := "sha384-" + base64.StdEncoding.EncodeToString(sum[:]); want != e.Integrity {
		return fmt.Errorf("%s is not the file the manifest was made for (its integrity value differs): build the frontend again", file)
	}
	return nil
}
