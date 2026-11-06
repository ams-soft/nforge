//go:build nforge_graph

package nhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"html"
	"log/slog"
	stdhttp "net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ams-soft/nforge/cache"
	"github.com/ams-soft/nforge/codec"
	"github.com/ams-soft/nforge/config"
	"github.com/ams-soft/nforge/deps"
	"github.com/ams-soft/nforge/http/internal/graph"
	"github.com/ams-soft/nforge/module"
)

func graphConfig() config.Config {
	var cfg config.Config
	cfg.Env, cfg.App.Name, cfg.App.Version = "local", "graph-app", "1"
	cfg.Codec.Formats.Enabled = []string{"json"}
	cfg.Codec.Default = "json"
	cfg.HTTP.Routes.Graph.Enabled = true
	return cfg
}

// keepDefaultCodec restores the process-wide registry that bootstrap replaces.
func keepDefaultCodec(t *testing.T) {
	t.Helper()
	previous := codec.Default()
	t.Cleanup(func() { codec.SetDefault(previous) })
}

func graphServer(t *testing.T, cfg config.Config, extra ...deps.Module) *ServerHttp {
	t.Helper()
	keepDefaultCodec(t)
	d, err := deps.Bootstrap(context.Background(), cfg, deps.WithModules(extra...))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Shutdown(context.Background()) })
	return NewHTTPServer(d)
}

func getGraph(s *ServerHttp, path string, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), stdhttp.MethodGet, path, nil)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	s.Srv.Handler.ServeHTTP(rec, req)
	return rec
}

var preRe = regexp.MustCompile(`(?s)<pre id="json-source">(.*?)</pre>`)

func graphDocument(t *testing.T, body string) graph.Document {
	t.Helper()
	m := preRe.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no JSON tab in:\n%s", body)
	}
	var doc graph.Document
	if err := json.Unmarshal([]byte(html.UnescapeString(m[1])), &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestGraphDisabledRegistersNothing(t *testing.T) {
	cfg := graphConfig()
	cfg.HTTP.Routes.Graph.Enabled = false
	s := graphServer(t, cfg)
	for _, path := range []string{"/graph", "/graph/mermaid.min.js"} {
		if got := getGraph(s, path).Code; got != stdhttp.StatusNotFound {
			t.Errorf("%s = %d, want 404", path, got)
		}
	}
	// the structure still exists: the orchestrator needs it
	if _, ok := s.Deps.Graph(); !ok {
		t.Fatal("the graph is resolved whether or not the route is on")
	}
}

func TestGraphPage(t *testing.T) {
	s := graphServer(t, graphConfig())
	rec := getGraph(s, "/graph")
	if rec.Code != stdhttp.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	h := rec.Header()
	if got := h.Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("content type %q", got)
	}
	if h.Get("Cache-Control") != "no-store" || h.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("headers: %v", h)
	}
	csp := h.Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'none'", "script-src 'nonce-", "frame-ancestors 'none'", "base-uri 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("csp %q lacks %q", csp, want)
		}
	}
	body := rec.Body.String()
	for _, want := range []string{`id="tab-visual"`, `id="tab-mermaid"`, `id="tab-json"`, "flowchart TD", `src="/graph/mermaid.min.js"`} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(body, "cdn") || strings.Contains(body, "https://") {
		t.Error("the page must be self-contained")
	}

	doc := graphDocument(t, body)
	state := map[string]graph.Status{}
	for _, st := range doc.OperationalStatus {
		state[st.ID] = st
	}
	if state["logger"].Status != "ready" || state["database"].Status != "disabled" || state["database"].Initialization != "disabled" {
		t.Fatalf("status = %+v", doc.OperationalStatus)
	}
	var order []string
	for _, m := range doc.Modules {
		if m.ID == "database" && (m.Enabled || m.StartupOrder != 0) {
			t.Fatalf("database = %+v", m)
		}
		order = append(order, m.ID)
	}
	if !slices.IsSorted(order) {
		t.Fatalf("modules must be sorted: %v", order)
	}
}

func TestGraphCustomPath(t *testing.T) {
	cfg := graphConfig()
	cfg.HTTP.Routes.Graph.Path = "/internal/topology"
	s := graphServer(t, cfg)
	if got := getGraph(s, "/internal/topology").Code; got != stdhttp.StatusOK {
		t.Fatalf("status = %d", got)
	}
	if got := getGraph(s, "/graph").Code; got != stdhttp.StatusNotFound {
		t.Fatalf("/graph = %d", got)
	}
}

func TestGraphAsset(t *testing.T) {
	s := graphServer(t, graphConfig())
	gz := getGraph(s, "/graph/mermaid.min.js", "Accept-Encoding", "br, gzip")
	if gz.Code != stdhttp.StatusOK || gz.Header().Get("Content-Encoding") != "gzip" || !bytes.Equal(gz.Body.Bytes(), graph.MermaidGzip()) {
		t.Fatalf("gzip: %d %v", gz.Code, gz.Header())
	}
	if got := gz.Header().Get("Content-Type"); got != "text/javascript; charset=utf-8" {
		t.Fatalf("content type %q", got)
	}
	plain := getGraph(s, "/graph/mermaid.min.js", "Accept-Encoding", "gzip;q=0")
	if plain.Header().Get("Content-Encoding") != "" || plain.Body.Len() <= gz.Body.Len() {
		t.Fatalf("plain: %v, %d bytes", plain.Header(), plain.Body.Len())
	}
	if got := getGraph(s, "/graph/mermaid.min.js", "If-None-Match", gz.Header().Get("ETag")); got.Code != stdhttp.StatusNotModified || got.Body.Len() != 0 {
		t.Fatalf("revalidation: %d", got.Code)
	}
}

// The API policy of http.security would block the page; report-only mode must
// not weaken what the page enforces.
func TestGraphReplacesTheSecurityPolicy(t *testing.T) {
	cfg := graphConfig()
	cfg.HTTP.Security.CSP.ReportOnly = true
	rec := getGraph(graphServer(t, cfg), "/graph")
	h := rec.Header()
	if h.Get("Content-Security-Policy-Report-Only") != "" || !strings.Contains(h.Get("Content-Security-Policy"), "'nonce-") {
		t.Fatalf("headers: %v", h)
	}
	if h.Get("X-Frame-Options") == "" {
		t.Fatal("the other http.security headers stay")
	}
}

// Secrets in the configuration must not reach the page in any tab.
func TestGraphLeaksNoConfiguration(t *testing.T) {
	const secret = "s3cr3t-value"
	cfg := graphConfig()
	cfg.Cache = cache.Config{Enabled: true, Driver: cache.DriverMemory, Prefix: secret}
	cache.ApplyDefaults(&cfg.Cache)
	cfg.Database.Password = secret
	cfg.Database.DSN = "postgres://u:" + secret + "@db.internal.example/x"
	cfg.Logging.Output.Otel.Endpoint = "http://" + secret + ":4317"
	body := getGraph(graphServer(t, cfg), "/graph").Body.String()
	for _, leak := range []string{secret, "db.internal", "postgres://", "4317"} {
		if strings.Contains(body, leak) {
			t.Errorf("the page leaks %q", leak)
		}
	}
}

// The state is live: a dependency that fails shows as not ready after the
// sample expires, the structure does not change, and no error text is exposed.
func TestGraphStatusIsDynamic(t *testing.T) {
	var down atomic.Bool
	flaky := deps.Module{ID: "flaky", Init: func(context.Context, config.Config, *module.Scope) (module.Hooks, error) {
		return module.Hooks{Ready: func(context.Context) error {
			if down.Load() {
				return errors.New("dial tcp 10.0.0.9:5432: secret detail")
			}
			return nil
		}}, nil
	}}
	s := graphServer(t, graphConfig(), flaky)
	status := func() string {
		for _, st := range graphDocument(t, getGraph(s, "/graph").Body.String()).OperationalStatus {
			if st.ID == "flaky" {
				return st.Status
			}
		}
		return ""
	}
	if got := status(); got != "ready" {
		t.Fatalf("status = %q", got)
	}
	down.Store(true)
	if got := status(); got != "ready" {
		t.Fatalf("a sample serves for %s: %q", graphStatusTTL, got)
	}
	expireGraphSample(t, s)
	body := getGraph(s, "/graph").Body.String()
	if !strings.Contains(body, `:::not_ready`) || strings.Contains(body, "10.0.0.9") {
		t.Fatal("the failure must show as a state, without its text")
	}
}

func expireGraphSample(t *testing.T, s *ServerHttp) {
	t.Helper()
	s.graph.mu.Lock()
	defer s.graph.mu.Unlock()
	s.graph.sampled = time.Time{}
}

func TestGraphBurstCostsOneProbe(t *testing.T) {
	var probes atomic.Int32
	counted := deps.Module{ID: "counted", Init: func(context.Context, config.Config, *module.Scope) (module.Hooks, error) {
		return module.Hooks{Ready: func(context.Context) error {
			probes.Add(1)
			time.Sleep(10 * time.Millisecond)
			return nil
		}}, nil
	}}
	s := graphServer(t, graphConfig(), counted)
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() { _ = getGraph(s, "/graph") })
	}
	wg.Wait()
	if got := probes.Load(); got != 1 {
		t.Fatalf("%d probes for one burst", got)
	}
}

func TestGraphPanicsOnMisuse(t *testing.T) {
	mustPanic := func(name, want string, fn func()) {
		t.Helper()
		defer func() {
			if r, _ := recover().(string); !strings.Contains(r, want) {
				t.Fatalf("%s: panic = %q, want %q", name, r, want)
			}
		}()
		fn()
	}
	cfg := graphConfig()
	mustPanic("no plan", "deps.NewDeps", func() { NewHTTPServer(&deps.Deps{Config: cfg, Logger: slog.New(slog.DiscardHandler)}) })
	cfg.HTTP.Routes.Health.Enabled, cfg.HTTP.Routes.Health.Path = true, "/graph"
	keepDefaultCodec(t)
	d, err := deps.NewDepsE(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Shutdown(context.Background()) })
	mustPanic("collision", "collides", func() { NewHTTPServer(d) })
}

func TestAcceptsGzip(t *testing.T) {
	for header, want := range map[string]bool{
		"": false, "br": false, "gzip": true, "GZIP": true, "br, gzip;q=0.5": true, "gzip;q=0": false, "gzip; q=0.0": false, "x-gzip": false,
	} {
		if got := acceptsGzip(header); got != want {
			t.Errorf("%q = %v", header, got)
		}
	}
}
