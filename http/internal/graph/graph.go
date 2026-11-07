// Package graph renders the module graph page of nhttp. Document is the one
// canonical form: the JSON tab is its encoding and the Mermaid source is derived
// from it, so the representations cannot disagree.
package graph

import (
	"bytes"
	"cmp"
	"compress/gzip"
	"crypto/rand"
	_ "embed" // the page and the Mermaid bundle
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ams-soft/nforge/module"
)

// SchemaVersion is the version of the Document layout.
const SchemaVersion = 1

// MaxOutputBytes bounds the JSON and the Mermaid source of one render.
const MaxOutputBytes = 1 << 20

// ErrTooLarge reports a graph over MaxOutputBytes.
var ErrTooLarge = errors.New("graph: output too large")

// MermaidVersion is the bundled release of Mermaid. assets/mermaid.min.js.gz
// is its dist/mermaid.min.js from npm, compressed with gzip -9 -n.
const MermaidVersion = "12.2.0"

//go:embed assets/mermaid.min.js.gz
var mermaidGzip []byte

//go:embed page.html
var pageSource string

// pageTemplate parses on first use: this package is linked into every nhttp
// binary, and with the route disabled it must cost nothing at startup.
var pageTemplate = sync.OnceValue(func() *template.Template {
	return template.Must(template.New("graph").Parse(pageSource))
})

// Document is the versioned description of the graph and its state. It holds
// module identifiers and states only, never configuration values or errors.
type Document struct {
	SchemaVersion     int      `json:"schema_version"`
	GeneratedAt       string   `json:"generated_at"`
	Modules           []Module `json:"modules"`
	Edges             []Edge   `json:"edges"`
	OperationalStatus []Status `json:"operational_status"`
}

// Module is one module of the graph.
type Module struct {
	ID                   string   `json:"id"`
	Enabled              bool     `json:"enabled"`
	Provides             []string `json:"provides"`
	Dependencies         []string `json:"dependencies"`
	OptionalDependencies []string `json:"optional_dependencies"`
	// StartupOrder is the 1-based start position, 0 for a disabled module.
	StartupOrder int `json:"startup_order"`
}

// Edge says that From uses Capability, published by To.
type Edge struct {
	From       string `json:"from"`
	To         string `json:"to"`
	Capability string `json:"capability"`
	Required   bool   `json:"required"`
}

// Status is the operational state of one module.
type Status struct {
	ID             string `json:"id"`
	Initialization string `json:"initialization"`
	Status         string `json:"status"` // ready | not_ready | disabled
}

// NewDocument joins the immutable structure and a status sample.
func NewDocument(snap module.Snapshot, status []module.Status, at time.Time) Document {
	doc := Document{
		SchemaVersion:     SchemaVersion,
		GeneratedAt:       at.UTC().Format(time.RFC3339),
		Modules:           make([]Module, len(snap.Modules)),
		Edges:             make([]Edge, len(snap.Edges)),
		OperationalStatus: make([]Status, len(status)),
	}
	for i, m := range snap.Modules {
		doc.Modules[i] = Module{
			ID:                   string(m.ID),
			Enabled:              m.Enabled,
			Provides:             toStrings(m.Provides),
			Dependencies:         toStrings(m.Dependencies),
			OptionalDependencies: toStrings(m.OptionalDependencies),
			StartupOrder:         m.StartupOrder,
		}
	}
	for i, e := range snap.Edges {
		doc.Edges[i] = Edge{From: string(e.From), To: string(e.To), Capability: string(e.Capability), Required: e.Required}
	}
	for i, s := range status {
		doc.OperationalStatus[i] = Status{ID: string(s.ID), Initialization: string(s.State), Status: string(s.Availability)}
	}
	return doc
}

func toStrings[S ~string](in []S) []string {
	out := make([]string, len(in)) // never nil: the JSON has [] and not null
	for i, v := range in {
		out[i] = string(v)
	}
	return out
}

// nodeID maps a module ID to a Mermaid identifier. The prefix keeps reserved
// words such as "end" out, and the escaping is injective: "a-b" and "a_b" differ.
func nodeID(id string) string {
	return "m_" + strings.NewReplacer("_", "__", "-", "_h").Replace(id)
}

// Mermaid returns the flowchart source. An arrow goes from a consumer to what it
// depends on; a dotted one is an optional dependency. Identifiers are restricted
// by the module package and are quoted here as well.
func (d Document) Mermaid() string {
	state := make(map[string]string, len(d.OperationalStatus))
	for _, s := range d.OperationalStatus {
		state[s.ID] = s.Status
	}
	var b strings.Builder
	b.WriteString("flowchart TD\n")
	for _, m := range d.Modules {
		class := "not_ready"
		if s, ok := state[m.ID]; ok {
			class = s
		}
		fmt.Fprintf(&b, "  %s[%q]:::%s\n", nodeID(m.ID), m.ID, class)
	}
	// One arrow per pair of modules, however many capabilities link them; a
	// required capability wins over an optional one.
	type pair struct{ from, to string }
	required := map[pair]bool{}
	var pairs []pair
	for _, e := range d.Edges {
		p := pair{e.From, e.To}
		if _, seen := required[p]; !seen {
			pairs = append(pairs, p)
		}
		required[p] = required[p] || e.Required
	}
	slices.SortFunc(pairs, func(a, b pair) int { return cmp.Or(cmp.Compare(a.from, b.from), cmp.Compare(a.to, b.to)) })
	for _, p := range pairs {
		arrow := "-.->"
		if required[p] {
			arrow = "-->"
		}
		fmt.Fprintf(&b, "  %s %s %s\n", nodeID(p.from), arrow, nodeID(p.to))
	}
	b.WriteString("  classDef ready fill:#d7f5e3,stroke:#1a7f4b,color:#0b3d24\n")
	b.WriteString("  classDef not_ready fill:#fbdcdc,stroke:#b42318,color:#5c120c\n")
	b.WriteString("  classDef disabled fill:#ececec,stroke:#8a8a8a,color:#555555,stroke-dasharray:4 3\n")
	return b.String()
}

// Rendered is a Document in its text forms, computed once per status sample.
type Rendered struct {
	JSON, Mermaid string
}

// Render encodes d, refusing a result over MaxOutputBytes.
func Render(d Document) (Rendered, error) {
	data, err := json.MarshalIndent(d, "", "  ") //nolint:gosec // module identifiers and states only: nothing here is a secret
	if err != nil {
		return Rendered{}, fmt.Errorf("graph: encode: %w", err)
	}
	r := Rendered{JSON: string(data), Mermaid: d.Mermaid()}
	if len(r.JSON) > MaxOutputBytes || len(r.Mermaid) > MaxOutputBytes {
		return Rendered{}, ErrTooLarge
	}
	return r, nil
}

// Page returns the HTML of r with a fresh nonce and the policy that allows it.
// Everything variable goes through html/template; the Mermaid script reads the
// source from the DOM, so no data is ever interpolated into JavaScript.
func Page(r Rendered, title, assetURL string) (page []byte, csp string, err error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return nil, "", fmt.Errorf("graph: nonce: %w", err)
	}
	nonce := base64.RawURLEncoding.EncodeToString(raw)
	var buf bytes.Buffer
	data := struct {
		Title, Nonce, AssetURL, JSON, Mermaid string
	}{title, nonce, assetURL, r.JSON, r.Mermaid}
	if err := pageTemplate().Execute(&buf, data); err != nil {
		return nil, "", fmt.Errorf("graph: render: %w", err)
	}
	return buf.Bytes(), pageCSP(nonce), nil
}

// pageCSP allows the page's own nonced scripts and nothing remote. Mermaid
// writes a <style> into the SVG and sets style attributes, which cannot carry a
// nonce, so styles need 'unsafe-inline'; scripts never do.
func pageCSP(nonce string) string {
	return "default-src 'none'; script-src 'nonce-" + nonce + "'; style-src 'unsafe-inline'; img-src data:; " +
		"base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
}

// MermaidGzip returns the compressed bundle. It must not be modified.
func MermaidGzip() []byte { return mermaidGzip }

// WriteMermaid decompresses the bundle to w, for a client without gzip.
func WriteMermaid(w io.Writer) error {
	zr, err := gzip.NewReader(bytes.NewReader(mermaidGzip))
	if err != nil {
		return fmt.Errorf("graph: bundle: %w", err)
	}
	defer func() { _ = zr.Close() }()
	if _, err := io.Copy(w, zr); err != nil { //nolint:gosec // the embedded bundle is trusted, fixed-size input
		return fmt.Errorf("graph: bundle: %w", err)
	}
	return nil
}
