package codec

import (
	"context"
	"fmt"

	"github.com/ams-soft/nforge/module"
)

// Capability is the application's Registry, published by Module. Modules that
// encode or decode declare it instead of reading the process default.
var Capability = module.NewCapability[*Registry]("codec")

// MaxPayloadCeiling is the largest accepted codec.max_payload_bytes. The limit
// exists to protect the process, so a value that no longer does is refused.
const MaxPayloadCeiling = 256 * MB

// Settings is the codec configuration section.
type Settings struct {
	Formats             FormatSettings `yaml:"formats"`
	Default             string         `yaml:"default"`               // default: "json"; must be enabled
	StrictUnknownFields bool           `yaml:"strict_unknown_fields"` // default: false
	MaxPayloadBytes     ByteSize       `yaml:"max_payload_bytes"`     // e.g. 5mb, 512kb, 1048576; default: 5mb; at most 256mb
	XML                 XMLSettings    `yaml:"xml"`
}

// FormatSettings lists the enabled formats.
type FormatSettings struct {
	Enabled []string `yaml:"enabled"` // json | cbor | xml; default: [json]
}

// XMLSettings configures the XML codec.
type XMLSettings struct {
	DisallowDTD *bool `yaml:"disallow_dtd"` // default: true
}

// ApplyDefaults fills the unset fields with their documented defaults.
func (s *Settings) ApplyDefaults() {
	if len(s.Formats.Enabled) == 0 {
		s.Formats.Enabled = []string{"json"}
	}
	if s.Default == "" {
		s.Default = "json"
	}
	if s.MaxPayloadBytes == 0 {
		s.MaxPayloadBytes = DefaultMaxPayloadBytes
	}
	if s.XML.DisallowDTD == nil {
		disallow := true
		s.XML.DisallowDTD = &disallow
	}
}

// Config returns the Registry configuration of the settings.
func (s Settings) Config() Config {
	return Config{
		Formats:             s.Formats.Enabled,
		Default:             s.Default,
		StrictUnknownFields: s.StrictUnknownFields,
		MaxPayloadBytes:     int64(s.MaxPayloadBytes),
		AllowDTD:            s.XML.DisallowDTD != nil && !*s.XML.DisallowDTD,
	}
}

// Enables reports whether format is one of the enabled formats.
func (s Settings) Enables(format string) bool {
	for _, f := range s.Formats.Enabled {
		if normalize(f) == normalize(format) {
			return true
		}
	}
	return false
}

// ModuleOption configures Module.
type ModuleOption func(*moduleOptions)

type moduleOptions struct {
	extra    []Codec
	validate func(any) error
}

// WithCodecs registers custom codecs, enabled through codec.formats.enabled
// by their content type.
func WithCodecs(c ...Codec) ModuleOption {
	return func(o *moduleOptions) { o.extra = append(o.extra, c...) }
}

// WithValidator runs fn after every successful decode, after Validator.
func WithValidator(fn func(any) error) ModuleOption {
	return func(o *moduleOptions) { o.validate = fn }
}

// Module is the codec module: it owns the codec section and publishes the
// application's Registry under Capability. With app.WithProcessGlobals the
// Registry is also installed as the process default (SetDefault).
func Module(opts ...ModuleOption) module.Descriptor {
	var o moduleOptions
	for _, opt := range opts {
		opt(&o)
	}
	return module.Descriptor{ID: "codec", Section: "codec", Configure: func(src module.Source) (module.Definition, error) {
		var s Settings
		if err := src.Decode(&s); err != nil {
			return module.Definition{}, err
		}
		s.ApplyDefaults()
		if p := s.MaxPayloadBytes; p < 0 || p > MaxPayloadCeiling {
			return module.Definition{}, fmt.Errorf("codec.max_payload_bytes must be between 0 and %d (256 MiB), got %d: every payload is held in memory while it is decoded", int64(MaxPayloadCeiling), int64(p))
		}
		cfg := s.Config()
		cfg.Validate = o.validate
		// Building the Registry is pure, so an unknown or disabled default
		// format fails with the other configuration errors.
		registry, err := New(cfg, o.extra...)
		if err != nil {
			return module.Definition{}, err
		}
		globals := src.ProcessGlobals()
		return module.Definition{
			Provides: []module.Handle{Capability},
			Optional: []module.Handle{module.Logger},
			Config:   s,
			Init: func(ctx context.Context, sc *module.Scope) (module.Hooks, error) {
				if globals {
					SetDefault(registry)
				}
				module.Provide(sc, Capability, registry)
				if logger, ok := module.Lookup(sc, module.Logger); ok {
					logger.InfoContext(ctx, "[CodecModule] Codec setup completed", "formats", s.Formats.Enabled)
				}
				return module.Hooks{}, nil
			},
		}, nil
	}}
}
