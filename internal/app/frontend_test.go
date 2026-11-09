package app

import (
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Helles01/raptor-panel/internal/api/web"
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

// writeBuild writes a build into a folder: the main script named by its number, and a manifest that names it, with the time the file is given.
func writeBuild(t *testing.T, dir string, n int, at time.Time) {
	t.Helper()
	script := fmt.Sprintf("console.log('build %d')", n)
	name := fmt.Sprintf("bundle.%d.js", n)
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err := json.Marshal(map[string]map[string]string{"main.js": {"src": "/assets/" + name, "integrity": integrityOf(script)}})
	if err != nil {
		t.Fatal(err)
	}
	putManifest(t, dir, manifest, at)
}

// putManifest puts a manifest in place with the time it is given, in one step: written under another name, given its time and renamed. A loop that looks every few
// milliseconds would otherwise see the file with the time of the write and then again with the time given, and take them for two versions.
func putManifest(t *testing.T, dir string, manifest []byte, at time.Time) {
	t.Helper()
	tmp := filepath.Join(dir, "manifest.json.tmp")
	if err := os.WriteFile(tmp, manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(tmp, at, at); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, "manifest.json")); err != nil {
		t.Fatal(err)
	}
}

func pageOf(t *testing.T, f Frontend) string {
	t.Helper()
	rec := httptest.NewRecorder()
	if err := f.Shell.Render(rec, web.PageAuth, nil, web.Site{Name: "Raptor"}); err != nil {
		t.Fatal(err)
	}
	return rec.Body.String()
}

// waitFor looks until what is wanted is so, for a few seconds.
func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("gave up waiting for %s", what)
}

func TestWatchFrontendServesANewBuildWithoutARestart(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	writeBuild(t, dir, 1, base)
	f, err := LoadFrontend(dir)
	if err != nil {
		t.Fatal(err)
	}
	logs := &syncBuffer{}
	loop := WatchFrontend(f.Assets, f.Shell, 10*time.Millisecond, slog.New(slog.NewTextHandler(logs, nil)))
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- loop.Run(ctx) }()
	defer func() {
		stop()
		if err := <-done; err != nil {
			t.Errorf("the loop ended with %v", err)
		}
	}()

	if !strings.Contains(pageOf(t, f), "bundle.1.js") {
		t.Fatal("the first build is not served")
	}
	writeBuild(t, dir, 2, base.Add(time.Minute))
	waitFor(t, "the second build", func() bool { return strings.Contains(pageOf(t, f), "bundle.2.js") })
	// The page changes inside Reload and the line is written just after it, so the line is waited for too.
	waitFor(t, "the log line", func() bool { return strings.Contains(logs.String(), "a new build of the frontend is served") })

	// A build that does not check out (here its script is not the one the manifest was made for) is not taken, and is said once, however long it stays so.
	if err := os.WriteFile(filepath.Join(dir, "bundle.3.js"), []byte("not the script"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, _ := json.Marshal(map[string]map[string]string{"main.js": {"src": "/assets/bundle.3.js", "integrity": integrityOf("console.log('build 3')")}})
	putManifest(t, dir, manifest, base.Add(2*time.Minute))
	waitFor(t, "the complaint", func() bool { return strings.Contains(logs.String(), "was not taken") })
	time.Sleep(100 * time.Millisecond) // ten more looks
	if n := strings.Count(logs.String(), "was not taken"); n != 1 || !strings.Contains(pageOf(t, f), "bundle.2.js") {
		t.Errorf("the complaint was made %d times, or the page changed: %s", n, pageOf(t, f))
	}

	// When it is right, it is taken.
	writeBuild(t, dir, 4, base.Add(3*time.Minute))
	waitFor(t, "the fourth build", func() bool { return strings.Contains(pageOf(t, f), "bundle.4.js") })
}

// A build made after the shell was loaded and before the loop began to look is served too: the loop does not take the manifest it finds first for the one the
// shell was made from. And when nothing was made in between, the first look changes nothing and says nothing.
func TestWatchFrontendTakesABuildMadeBeforeItBegan(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	writeBuild(t, dir, 1, base)
	f, err := LoadFrontend(dir)
	if err != nil {
		t.Fatal(err)
	}
	writeBuild(t, dir, 2, base.Add(time.Minute)) // after the load, before the loop
	logs := &syncBuffer{}
	loop := WatchFrontend(f.Assets, f.Shell, 5*time.Millisecond, slog.New(slog.NewTextHandler(logs, nil)))
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- loop.Run(ctx) }()
	waitFor(t, "the build made before the loop", func() bool { return strings.Contains(pageOf(t, f), "bundle.2.js") })
	waitFor(t, "the log line", func() bool { return strings.Contains(logs.String(), "a new build of the frontend is served") })
	stop()
	<-done

	// Nothing made in between: the first look finds the build that is served, and the log stays empty.
	same, err := LoadFrontend(dir)
	if err != nil {
		t.Fatal(err)
	}
	quiet := &syncBuffer{}
	loop = WatchFrontend(same.Assets, same.Shell, 5*time.Millisecond, slog.New(slog.NewTextHandler(quiet, nil)))
	ctx, stop = context.WithCancel(context.Background())
	go func() { done <- loop.Run(ctx) }()
	time.Sleep(60 * time.Millisecond)
	stop()
	<-done
	if quiet.String() != "" || !strings.Contains(pageOf(t, same), "bundle.2.js") {
		t.Errorf("log %q, page %s", quiet.String(), pageOf(t, same))
	}
}

func TestWatchFrontendLeavesAMissingManifestAlone(t *testing.T) {
	dir := t.TempDir()
	writeBuild(t, dir, 1, time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC))
	f, err := LoadFrontend(dir)
	if err != nil {
		t.Fatal(err)
	}
	logs := &syncBuffer{}
	loop := WatchFrontend(f.Assets, f.Shell, 5*time.Millisecond, slog.New(slog.NewTextHandler(logs, nil)))
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- loop.Run(ctx) }()
	// A build that is being made removes the manifest for a moment: nothing is said, and the page keeps its script.
	if err := os.Remove(filepath.Join(dir, "manifest.json")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)
	stop()
	<-done
	if logs.String() != "" || !strings.Contains(pageOf(t, f), "bundle.1.js") {
		t.Errorf("log %q, page %s", logs.String(), pageOf(t, f))
	}
}

// syncBuffer is a log that the loop writes while the test reads it.
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
