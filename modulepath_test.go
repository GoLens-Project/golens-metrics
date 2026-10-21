package golens

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The module was renamed from the bare path "golens" to the canonical GitHub
// import path. These tests pin that contract: the path consumers go get, the
// absence of stale legacy imports in any package, and user-facing docs that
// match the module. Product-identity strings (the "golens:" error prefixes,
// the OTLP service name) intentionally keep the old name and are not checked.
const (
	legacyModulePath    = "golens"
	canonicalModulePath = "github.com/GoLens-Project/golens-metrics"
)

// modulePath reads the module directive from go.mod in the repository root
// (the package directory the test binary runs from).
func modulePath(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if path, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.TrimSpace(path)
		}
	}
	t.Fatal("go.mod has no module directive")
	return ""
}

func TestModulePathRename(t *testing.T) {
	t.Run("go.mod declares the canonical import path", func(t *testing.T) {
		if got := modulePath(t); got != canonicalModulePath {
			t.Errorf("module path = %q, want %q (this is the path consumers go get)", got, canonicalModulePath)
		}
	})

	t.Run("no source file imports the legacy path", func(t *testing.T) {
		err := filepath.WalkDir(".", func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			// Skip hidden directories (.git, sandbox mounts) and non-Go files.
			if d.IsDir() {
				if strings.HasPrefix(d.Name(), ".") && d.Name() != "." {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") {
				return nil
			}
			fset := token.NewFileSet()
			// ImportsOnly: this test judges import paths only, so stale
			// string literals like the "golens:" error prefix are ignored.
			f, perr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
			if perr != nil {
				t.Errorf("parse %s: %v", path, perr)
				return nil
			}
			for _, imp := range f.Imports {
				p, uerr := strconv.Unquote(imp.Path.Value)
				if uerr != nil {
					continue
				}
				if p == legacyModulePath || strings.HasPrefix(p, legacyModulePath+"/") {
					t.Errorf("%s: imports legacy module path %q (want %q)", fset.Position(imp.Pos()), p, canonicalModulePath)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk repo: %v", err)
		}
	})

	t.Run("README references the current path, not the legacy one", func(t *testing.T) {
		data, err := os.ReadFile("README.md")
		if err != nil {
			t.Fatalf("read README.md: %v", err)
		}
		readme := string(data)
		if !strings.Contains(readme, "go get "+canonicalModulePath) {
			t.Errorf("README install instruction missing %q", "go get "+canonicalModulePath)
		}
		// Remove the canonical path, then the legacy full path must not
		// survive anywhere (imports, badges, clone URLs). The legacy path
		// is a prefix of the canonical one, hence the strip first.
		residue := strings.ReplaceAll(readme, canonicalModulePath, "")
		if strings.Contains(residue, "github.com/GoLens-Project/"+legacyModulePath) {
			t.Errorf("README still references the legacy path github.com/GoLens-Project/%s", legacyModulePath)
		}
	})
}
