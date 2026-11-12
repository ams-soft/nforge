package nhttp

import (
	"bufio"
	"net"
	stdhttp "net/http"
	"strconv"
	"sync/atomic"
	"time"

	router "github.com/ams-soft/go-ams-router"
)

// requestMetrics records, per request: nforge_http_requests_total{method,route,code},
// nforge_http_request_duration_seconds{method,route} and the
// nforge_http_requests_in_flight gauge. The route is the matched pattern (low
// cardinality), "unmatched" when nothing matched, never the raw path. A long-lived
// response that calls BeginStream stops counting as in flight and is not put in
// the duration histogram, so streams do not skew it.
func (s *Server) requestMetrics() func(stdhttp.Handler) stdhttp.Handler {
	reg := s.metrics
	total := reg.Counter("nforge_http_requests_total", "HTTP requests by method, matched route and status code.", "method", "route", "code")
	dur := reg.Histogram("nforge_http_request_duration_seconds", "HTTP request duration in seconds (long-lived streams excluded).", nil, "method", "route")
	inflight := reg.Gauge("nforge_http_requests_in_flight", "HTTP requests being served (streams excluded).")
	return func(next stdhttp.Handler) stdhttp.Handler {
		return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
			start := time.Now()
			inflight.Add(1)
			var released atomic.Bool
			release := func() {
				if released.CompareAndSwap(false, true) {
					inflight.Add(-1)
				}
			}
			ctx, ex := withExemptions(r.Context())
			ex.add(release)
			rec := &metricsWriter{ResponseWriter: w, status: stdhttp.StatusOK}
			defer func() {
				streamed := released.Load()
				release()
				route := "unmatched"
				if rc := router.RouteContext(r.Context()); rc != nil {
					if p := rc.RoutePattern(); p != "" {
						route = p
					}
				}
				total.Inc(r.Method, route, strconv.Itoa(rec.status))
				if !streamed {
					dur.Observe(time.Since(start).Seconds(), r.Method, route)
				}
			}()
			next.ServeHTTP(rec, r.WithContext(ctx))
		})
	}
}

// metricsWriter captures the status. It forwards Flush and Hijack and exposes
// Unwrap so http.ResponseController and type assertions still reach the real writer.
type metricsWriter struct {
	stdhttp.ResponseWriter
	status int
	wrote  bool
}

func (m *metricsWriter) WriteHeader(code int) {
	if !m.wrote {
		m.status, m.wrote = code, true
	}
	m.ResponseWriter.WriteHeader(code)
}

func (m *metricsWriter) Write(b []byte) (int, error) {
	m.wrote = true
	return m.ResponseWriter.Write(b)
}

func (m *metricsWriter) Flush() {
	if f, ok := m.ResponseWriter.(stdhttp.Flusher); ok {
		f.Flush()
		return
	}
	_ = stdhttp.NewResponseController(m.ResponseWriter).Flush()
}

func (m *metricsWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return stdhttp.NewResponseController(m.ResponseWriter).Hijack()
}

func (m *metricsWriter) Unwrap() stdhttp.ResponseWriter { return m.ResponseWriter }
