package nhttp

import (
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ams-soft/nforge/metrics"
)

func metricsServer(t *testing.T) (*Server, *metrics.Registry) {
	t.Helper()
	reg := metrics.New()
	var cfg testConfig
	cfg.App.Name, cfg.App.Version = "t", "1.0.0"
	cfg.HTTP.Routes.Metrics.Enabled = true
	s := buildServer(&testDeps{Config: cfg, Metrics: reg})
	return s, reg
}

func TestRequestMetricsByRouteWithoutRawPaths(t *testing.T) {
	s, _ := metricsServer(t)
	s.Router().Get("/items/{id}", func(w stdhttp.ResponseWriter, r *stdhttp.Request) { w.WriteHeader(204) })
	s.Router().Get("/boom", func(w stdhttp.ResponseWriter, r *stdhttp.Request) { w.WriteHeader(500) })
	for _, p := range []string{"/items/1", "/items/2", "/items/3", "/boom", "/no/such/path/a", "/no/such/path/b"} {
		serveReq(s, httptest.NewRequest("GET", p, nil))
	}
	rec := serveReq(s, httptest.NewRequest("GET", "/metrics", nil))
	out := rec.Body.String()
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain; version=0.0.4") {
		t.Fatalf("/metrics: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	for _, want := range []string{
		`nforge_http_requests_total{code="204",method="GET",route="/items/{id}"} 3`,
		`nforge_http_requests_total{code="500",method="GET",route="/boom"} 1`,
		`nforge_http_requests_total{code="404",method="GET",route="unmatched"} 2`,
		`nforge_http_request_duration_seconds_count{method="GET",route="/items/{id}"} 3`,
		"nforge_http_requests_in_flight 1", // the scrape itself
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if strings.Contains(out, "/no/such/path") || strings.Contains(out, "/items/1") {
		t.Error("raw paths must never become label values")
	}
}

func TestStreamsAreNotInFlightOrInTheDurationHistogram(t *testing.T) {
	s, reg := metricsServer(t)
	done := make(chan struct{})
	s.Router().Get("/stream", func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		BeginStream(w, r)
		<-done
		w.WriteHeader(200)
	})
	finished := make(chan struct{})
	go func() { serveReq(s, httptest.NewRequest("GET", "/stream", nil)); close(finished) }()
	for i := 0; i < 100; i++ { // wait for the handler to be inside BeginStream
		var b strings.Builder
		_ = reg.Render(&b)
		if strings.Contains(b.String(), "nforge_http_requests_in_flight 0") && i > 5 {
			break
		}
		serveReq(s, httptest.NewRequest("GET", "/nothing", nil))
	}
	var b strings.Builder
	_ = reg.Render(&b)
	if !strings.Contains(b.String(), "nforge_http_requests_in_flight 0") {
		t.Fatalf("a stream must not count as in flight:\n%s", b.String())
	}
	close(done)
	<-finished
	b.Reset()
	_ = reg.Render(&b)
	if strings.Contains(b.String(), `route="/stream"} 1`) && strings.Contains(b.String(), `nforge_http_request_duration_seconds_count{method="GET",route="/stream"}`) {
		t.Error("a stream must not be observed in the duration histogram")
	}
	if !strings.Contains(b.String(), `nforge_http_requests_total{code="200",method="GET",route="/stream"} 1`) {
		t.Errorf("the stream request is still counted:\n%s", b.String())
	}
}

func TestMetricsRouteRequiresARegistry(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("enabling http.routes.metrics without a registry must panic at construction")
		}
	}()
	var cfg testConfig
	cfg.App.Name, cfg.App.Version = "t", "1.0.0"
	cfg.HTTP.Routes.Metrics.Enabled = true
	buildServer(&testDeps{Config: cfg})
}
