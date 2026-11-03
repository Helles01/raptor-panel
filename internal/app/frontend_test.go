package app

import (
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func integrityOf(body string) string {
	sum := sha512.Sum384([]byte(body))
	return "sha384-" + base64.StdEncoding.EncodeToString(sum[:])
}

// build makes the files of a build: the manifest, a main script and a font.
func build(t *testing.T, mutate func(m map[string]map[string]string, files fstest.MapFS)) fstest.MapFS {
	t.Helper()
	main := "console.log('main')"
	files := fstest.MapFS{"bundle.abc123.js": {Data: []byte(main)}, "font.woff2": {Data: []byte("font")}}
	m := map[string]map[string]string{
		"main.js":   {"src": "/assets/bundle.abc123.js", "integrity": integrityOf(main)},
		"font.woff": {"src": "/assets/font.woff2"},
	}
	if mutate != nil {
		mutate(m, files)
	}
	body, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	files["manifest.json"] = &fstest.MapFile{Data: body}
	return files
}

func TestLoadFrontend(t *testing.T) {
	f, err := LoadFrontendFS(build(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	if f.Shell == nil || f.Assets == nil || f.Locales == nil {
		t.Errorf("frontend = %+v", f)
	}
}

func TestLoadFrontendRefusesABrokenBuild(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(m map[string]map[string]string, files fstest.MapFS)
		want   string
	}{
		"script missing": {func(m map[string]map[string]string, files fstest.MapFS) { delete(files, "bundle.abc123.js") }, "bundle.abc123.js"},
		"font missing":   {func(m map[string]map[string]string, files fstest.MapFS) { delete(files, "font.woff2") }, "font.woff2"},
		"script changed": {func(m map[string]map[string]string, files fstest.MapFS) {
			files["bundle.abc123.js"] = &fstest.MapFile{Data: []byte("console.log('changed')")}
		}, "integrity"},
		"no main entry": {func(m map[string]map[string]string, files fstest.MapFS) { delete(m, "main.js") }, "main.js"},
		"bad integrity": {func(m map[string]map[string]string, files fstest.MapFS) { m["main.js"]["integrity"] = "md5-abc" }, "integrity"},
		"nested source": {func(m map[string]map[string]string, files fstest.MapFS) {
			m["main.js"]["src"] = "/assets/../etc/passwd"
		}, "manifest"},
	} {
		if _, err := LoadFrontendFS(build(t, tc.mutate)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to mention %q", name, err, tc.want)
		}
	}
}

func TestLoadFrontendRefusesWhatIsNotAManifest(t *testing.T) {
	if _, err := LoadFrontendFS(fstest.MapFS{}); err == nil || !strings.Contains(err.Error(), "manifest.json") {
		t.Errorf("no manifest: %v", err)
	}
	if _, err := LoadFrontendFS(fstest.MapFS{"manifest.json": {Data: []byte("not json")}}); err == nil {
		t.Error("a manifest that is not JSON was accepted")
	}
	// An entry that names a folder is not a file.
	files := build(t, nil)
	files["bundle.abc123.js"] = &fstest.MapFile{Mode: 0o755 | os.ModeDir}
	if _, err := LoadFrontendFS(files); err == nil {
		t.Error("a folder was taken for the script")
	}
}

func TestLoadFrontendFromAFolder(t *testing.T) {
	dir := t.TempDir()
	for name, f := range build(t, nil) {
		if err := os.WriteFile(filepath.Join(dir, name), f.Data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := LoadFrontend(dir); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{"missing": filepath.Join(dir, "nothing"), "a file": filepath.Join(dir, "manifest.json"), "empty": t.TempDir()} {
		if _, err := LoadFrontend(path); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := LoadFrontend(filepath.Join(dir, "nothing")); err == nil || !strings.Contains(err.Error(), "web.assets_dir") {
		t.Errorf("the message does not say what to do: %v", err)
	}
}
