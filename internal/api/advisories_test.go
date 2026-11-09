package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Helles01/raptor-panel/internal/api/middleware"
)

// scripts/check.sh accepts two advisories of Echo v4 (GO-2026-6872 and GO-2026-6875, no fixed version exists), on the reasons in docs/accepted-advisories.md. Those reasons are
// facts about this code, and this test is what keeps them true: if one stops being so, the acceptance is wrong, and the build says so here, in words, and not in a list of
// identifiers that somebody could extend.

// The client address is always taken through the extractor, never from Echo's default, which believes X-Forwarded-For and X-Real-IP from anybody (6872).
func TestEveryServerHasAClientAddressExtractor(t *testing.T) {
	for name, opts := range map[string]Options{
		"without a list of trusted proxies": {Logger: slog.Default(), Authorizer: &recordingAuthorizer{}},
		"with one":                          {Logger: slog.Default(), Authorizer: &recordingAuthorizer{}, Trust: middleware.NewProxyTrust(nil, slog.Default())},
	} {
		s, err := New(opts)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if s.Echo.IPExtractor == nil {
			t.Errorf("%s: Echo is left with its default way of finding the client address", name)
		}
	}
}

// The parts of Echo that the advisories are about are not used at all: its middleware package (the proxy, the static files, the secure and method override
// middleware; 6875), a second Echo made anywhere but where the extractor is set, a configured Echo, and the scheme of a request as Echo works it out. Test files are
// left out: they may build whatever they need to test with.
func TestTheEchoFeaturesOfTheAdvisoriesAreNotUsed(t *testing.T) {
	root := filepath.Join("..", "..")
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "reference", "web", ".git", "node_modules", "bin":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		file, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		for _, imp := range file.Imports {
			if strings.Contains(imp.Path.Value, "github.com/labstack/echo/v4/middleware") {
				t.Errorf("%s imports Echo's middleware package (%s): GO-2026-6875 is accepted on the ground that it is not used", rel, imp.Path.Value)
			}
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			switch {
			case sel.Sel.Name == "NewWithConfig":
				t.Errorf("%s: NewWithConfig (GO-2026-6875 is accepted on the ground that it is not used)", rel)
			case sel.Sel.Name == "Scheme" && len(call.Args) == 0:
				t.Errorf("%s: a call of Scheme() (GO-2026-6875 is accepted on the ground that Echo's scheme is not used)", rel)
			case sel.Sel.Name == "New" && isEchoPackage(sel.X) && rel != "internal/api/server.go":
				t.Errorf("%s makes an Echo of its own: the extractor of the client address is set in internal/api/server.go only (GO-2026-6872)", rel)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func isEchoPackage(x ast.Expr) bool {
	id, ok := x.(*ast.Ident)
	return ok && id.Name == "echo"
}
