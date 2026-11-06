package config

import (
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

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

// Every key of Config must appear in §4.2, inside the block of its top-level
// section. It checks that a key is documented, not what the text says about it.
func TestAgentsDocumentsEveryConfigKey(t *testing.T) {
	text, err := os.ReadFile("../AGENTS.md")
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

	var keys []string
	yamlKeys(reflect.TypeFor[Config](), "", map[reflect.Type]bool{}, &keys)
	if len(keys) < 100 {
		t.Fatalf("only %d keys found: the walk is broken", len(keys))
	}
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
	for _, key := range keys {
		parts := strings.Split(key, ".")
		if len(parts) == 1 {
			if section(key) == "" {
				t.Errorf("%s: no section in §4.2", key)
			}
			continue
		}
		leaf := regexp.MustCompile(`\b` + regexp.QuoteMeta(parts[len(parts)-1]) + `\b`)
		if !leaf.MatchString(section(parts[0])) {
			t.Errorf("%s is missing from §4.2 of AGENTS.md", key)
		}
	}
}
