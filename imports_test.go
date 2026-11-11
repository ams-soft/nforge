package nforge

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const modulePath = "github.com/ams-soft/nforge"

// importRules lists, per package directory, the nForge packages its non-test
// files may import (its own internal/ subpackages are always allowed and follow
// the same rules). It keeps the architecture: module and config know no
// concrete module, app orchestrates only through module and config, and the
// standalone packages stay standalone.
var importRules = map[string][]string{
	"module":           {},
	"config":           {},
	"app":              {"module", "config"},
	"codec":            {"module"},
	"database":         {"module"},
	"jose":             {"module"},
	"cache":            {"module", "codec", "database"},
	"message":          {"module", "codec"},
	"telemetry":        {"module"},
	"logs":             {"module", "telemetry"},
	"crypto":           {},
	"helper/safecalc":  {},
	"helper/qrcode":    {},
	"http":             {"", "module", "codec", "database", "jose", "cache", "telemetry", "message"},
	"internal/apptest": {"app", "config", "module"},
}

func goFiles(t *testing.T, dir string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && path != dir && !strings.Contains(path, "internal") {
			return filepath.SkipDir // a nested public package has its own rule
		}
		if !d.IsDir() && strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func imports(t *testing.T, file string) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, imp := range f.Imports {
		out = append(out, strings.Trim(imp.Path.Value, `"`))
	}
	return out
}

func TestImportGraph(t *testing.T) {
	for dir, allowed := range importRules {
		own := modulePath + "/" + dir + "/internal/"
		for _, file := range goFiles(t, dir) {
			for _, path := range imports(t, file) {
				if !strings.HasPrefix(path, modulePath) || strings.HasPrefix(path, own) {
					continue
				}
				rel := strings.TrimPrefix(strings.TrimPrefix(path, modulePath), "/")
				if !slices.Contains(allowed, rel) {
					t.Errorf("%s imports %s", file, path)
				}
			}
		}
	}
}

// Only the orchestrator handles process signals: an HTTP-only or worker-only
// application gets the same lifecycle, and a library never steals SIGINT.
func TestOnlyAppHandlesSignals(t *testing.T) {
	for dir := range importRules {
		if dir == "app" {
			continue
		}
		for _, file := range goFiles(t, dir) {
			if slices.Contains(imports(t, file), "os/signal") {
				t.Errorf("%s imports os/signal", file)
			}
		}
	}
}

// Every package of the module has a rule, so a new one is a deliberate choice,
// and the removed deps container stays removed.
func TestEveryPackageHasARule(t *testing.T) {
	if _, err := os.Stat("deps"); err == nil {
		t.Fatal("the deps package was removed in favor of app and module")
	}
	err := filepath.WalkDir(".", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		name := d.Name()
		if path != "." && (strings.HasPrefix(name, "_") || strings.HasPrefix(name, ".") || name == "testdata") {
			return filepath.SkipDir
		}
		if strings.Contains(path, "internal") && path != "internal/apptest" {
			return nil
		}
		files, _ := filepath.Glob(filepath.Join(path, "*.go"))
		hasCode := slices.ContainsFunc(files, func(f string) bool { return !strings.HasSuffix(f, "_test.go") })
		if hasCode && path != "." {
			if _, ok := importRules[filepath.ToSlash(path)]; !ok {
				t.Errorf("package %s has no import rule", path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
