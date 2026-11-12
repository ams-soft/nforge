package metrics

import (
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func render(t *testing.T, r *Registry) string {
	t.Helper()
	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain; version=0.0.4") {
		t.Fatalf("content type %q", ct)
	}
	return rec.Body.String()
}

func TestCounterGaugeHistogramExposition(t *testing.T) {
	r := New()
	c := r.Counter("app_requests_total", "Requests.", "method", "code")
	c.Inc("GET", "200")
	c.Add(2, "GET", "200")
	c.Inc("POST", "500")
	g := r.Gauge("app_inflight", "In flight.")
	g.Add(3)
	g.Add(-1)
	h := r.Histogram("app_duration_seconds", "Duration.", []float64{0.1, 1}, "route")
	h.Observe(0.05, "/a")
	h.Observe(0.5, "/a")
	h.Observe(5, "/a")
	r.Value("app_up", "Up.", "gauge", func() float64 { return 1 })
	r.Func("app_pool", "Pool.", "gauge", func() []Sample {
		return []Sample{{Labels: map[string]string{"state": "idle"}, Value: 4}, {Labels: map[string]string{"state": "busy"}, Value: 1}}
	})
	out := render(t, r)
	for _, want := range []string{
		"# HELP app_requests_total Requests.\n# TYPE app_requests_total counter\n",
		`app_requests_total{code="200",method="GET"} 3` + "\n",
		`app_requests_total{code="500",method="POST"} 1` + "\n",
		"app_inflight 2\n",
		`app_duration_seconds_bucket{route="/a",le="0.1"} 1` + "\n",
		`app_duration_seconds_bucket{route="/a",le="1"} 2` + "\n",
		`app_duration_seconds_bucket{route="/a",le="+Inf"} 3` + "\n",
		`app_duration_seconds_sum{route="/a"} 5.55` + "\n",
		`app_duration_seconds_count{route="/a"} 3` + "\n",
		"app_up 1\n",
		`app_pool{state="busy"} 1` + "\n" + `app_pool{state="idle"} 4` + "\n",
		"nforge_metrics_dropped_series_total 0\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestLabelAndHelpEscaping(t *testing.T) {
	r := New()
	r.Counter("x_total", "line1\nline2 \\ back", "v").Inc("a\"b\\c\nd")
	out := render(t, r)
	if !strings.Contains(out, `# HELP x_total line1\nline2 \\ back`) || !strings.Contains(out, `x_total{v="a\"b\\c\nd"} 1`) {
		t.Fatalf("escaping:\n%s", out)
	}
}

func TestSeriesCapDropsInsteadOfGrowing(t *testing.T) {
	r := New()
	c := r.Counter("c_total", "c", "id")
	for i := range maxSeriesPerInstrument + 50 {
		c.Inc(strings.Repeat("x", 1) + string(rune('a'+i%26)) + string(rune('0'+i/26%10)) + string(rune('A'+i/260%26)) + string(rune('0'+i/6760)))
	}
	if r.dropped.Load() == 0 {
		t.Fatal("past the cap new label sets must be dropped and counted")
	}
	if !strings.Contains(render(t, r), "nforge_metrics_dropped_series_total ") {
		t.Fatal("the drop counter is exposed")
	}
}

func TestMisuse(t *testing.T) {
	r := New()
	r.Counter("dup_total", "d")
	for name, f := range map[string]func(){
		"duplicate":    func() { r.Counter("dup_total", "d") },
		"bad name":     func() { r.Counter("1bad", "d") },
		"bad label":    func() { r.Counter("ok_total", "d", "__x") },
		"label arity":  func() { r.Counter("ar_total", "d", "a").Inc() },
		"bucket order": func() { r.Histogram("h_seconds", "h", []float64{2, 1}) },
		"func type":    func() { r.Func("f", "f", "histogram", nil) },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s must panic", name)
				}
			}()
			f()
		}()
	}
}

func TestConcurrentUse(t *testing.T) {
	r := New()
	c := r.Counter("c_total", "c", "k")
	g := r.Gauge("g", "g")
	h := r.Histogram("h_seconds", "h", nil, "k")
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			for range 2000 {
				c.Inc("a")
				g.Add(1)
				h.Observe(0.01, "k")
				if i == 0 {
					_ = render(t, r)
				}
			}
		})
	}
	wg.Wait()
	out := render(t, r)
	if !strings.Contains(out, `c_total{k="a"} 16000`) || !strings.Contains(out, "g 16000") || !strings.Contains(out, `h_seconds_count{k="k"} 16000`) {
		t.Fatalf("lost updates:\n%s", out)
	}
}
