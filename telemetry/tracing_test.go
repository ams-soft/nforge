package telemetry

import (
	"context"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

func sampled(t *testing.T, ratio float64, n int) int {
	t.Helper()
	s := newSampler(ratio)
	gen := IDGenerator()
	hits := 0
	for range n {
		tid, _ := gen.NewIDs(context.Background())
		res := s.ShouldSample(sdktrace.SamplingParameters{ParentContext: context.Background(), TraceID: tid, Name: "x"})
		if res.Decision == sdktrace.RecordAndSample {
			hits++
		}
	}
	return hits
}

func TestSampleRatio(t *testing.T) {
	const n = 4000
	if got := sampled(t, 1, n); got != n {
		t.Errorf("ratio 1 sampled %d of %d", got, n)
	}
	if got := sampled(t, 0, n); got != 0 {
		t.Errorf("ratio 0 sampled %d of %d", got, n)
	}
	got := sampled(t, 0.25, n)
	if got < n*18/100 || got > n*32/100 {
		t.Errorf("ratio 0.25 sampled %d of %d (want about a quarter)", got, n)
	}
}

func TestIDGeneratorUsesTheTraceIDInTheContext(t *testing.T) {
	want, ok := ParseTraceID("0af7651916cd43dd8448eb211c80319c")
	if !ok {
		t.Fatal("bad fixture")
	}
	ctx := ContextWithTraceID(context.Background(), want)
	if got, _ := IDGenerator().NewIDs(ctx); got != want {
		t.Fatalf("trace id = %s, want %s", got, want)
	}
	if got, _ := IDGenerator().NewIDs(context.Background()); !got.IsValid() || got == (trace.TraceID{}) {
		t.Fatalf("a root span without a context id gets a random valid one, got %s", got)
	}
}
