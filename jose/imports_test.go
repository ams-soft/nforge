package jose

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// TestImportGraph keeps jose, crypto and the helpers standalone (no internal nForge imports), cache
// limited to codec, and config limited to its standalone config dependencies.
// A package may always import its own internal/ subpackages, and those follow
// the rules of their parent.
func TestImportGraph(t *testing.T) {
	const module = "github.com/ams-soft/nforge/"
	check := func(dir string, allowed ...string) {
		own := module + filepath.Base(dir) + "/internal/"
		if dir == "." {
			own = module + "jose/internal/"
		}
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		internal, err := filepath.Glob(filepath.Join(dir, "internal", "*", "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, internal...)
		for _, file := range files {
			if strings.HasSuffix(file, "_test.go") {
				continue
			}
			f, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatal(err)
			}
			for _, imp := range f.Imports {
				path := strings.Trim(imp.Path.Value, `"`)
				if !strings.HasPrefix(path, "github.com/ams-soft/nforge") {
					continue
				}
				ok := strings.HasPrefix(path, own)
				for _, a := range allowed {
					ok = ok || path == a
				}
				if !ok {
					t.Errorf("%s imports %s", file, path)
				}
			}
		}
	}
	check(".")
	check("../config", "github.com/ams-soft/nforge/database", "github.com/ams-soft/nforge/jose", "github.com/ams-soft/nforge/cache")
	check("../cache", "github.com/ams-soft/nforge/codec")
	check("../database")
	check("../codec")
	check("../crypto")
	check("../helper/safecalc")
}
