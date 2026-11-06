package nforge

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

var (
	versionRe   = regexp.MustCompile(`\bv\d+\.\d+\.\d+\b`)
	toolchainRe = regexp.MustCompile(`toolchain go\d+\.\d+\.\d+`)
)

// docs are the files that describe the current state. CHANGELOG.md is history.
var docs = []string{"AGENTS.md", "README.md", "llms.txt"}

// A version cited in prose rots at the next dependency update, which is how
// these files came to disagree with go.mod. Every vX.Y.Z must be in go.mod; the
// linter is the exception, because it is a tool and not a dependency.
func TestDocsCiteTheVersionsOfGoMod(t *testing.T) {
	gomod, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range docs {
		text, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for n, line := range strings.Split(string(text), "\n") {
			if strings.Contains(line, "golangci-lint") {
				continue
			}
			for _, v := range versionRe.FindAllString(line, -1) {
				if !strings.Contains(string(gomod), " "+v+"\n") {
					t.Errorf("%s:%d cites %s, which go.mod does not require", name, n+1, v)
				}
			}
			for _, tc := range toolchainRe.FindAllString(line, -1) {
				if !strings.Contains(string(gomod), tc+"\n") {
					t.Errorf("%s:%d cites %q, go.mod differs", name, n+1, tc)
				}
			}
		}
	}
}

func TestAgentsCitesTheFrameworkVersion(t *testing.T) {
	text, err := os.ReadFile("AGENTS.md")
	if err != nil {
		t.Fatal(err)
	}
	if want := "(currently `" + Version + "`)"; !strings.Contains(string(text), want) {
		t.Errorf("AGENTS.md lacks %q", want)
	}
}
