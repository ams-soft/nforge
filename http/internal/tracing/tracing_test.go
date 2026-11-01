package tracing

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ams-soft/go-ams-router/middleware"
	"github.com/ams-soft/nforge/telemetry"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

const (
	callerTrace = "0af7651916cd43dd8448eb211c80319c"
	callerSpan  = "b7ad6b7169203331"
)

type seen struct {
	traceID   string
	requestID string
	baggage   string
	parent    bool
}

// serve sends one request carrying a traceparent, a baggage header and an
// X-Request-Id that all point at someone else's trace, and reports what the
// handler and the response saw.
func serve(t *testing.T, o Options, requestID string) (seen, *httptest.ResponseRecorder) {
	t.Helper()
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter), sdktrace.WithIDGenerator(telemetry.IDGenerator()))
	prevTP, prevProp := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	t.Cleanup(func() {
		otel.SetTracerProvider(prevTP)
		otel.SetTextMapPropagator(prevProp)
		_ = tp.Shutdown(t.Context())
	})

	var got seen
	h := Middleware(o)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sc := trace.SpanContextFromContext(r.Context())
		got.traceID = sc.TraceID().String()
		got.requestID = middleware.GetRequestID(r.Context())
		got.baggage = baggage.FromContext(r.Context()).Member("tenant").Value()
	}))
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("Traceparent", "00-"+callerTrace+"-"+callerSpan+"-01")
	req.Header.Set("Baggage", "tenant=victim")
	if requestID != "" {
		req.Header.Set("X-Request-Id", requestID)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("expected one span, got %d", len(spans))
	}
	got.parent = spans[0].Parent.IsValid()
	return got, rec
}

func TestTrustedIncomingContextIsContinued(t *testing.T) {
	got, rec := serve(t, Options{TrustIncoming: true}, "")
	if got.traceID != callerTrace || !got.parent {
		t.Errorf("a trusted traceparent must be continued: %+v", got)
	}
	if got.baggage != "victim" {
		t.Errorf("trusted baggage must be kept: %q", got.baggage)
	}
	if rec.Header().Get("X-Request-Id") != callerTrace {
		t.Errorf("request id = %q, want the trace id", rec.Header().Get("X-Request-Id"))
	}
}

func TestTrustedRequestIDBecomesTheTraceID(t *testing.T) {
	adopted := "11111111111111111111111111111111"
	// no traceparent parent to win: send only the request id
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter), sdktrace.WithIDGenerator(telemetry.IDGenerator()))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev); _ = tp.Shutdown(t.Context()) })
	var id string
	h := Middleware(Options{TrustIncoming: true})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id = trace.SpanContextFromContext(r.Context()).TraceID().String()
	}))
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("X-Request-Id", adopted)
	h.ServeHTTP(httptest.NewRecorder(), req)
	if id != adopted {
		t.Fatalf("trace id = %s, want the request id %s", id, adopted)
	}
}

// On a public edge the caller's trace context must not be honored (G-17): it
// would let a client force sampling, join another request's trace and inject
// baggage into every downstream call.
func TestUntrustedIncomingContextIsIgnored(t *testing.T) {
	forged := "22222222222222222222222222222222"
	got, rec := serve(t, Options{TrustIncoming: false}, forged)
	if got.traceID == callerTrace || got.traceID == forged || got.parent {
		t.Errorf("the request must start its own trace: %+v", got)
	}
	if got.baggage != "" {
		t.Errorf("baggage from the caller must be dropped, got %q", got.baggage)
	}
	if rid := rec.Header().Get("X-Request-Id"); rid != got.traceID || rid == forged {
		t.Errorf("request id %q must be the new trace id %q", rid, got.traceID)
	}
	if got.requestID != got.traceID {
		t.Errorf("request_id = %q, want trace_id %q", got.requestID, got.traceID)
	}
}
