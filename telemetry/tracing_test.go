package telemetry

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/ams-soft/nforge/app"
	"github.com/ams-soft/nforge/internal/apptest"

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

func TestModuleSettings(t *testing.T) {
	cases := map[string]struct {
		yaml    string
		enabled bool
		trust   bool
		err     string
	}{
		"registered, section omitted": {yaml: "", enabled: true},
		"explicitly off":              {yaml: "tracing: {otel: {enabled: false}}\n"},
		"trust and ratio":             {yaml: "tracing: {otel: {sample_ratio: 0.1, trust_incoming: true, endpoint: 'localhost:4317', insecure: true}}\n", enabled: true, trust: true},
		"ratio out of range":          {yaml: "tracing: {otel: {sample_ratio: 1.5}}\n", err: "between 0 and 1"},
		"negative ratio":              {yaml: "tracing: {otel: {sample_ratio: -0.1}}\n", err: "between 0 and 1"},
		"nan ratio":                   {yaml: "tracing: {otel: {sample_ratio: .nan}}\n", err: "between 0 and 1"},
		"typo":                        {yaml: "tracing: {otel: {sample_rate: 1}}\n", err: "not found"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			a, err := apptest.New(t, tc.yaml, Module())
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := a.Graph().Modules[0].Enabled; got != tc.enabled {
				t.Fatalf("enabled = %v", got)
			}
			if !tc.enabled {
				return
			}
			if err := a.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = a.Shutdown(context.Background()) }()
			tr, ok := app.Value(a, Capability)
			if !ok || tr.Provider == nil || tr.TrustIncoming != tc.trust {
				t.Fatalf("tracing = %+v", tr)
			}
		})
	}
}

func TestSettingsNeverPrintCredentials(t *testing.T) {
	s := Settings{Otel: OtelSettings{Endpoint: "https://user:s3cr3t@collector:4317/?token=s3cr3t"}}
	for _, out := range []string{s.String(), s.GoString(), fmt.Sprint(s), fmt.Sprintf("%+v", s)} {
		if strings.Contains(out, "s3cr3t") {
			t.Fatalf("leaked: %s", out)
		}
	}
	b, _ := s.MarshalJSON()
	if strings.Contains(string(b), "s3cr3t") || RedactEndpoint("host:4317") != "host:4317" || RedactEndpoint("http://[::1") != "[REDACTED]" {
		t.Fatal("redaction")
	}
}
