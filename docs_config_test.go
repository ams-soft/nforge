package nforge_test

import (
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/ams-soft/nforge/cache"
	"github.com/ams-soft/nforge/codec"
	"github.com/ams-soft/nforge/config"
	"github.com/ams-soft/nforge/database"
	nhttp "github.com/ams-soft/nforge/http"
	"github.com/ams-soft/nforge/jose"
	"github.com/ams-soft/nforge/logs"
	"github.com/ams-soft/nforge/message"
	"github.com/ams-soft/nforge/telemetry"
)

// sections maps every top-level configuration section to the type that
// decodes it: the reserved ones of package config and the built-in modules'.
var sections = map[string]reflect.Type{
	"env":            reflect.TypeFor[string](),
	"app":            reflect.TypeFor[config.AppConfig](),
	"prd_exceptions": reflect.TypeFor[config.PrdExceptions](),
	"http":           reflect.TypeFor[nhttp.Config](),
	"database":       reflect.TypeFor[database.Config](),
	"logging":        reflect.TypeFor[logs.Settings](),
	"tracing":        reflect.TypeFor[telemetry.Settings](),
	"codec":          reflect.TypeFor[codec.Settings](),
	"jose":           reflect.TypeFor[jose.Config](),
	"cache":          reflect.TypeFor[cache.Config](),
	"message":        reflect.TypeFor[message.Config](),
}

// yamlKeys lists the dotted keys of a config type. Slices of structs and maps
// count through their element, like the entries of jose.keys.
func yamlKeys(t reflect.Type, prefix string, seen map[reflect.Type]bool, out *[]string) {
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct || seen[t] {
		return
	}
	seen[t] = true
	defer delete(seen, t)
	for i := range t.NumField() {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if name == "-" || name == "" || !f.IsExported() {
			continue
		}
		key := prefix + name
		*out = append(*out, key)
		yamlKeys(f.Type, key+".", seen, out)
	}
}

// Every key of every section must appear in §4.2, inside the block of its
// top-level section. It checks that a key is documented, not what the text
// says about it.
func TestAgentsDocumentsEveryConfigKey(t *testing.T) {
	text, err := os.ReadFile("AGENTS.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(text)
	start := strings.Index(doc, "### 4.2 Full schema")
	if start < 0 {
		t.Fatal("§4.2 not found")
	}
	doc = doc[start:]
	doc = doc[strings.Index(doc, "```yaml"):]
	doc = doc[:strings.Index(doc[7:], "```")+7]

	section := func(top string) string {
		re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(top) + `:`)
		loc := re.FindStringIndex(doc)
		if loc == nil {
			return ""
		}
		rest := doc[loc[1]:]
		if next := regexp.MustCompile(`(?m)^[a-z_]+:`).FindStringIndex(rest); next != nil {
			return rest[:next[0]]
		}
		return rest
	}
	total := 0
	for top, typ := range sections {
		if section(top) == "" && !strings.Contains(doc, top+":") {
			t.Errorf("%s: no section in §4.2", top)
			continue
		}
		var keys []string
		yamlKeys(typ, top+".", map[reflect.Type]bool{}, &keys)
		total += len(keys)
		for _, key := range keys {
			parts := strings.Split(key, ".")
			leaf := regexp.MustCompile(`\b` + regexp.QuoteMeta(parts[len(parts)-1]) + `\b`)
			if !leaf.MatchString(section(top)) {
				t.Errorf("%s is missing from §4.2 of AGENTS.md", key)
			}
		}
	}
	if total < 100 {
		t.Fatalf("only %d keys found: the walk is broken", total)
	}
}
