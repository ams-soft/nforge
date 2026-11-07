package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/url"
	"strings"

	"github.com/ams-soft/nforge/module"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
)

// Tracing is what the tracing module publishes: the application's provider and
// whether inbound trace context is trusted.
type Tracing struct {
	Provider trace.TracerProvider
	// TrustIncoming makes the HTTP tracing middleware continue an incoming
	// traceparent, adopt a valid X-Request-Id and keep baggage.
	TrustIncoming bool
}

// Capability is the tracing of the application, published by Module.
var Capability = module.NewCapability[*Tracing]("tracing")

// Settings is the tracing configuration section.
type Settings struct {
	Otel OtelSettings `yaml:"otel"`
}

// OtelSettings configures the OTLP/gRPC span exporter.
type OtelSettings struct {
	// Enabled is on unless set to false: registering the tracing module is the
	// opt-in.
	Enabled  *bool  `yaml:"enabled"`  // default: true
	Endpoint string `yaml:"endpoint"` // "host:port" or URL (http:// implies insecure); default: "localhost:4317"
	Insecure bool   `yaml:"insecure"` // default: false

	// SampleRatio is the fraction of requests whose spans are recorded and
	// exported, decided from the trace id; a request that arrives with a
	// sampled parent is still recorded when trust_incoming is on.
	SampleRatio *float64 `yaml:"sample_ratio"` // 0..1; default: 1 (every request)
	// TrustIncoming continues a traceparent, adopts a valid X-Request-Id as the
	// trace id and propagates baggage received from the caller. Behind a
	// gateway or between services that is what links the traces. It is off by
	// default: every request then starts its own trace, so a client of an
	// exposed API can neither force sampling, nor reuse someone else's trace
	// id, nor inject baggage.
	TrustIncoming *bool `yaml:"trust_incoming"` // default: false
}

// IsEnabled reports whether spans are exported.
func (o OtelSettings) IsEnabled() bool { return o.Enabled == nil || *o.Enabled }

// Ratio returns the effective sample ratio.
func (o OtelSettings) Ratio() float64 {
	if o.SampleRatio == nil {
		return 1
	}
	return *o.SampleRatio
}

// TrustsIncoming reports whether inbound trace context is trusted (opt-in).
func (o OtelSettings) TrustsIncoming() bool { return o.TrustIncoming != nil && *o.TrustIncoming }

// Validate checks the settings.
func (s Settings) Validate() error {
	if r := s.Otel.Ratio(); r < 0 || r > 1 || math.IsNaN(r) {
		return fmt.Errorf("tracing.otel.sample_ratio must be between 0 and 1, got %v", r)
	}
	return nil
}

// Redacted returns a copy without the endpoint's credentials.
func (s Settings) Redacted() Settings {
	s.Otel.Endpoint = RedactEndpoint(s.Otel.Endpoint)
	return s
}

type plainSettings Settings

// String prints the redacted settings.
func (s Settings) String() string { return fmt.Sprintf("%+v", plainSettings(s.Redacted())) }

// GoString prints the redacted settings.
func (s Settings) GoString() string { return fmt.Sprintf("%#v", plainSettings(s.Redacted())) }

// LogValue logs the redacted settings.
func (s Settings) LogValue() slog.Value { return slog.AnyValue(plainSettings(s.Redacted())) }

// MarshalJSON marshals the redacted settings.
func (s Settings) MarshalJSON() ([]byte, error) { return json.Marshal(plainSettings(s.Redacted())) }

const redacted = "[REDACTED]"

// RedactEndpoint masks the userinfo of an endpoint URL
// ("https://user:token@host:4317") and drops its query; a bare "host:port" has
// neither and is returned unchanged.
func RedactEndpoint(endpoint string) string {
	if !strings.Contains(endpoint, "://") {
		return endpoint
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return redacted
	}
	if u.User != nil {
		u.User = url.User(redacted)
	}
	u.RawQuery = ""
	return u.String()
}

// Module is the tracing module: it owns the tracing section and publishes the
// application's TracerProvider (OTLP/gRPC, batched, with the trace id taken
// from X-Request-Id for root spans). With app.WithProcessGlobals it also
// installs the provider and the W3C propagator as the OTel globals.
// tracing.otel.enabled: false disables the module.
func Module() module.Descriptor {
	return module.Descriptor{ID: "tracing", Section: "tracing", Configure: func(src module.Source) (module.Definition, error) {
		var s Settings
		if err := src.Decode(&s); err != nil {
			return module.Definition{}, err
		}
		if err := s.Validate(); err != nil {
			return module.Definition{}, err
		}
		app, globals := src.App(), src.ProcessGlobals()
		return module.Definition{
			Disabled: !s.Otel.IsEnabled(),
			Provides: []module.Handle{Capability},
			Optional: []module.Handle{module.Logger},
			Config:   s,
			Init: func(ctx context.Context, sc *module.Scope) (module.Hooks, error) {
				provider, err := NewTracerProvider(ctx, s, app)
				if err != nil {
					return module.Hooks{}, fmt.Errorf("unable to setup tracing: %w", err)
				}
				if globals {
					otel.SetTextMapPropagator(Propagator())
					otel.SetTracerProvider(provider)
				}
				module.Provide(sc, Capability, &Tracing{Provider: provider, TrustIncoming: s.Otel.TrustsIncoming()})
				if logger, ok := module.Lookup(sc, module.Logger); ok {
					logger.InfoContext(ctx, "[TracingModule] Tracing setup completed", "tracing", s)
				}
				return module.Hooks{Stop: provider.Shutdown}, nil
			},
		}, nil
	}}
}
