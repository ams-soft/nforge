package nhttp

import (
	"strings"
	"testing"
)

func TestGraphRouteDefaults(t *testing.T) {
	cfg, err := LoadFlags(writeConfig(t, baseYAML), "")
	if err != nil {
		t.Fatal(err)
	}
	if g := cfg.HTTP.Routes.Graph; g.Enabled || g.Path != "" {
		t.Fatalf("the graph is off by default and gets no path: %+v", g)
	}
	cfg, err = LoadFlags(writeConfig(t, baseYAML+"http: {routes: {graph: {enabled: true}}}\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.HTTP.Routes.Graph.Path; got != "/graph" {
		t.Fatalf("path = %q", got)
	}
}

func TestGraphRouteFromEnvFile(t *testing.T) {
	cfg, err := LoadFlags(writeConfig(t, baseYAML), writeEnv(t, "HTTP_ROUTES_GRAPH_ENABLED=true\nHTTP_ROUTES_GRAPH_PATH=/internal/graph\n"))
	if err != nil {
		t.Fatal(err)
	}
	if g := cfg.HTTP.Routes.Graph; !g.Enabled || g.Path != "/internal/graph" {
		t.Fatalf("graph = %+v", g)
	}
}

func TestGraphRouteValidation(t *testing.T) {
	tests := []struct{ name, yaml, want string }{
		{"no slash", `http: {routes: {graph: {enabled: true, path: graph}}}`, "literal path"},
		{"root", `http: {routes: {graph: {enabled: true, path: /}}}`, "literal path"},
		{"trailing slash", `http: {routes: {graph: {enabled: true, path: /graph/}}}`, "literal path"},
		{"router param", `http: {routes: {graph: {enabled: true, path: "/{id}"}}}`, "literal path"},
		{"wildcard", `http: {routes: {graph: {enabled: true, path: "/graph/*"}}}`, "literal path"},
		{"markup", `http: {routes: {graph: {enabled: true, path: "/<x>"}}}`, "literal path"},
		{"dot segment", `http: {routes: {graph: {enabled: true, path: /a/../b}}}`, "dot segments"},
		{"health", `http: {routes: {health: {enabled: true}, graph: {enabled: true, path: /health}}}`, "http.routes.health"},
		{"readiness", `http: {routes: {readiness: {enabled: true}, graph: {enabled: true, path: /ready}}}`, "http.routes.readiness"},
		{"docs", `http: {docs: {enabled: true}, routes: {graph: {enabled: true, path: /docs}}}`, "http.docs"},
		{"docs spec", `http: {docs: {enabled: true}, routes: {graph: {enabled: true, path: /docs/swagger.json}}}`, "http.docs"},
		{"its script", `http: {routes: {health: {enabled: true, path: /graph/mermaid.min.js}, graph: {enabled: true}}}`, "http.routes.health"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadFlags(writeConfig(t, baseYAML+tt.yaml+"\n"), "")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.want)
			}
		})
	}
	// a disabled route is not validated, like every other optional section
	if _, err := LoadFlags(writeConfig(t, baseYAML+"http: {routes: {graph: {path: bad}}}\n"), ""); err != nil {
		t.Fatal(err)
	}
}

// Enabled in prd it warns exactly once and never refuses the startup.
func TestGraphWarnsOnceInProduction(t *testing.T) {
	count := func(yaml string) int {
		n := 0
		for _, w := range loadWarnings(t, yaml) {
			if strings.Contains(w, "http.routes.graph") {
				n++
			}
		}
		return n
	}
	on := "http: {routes: {graph: {enabled: true}}}\n"
	if got := count("env: prd\n" + on); got != 1 {
		t.Fatalf("prd: %d warnings, want 1", got)
	}
	if got := count("env: stg\n" + on); got != 0 {
		t.Fatalf("stg: %d warnings", got)
	}
	if got := count("env: prd\n"); got != 0 {
		t.Fatalf("disabled: %d warnings", got)
	}
}
