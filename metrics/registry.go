package metrics

import (
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// maxSeriesPerInstrument bounds the label sets of one instrument.
const maxSeriesPerInstrument = 5000

// Registry holds instruments and collectors and renders them. It is safe for
// concurrent use. The zero value is not usable: use New.
type Registry struct {
	mu          sync.RWMutex
	instruments map[string]instrument
	order       []string
	collectors  []*collector
	dropped     atomic.Uint64
}

// New returns an empty registry.
func New() *Registry { return &Registry{instruments: map[string]instrument{}} }

type instrument interface {
	write(w io.Writer)
	meta() (name, help, typ string)
}

func (r *Registry) add(name string, in instrument) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.instruments[name]; dup {
		panic("metrics: " + name + " is already registered")
	}
	if !validName(name) {
		panic("metrics: invalid metric name " + strconv.Quote(name))
	}
	r.instruments[name] = in
	r.order = append(r.order, name)
}

func validName(s string) bool {
	if s == "" {
		return false
	}
	for i, c := range s {
		ok := c == '_' || c == ':' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (i > 0 && c >= '0' && c <= '9')
		if !ok {
			return false
		}
	}
	return true
}

// Sample is one value of a collector: a label set and a number.
type Sample struct {
	Labels map[string]string
	Value  float64
}

type collector struct {
	name, help, typ string
	f               func() []Sample
}

// Func registers a metric whose samples are computed at scrape time (a gauge
// or counter read from another component's state). typ is "gauge" or "counter".
func (r *Registry) Func(name, help, typ string, f func() []Sample) {
	if typ != "gauge" && typ != "counter" {
		panic("metrics: Func type must be gauge or counter")
	}
	if !validName(name) {
		panic("metrics: invalid metric name " + strconv.Quote(name))
	}
	r.mu.Lock()
	r.collectors = append(r.collectors, &collector{name: name, help: help, typ: typ, f: f})
	r.mu.Unlock()
}

// Value is the single-sample form of Func.
func (r *Registry) Value(name, help, typ string, f func() float64) {
	r.Func(name, help, typ, func() []Sample { return []Sample{{Value: f()}} })
}

// Handler serves the Prometheus text format (version 0.0.4).
func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet && req.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if req.Method == http.MethodHead {
			return
		}
		_ = r.Render(w)
	})
}

// Render writes every instrument and collector.
func (r *Registry) Render(w io.Writer) error {
	r.mu.RLock()
	names := append([]string(nil), r.order...)
	ins := make([]instrument, 0, len(names))
	for _, n := range names {
		ins = append(ins, r.instruments[n])
	}
	cols := append([]*collector(nil), r.collectors...)
	r.mu.RUnlock()
	bw := &errWriter{w: w}
	for _, in := range ins {
		name, help, typ := in.meta()
		header(bw, name, help, typ)
		in.write(bw)
	}
	for _, c := range cols {
		header(bw, c.name, c.help, c.typ)
		samples := c.f()
		sort.SliceStable(samples, func(i, j int) bool { return labelKey(samples[i].Labels) < labelKey(samples[j].Labels) })
		for _, s := range samples {
			fmt.Fprintf(bw, "%s%s %s\n", c.name, renderLabels(s.Labels), formatFloat(s.Value))
		}
	}
	header(bw, "nforge_metrics_dropped_series_total", "Label sets dropped because an instrument reached its series cap.", "counter")
	fmt.Fprintf(bw, "nforge_metrics_dropped_series_total %d\n", r.dropped.Load())
	return bw.err
}

type errWriter struct {
	w   io.Writer
	err error
}

func (e *errWriter) Write(p []byte) (int, error) {
	if e.err != nil {
		return 0, e.err
	}
	n, err := e.w.Write(p)
	e.err = err
	return n, err
}

func header(w io.Writer, name, help, typ string) {
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", name, escapeHelp(help), name, typ)
}

func escapeHelp(s string) string {
	return strings.NewReplacer(`\`, `\\`, "\n", `\n`).Replace(s)
}

func escapeLabel(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s)
}

func labelKey(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k + "\x00" + m[k] + "\x00")
	}
	return b.String()
}

func renderLabels(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + `="` + escapeLabel(m[k]) + `"`
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func formatFloat(v float64) string {
	switch {
	case math.IsInf(v, 1):
		return "+Inf"
	case math.IsInf(v, -1):
		return "-Inf"
	case math.IsNaN(v):
		return "NaN"
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// vec is the shared label-set bookkeeping of the three instrument kinds.
type vec[T any] struct {
	reg     *Registry
	name    string
	help    string
	typ     string
	labels  []string
	mu      sync.RWMutex
	series  map[string]*T
	values  map[string][]string
	newItem func() *T
}

func newVec[T any](r *Registry, name, help, typ string, labels []string, mk func() *T) *vec[T] {
	for _, l := range labels {
		if !validName(l) || strings.HasPrefix(l, "__") {
			panic("metrics: invalid label name " + strconv.Quote(l))
		}
	}
	return &vec[T]{reg: r, name: name, help: help, typ: typ, labels: labels, series: map[string]*T{}, values: map[string][]string{}, newItem: mk}
}

func (v *vec[T]) meta() (string, string, string) { return v.name, v.help, v.typ }

// get returns the child for the label values, or nil when the series cap is reached.
func (v *vec[T]) get(vals []string) *T {
	if len(vals) != len(v.labels) {
		panic(fmt.Sprintf("metrics: %s takes %d label values, got %d", v.name, len(v.labels), len(vals)))
	}
	key := strings.Join(vals, "\x00")
	v.mu.RLock()
	c := v.series[key]
	v.mu.RUnlock()
	if c != nil {
		return c
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if c = v.series[key]; c != nil {
		return c
	}
	if len(v.series) >= maxSeriesPerInstrument {
		v.reg.dropped.Add(1)
		return nil
	}
	c = v.newItem()
	v.series[key] = c
	v.values[key] = append([]string(nil), vals...)
	return c
}

// each visits the series in a stable order.
func (v *vec[T]) each(f func(labels string, extra func(string) string, c *T)) {
	v.mu.RLock()
	keys := make([]string, 0, len(v.series))
	for k := range v.series {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	type pair struct {
		vals []string
		c    *T
	}
	items := make([]pair, len(keys))
	for i, k := range keys {
		items[i] = pair{v.values[k], v.series[k]}
	}
	v.mu.RUnlock()
	for _, it := range items {
		m := make(map[string]string, len(v.labels))
		for i, l := range v.labels {
			m[l] = it.vals[i]
		}
		f(renderLabels(m), func(extra string) string {
			base := renderLabels(m)
			if base == "" {
				return "{" + extra + "}"
			}
			return base[:len(base)-1] + "," + extra + "}"
		}, it.c)
	}
}

// ---- Counter

// CounterVec is a counter family keyed by label values.
type CounterVec struct{ *vec[atomic.Uint64] }

// Counter registers a monotonically increasing counter.
func (r *Registry) Counter(name, help string, labelNames ...string) *CounterVec {
	c := &CounterVec{newVec(r, name, help, "counter", labelNames, func() *atomic.Uint64 { return new(atomic.Uint64) })}
	r.add(name, c)
	return c
}

// Inc adds one to the counter of the label values.
func (c *CounterVec) Inc(labelValues ...string) { c.Add(1, labelValues...) }

// Add adds n (never negative) to the counter of the label values.
func (c *CounterVec) Add(n uint64, labelValues ...string) {
	if s := c.get(labelValues); s != nil {
		s.Add(n)
	}
}

func (c *CounterVec) write(w io.Writer) {
	c.each(func(labels string, _ func(string) string, s *atomic.Uint64) {
		fmt.Fprintf(w, "%s%s %d\n", c.name, labels, s.Load())
	})
}

// ---- Gauge

// GaugeVec is a gauge family keyed by label values.
type GaugeVec struct{ *vec[atomic.Uint64] } // float64 bits

// Gauge registers a value that goes up and down.
func (r *Registry) Gauge(name, help string, labelNames ...string) *GaugeVec {
	g := &GaugeVec{newVec(r, name, help, "gauge", labelNames, func() *atomic.Uint64 { return new(atomic.Uint64) })}
	r.add(name, g)
	return g
}

// Set stores v.
func (g *GaugeVec) Set(v float64, labelValues ...string) {
	if s := g.get(labelValues); s != nil {
		s.Store(math.Float64bits(v))
	}
}

// Add adds delta (negative allowed).
func (g *GaugeVec) Add(delta float64, labelValues ...string) {
	s := g.get(labelValues)
	if s == nil {
		return
	}
	for {
		old := s.Load()
		if s.CompareAndSwap(old, math.Float64bits(math.Float64frombits(old)+delta)) {
			return
		}
	}
}

func (g *GaugeVec) write(w io.Writer) {
	g.each(func(labels string, _ func(string) string, s *atomic.Uint64) {
		fmt.Fprintf(w, "%s%s %s\n", g.name, labels, formatFloat(math.Float64frombits(s.Load())))
	})
}

// ---- Histogram

type histSeries struct {
	mu      sync.Mutex
	counts  []uint64
	sum     float64
	samples uint64
}

// HistogramVec is a histogram family keyed by label values.
type HistogramVec struct {
	*vec[histSeries]
	buckets []float64
}

// DefaultBuckets suit request durations in seconds.
var DefaultBuckets = []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10}

// Histogram registers a histogram with the given upper bounds (ascending).
func (r *Registry) Histogram(name, help string, buckets []float64, labelNames ...string) *HistogramVec {
	if len(buckets) == 0 {
		buckets = DefaultBuckets
	}
	if !sort.Float64sAreSorted(buckets) {
		panic("metrics: histogram buckets must be ascending")
	}
	bs := append([]float64(nil), buckets...)
	h := &HistogramVec{buckets: bs}
	h.vec = newVec(r, name, help, "histogram", labelNames, func() *histSeries { return &histSeries{counts: make([]uint64, len(bs))} })
	r.add(name, h)
	return h
}

// Observe records one value.
func (h *HistogramVec) Observe(v float64, labelValues ...string) {
	s := h.get(labelValues)
	if s == nil {
		return
	}
	s.mu.Lock()
	for i, b := range h.buckets {
		if v <= b {
			s.counts[i]++
			break
		}
	}
	s.sum += v
	s.samples++
	s.mu.Unlock()
}

func (h *HistogramVec) write(w io.Writer) {
	h.each(func(labels string, extra func(string) string, s *histSeries) {
		s.mu.Lock()
		counts, sum, n := append([]uint64(nil), s.counts...), s.sum, s.samples
		s.mu.Unlock()
		var cum uint64
		for i, b := range h.buckets {
			cum += counts[i]
			fmt.Fprintf(w, "%s_bucket%s %d\n", h.name, extra(`le="`+formatFloat(b)+`"`), cum)
		}
		fmt.Fprintf(w, "%s_bucket%s %d\n", h.name, extra(`le="+Inf"`), n)
		fmt.Fprintf(w, "%s_sum%s %s\n%s_count%s %d\n", h.name, labels, formatFloat(sum), h.name, labels, n)
	})
}
