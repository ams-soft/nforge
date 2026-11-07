package graph

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ams-soft/nforge/module"
)

func sample() Document {
	snap := module.Snapshot{
		Modules: []module.ModuleInfo{
			{ID: "cache", Enabled: true, Provides: []module.CapabilityID{"cache"}, Dependencies: []module.ID{"logger"}, StartupOrder: 2},
			{ID: "database", Provides: []module.CapabilityID{"database"}},
			{ID: "logger", Enabled: true, Provides: []module.CapabilityID{"logger"}, StartupOrder: 1},
			{ID: "my-mod", Enabled: true, OptionalDependencies: []module.ID{"logger"}, StartupOrder: 3},
			{ID: "my_mod", Enabled: true, StartupOrder: 4},
		},
		Edges: []module.Edge{
			{From: "cache", To: "logger", Capability: "logger", Required: true},
			{From: "my-mod", To: "logger", Capability: "logger"},
		},
		Order: []module.ID{"logger", "cache", "my-mod", "my_mod"},
	}
	status := []module.Status{
		{ID: "cache", State: module.StateStarted, Availability: module.NotReady},
		{ID: "database", State: module.StateDisabled, Availability: module.Disabled},
		{ID: "logger", State: module.StateStarted, Availability: module.Ready},
		{ID: "my-mod", State: module.StateStarted, Availability: module.Ready},
		{ID: "my_mod", State: module.StateStarted, Availability: module.Ready},
	}
	return NewDocument(snap, status, time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC))
}

func TestDocumentJSON(t *testing.T) {
	r, err := Render(sample())
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(r.JSON), &doc); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"schema_version", "generated_at", "modules", "edges", "operational_status"} {
		if _, ok := doc[key]; !ok {
			t.Errorf("missing %s", key)
		}
	}
	if doc["schema_version"] != float64(SchemaVersion) || doc["generated_at"] != "2026-11-06T12:00:00Z" {
		t.Fatalf("doc = %v", doc)
	}
	first := doc["modules"].([]any)[1].(map[string]any)
	if first["enabled"] != false || first["startup_order"] != float64(0) {
		t.Fatalf("database = %v", first)
	}
	if first["dependencies"] == nil {
		t.Fatal("dependencies must be [], not null")
	}
}

func TestMermaid(t *testing.T) {
	got := sample().Mermaid()
	for _, want := range []string{
		"flowchart TD\n",
		`m_cache["cache"]:::not_ready`,
		`m_database["database"]:::disabled`,
		`m_logger["logger"]:::ready`,
		"m_cache --> m_logger",
		"m_my_hmod -.-> m_logger",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if !strings.Contains(got, "m_my__mod") {
		t.Errorf("my-mod and my_mod must not collide:\n%s", got)
	}
	if got != sample().Mermaid() {
		t.Fatal("the source must be deterministic")
	}
}

func TestMermaidOneArrowPerPair(t *testing.T) {
	d := sample()
	d.Edges = []Edge{{From: "a", To: "b", Capability: "x"}, {From: "a", To: "b", Capability: "y", Required: true}}
	got := d.Mermaid()
	if strings.Count(got, "m_a ") != 1 || !strings.Contains(got, "m_a --> m_b") {
		t.Fatalf("source:\n%s", got)
	}
}

// Nothing reaches the page unescaped, whatever a future field carries.
func TestPageEscapes(t *testing.T) {
	evil := `</pre><script>alert(1)</script>`
	page, csp, err := Page(Rendered{JSON: evil, Mermaid: evil}, evil, "/graph/mermaid.min.js")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(page, []byte("<script>alert")) {
		t.Fatalf("unescaped input in:\n%s", page)
	}
	if bytes.Contains(page, []byte("http://")) || bytes.Contains(page, []byte("https://")) {
		t.Fatal("the page must reference nothing remote")
	}
	nonce := strings.TrimSuffix(strings.SplitN(csp, "'nonce-", 2)[1], "")
	nonce = nonce[:strings.IndexByte(nonce, '\'')]
	if got := bytes.Count(page, []byte(fmt.Sprintf(`nonce="%s"`, nonce))); got != 2 {
		t.Fatalf("%d scripts carry the nonce, want 2", got)
	}
	if strings.Contains(csp, "script-src 'unsafe") || strings.Contains(csp, "https:") {
		t.Fatalf("csp = %s", csp)
	}
	_, other, err := Page(Rendered{}, "t", "/a")
	if err != nil || other == csp {
		t.Fatal("every render needs a fresh nonce")
	}
}

func TestRenderBoundsTheOutput(t *testing.T) {
	d := sample()
	for i := range 40000 {
		d.Edges = append(d.Edges, Edge{From: fmt.Sprintf("module-%d", i), To: "x", Capability: "c"})
	}
	if _, err := Render(d); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v", err)
	}
}

func TestBundle(t *testing.T) {
	var out bytes.Buffer
	if err := WriteMermaid(&out); err != nil {
		t.Fatal(err)
	}
	if out.Len() < 1<<20 || !bytes.Contains(out.Bytes(), []byte("mermaid")) {
		t.Fatalf("bundle: %d bytes", out.Len())
	}
}
