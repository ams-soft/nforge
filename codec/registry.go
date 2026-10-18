package codec

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
)

// Config mirrors the `codec` section of config.yaml.
type Config struct {
	// Formats lists the active formats ("json", "cbor", "xml" or content
	// types of custom codecs). Empty = json only.
	Formats []string
	// Default is the format used when the caller passes format "".
	Default string
	// StrictUnknownFields rejects unknown fields in the payload.
	StrictUnknownFields bool
	// MaxPayloadBytes limits the size of the incoming payload. Zero uses
	// DefaultMaxPayloadBytes.
	MaxPayloadBytes int64
	// AllowDTD allows <!DOCTYPE> in XML (config: codec.xml.disallow_dtd = false).
	AllowDTD bool
	// Validate, if set, runs after every successful decode (e.g. a
	// go-playground/validator). It runs after Validator.Validate.
	Validate func(v any) error
}

// Registry is the single facade used by business code: it resolves the codec
// by format and applies the payload limit and post-decode validation.
type Registry struct {
	mu         sync.RWMutex
	codecs     map[string]Codec
	order      []string
	def        string
	maxPayload int64
	validate   func(any) error
}

// DefaultMaxPayloadBytes is the payload limit when none is configured.
const DefaultMaxPayloadBytes = 5 << 20 // 5 MiB

var aliases = map[string]string{
	"json":     JSON,
	"cbor":     CBOR,
	"xml":      XML,
	"text/xml": XML,
}

// New creates a Registry with the built-in codecs listed in cfg.Formats plus
// the given extra codecs. Every active format must have a codec: a format
// without a codec is a configuration error, not "half supported".
func New(cfg Config, extra ...Codec) (*Registry, error) {
	if cfg.MaxPayloadBytes < 0 {
		return nil, errors.New("codec: MaxPayloadBytes must not be negative")
	}
	if cfg.MaxPayloadBytes == 0 {
		cfg.MaxPayloadBytes = DefaultMaxPayloadBytes
	}
	formats := cfg.Formats
	if len(formats) == 0 {
		formats = []string{"json"}
	}

	opts := Options{StrictUnknownFields: cfg.StrictUnknownFields, AllowDTD: cfg.AllowDTD}
	available := map[string]Codec{
		JSON: NewJSON(opts),
		CBOR: NewCBOR(opts),
		XML:  NewXML(opts),
	}
	for _, c := range extra {
		available[normalize(c.ContentType())] = c
	}

	r := &Registry{
		codecs:     map[string]Codec{},
		maxPayload: cfg.MaxPayloadBytes,
		validate:   cfg.Validate,
	}
	for _, f := range formats {
		key := normalize(f)
		c, ok := available[key]
		if !ok {
			return nil, fmt.Errorf("codec: format %q enabled but no Codec registered: %w", f, ErrUnsupportedFormat)
		}
		r.Register(c)
	}

	def := cfg.Default
	if def == "" {
		def = "json"
	}
	r.def = normalize(def)
	if _, ok := r.codecs[r.def]; !ok {
		return nil, fmt.Errorf("codec: default format %q is not enabled: %w", def, ErrUnsupportedFormat)
	}
	return r, nil
}

// Register adds (or replaces) a codec, identified by its ContentType.
// It must be called at initialization, not per request.
func (r *Registry) Register(c Codec) {
	key := normalize(c.ContentType())
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.codecs[key]; !ok {
		r.order = append(r.order, key)
	}
	r.codecs[key] = c
}

// Formats returns the registered content types, in registration order. It is
// the source for advertising supported formats (e.g. OpenAPI).
func (r *Registry) Formats() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]string(nil), r.order...)
}

// DefaultFormat returns the content type used when the format is "".
func (r *Registry) DefaultFormat() string { return r.def }

// Lookup resolves the codec for a format ("json", "application/json",
// "application/json; charset=utf-8"...). Format "" uses the default.
func (r *Registry) Lookup(format string) (Codec, error) {
	key := r.def
	if strings.TrimSpace(format) != "" {
		key = normalize(format)
	}
	r.mu.RLock()
	c, ok := r.codecs[key]
	r.mu.RUnlock()
	if !ok {
		return nil, unsupportedFormat(format)
	}
	return c, nil
}

func (r *Registry) EncodeAs(format string, v any) ([]byte, error) {
	c, err := r.Lookup(format)
	if err != nil {
		return nil, err
	}
	return c.Encode(v)
}

func (r *Registry) DecodeAs(format string, data []byte, v any) error {
	c, err := r.Lookup(format)
	if err != nil {
		return err
	}
	if int64(len(data)) > r.maxPayload {
		return r.tooLarge()
	}
	if err := c.Decode(data, v); err != nil {
		return err
	}
	return r.runValidation(v)
}

// EncodeTo writes v directly to w (streaming when the codec supports it).
func (r *Registry) EncodeTo(format string, w io.Writer, v any) error {
	c, err := r.Lookup(format)
	if err != nil {
		return err
	}
	if sc, ok := c.(StreamCodec); ok {
		return sc.EncodeTo(w, v)
	}
	data, err := c.Encode(v)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

// DecodeFrom reads at most MaxPayloadBytes from rd; beyond that it returns
// ErrPayloadTooLarge without reading the rest of the body.
func (r *Registry) DecodeFrom(format string, rd io.Reader, v any) error {
	c, err := r.Lookup(format)
	if err != nil {
		return err
	}
	if err := checkTarget(v); err != nil {
		return err
	}
	lr := &limitReader{r: rd, n: r.maxPayload, max: r.maxPayload}
	if sc, ok := c.(StreamCodec); ok {
		err = sc.DecodeFrom(lr, v)
	} else {
		var buf bytes.Buffer
		if _, err = buf.ReadFrom(lr); err == nil {
			err = c.Decode(buf.Bytes(), v)
		}
	}
	if err != nil {
		return err
	}
	return r.runValidation(v)
}

func (r *Registry) runValidation(v any) error {
	if val, ok := v.(Validator); ok {
		if err := val.Validate(); err != nil {
			return &ValidationError{Err: err}
		}
	}
	if r.validate != nil {
		if err := r.validate(v); err != nil {
			return &ValidationError{Err: err}
		}
	}
	return nil
}

func (r *Registry) tooLarge() error {
	return fmt.Errorf("%w (max %d bytes)", ErrPayloadTooLarge, r.maxPayload)
}

// normalize reduces a format to the registry key: lowercase, without
// parameters and with the short form translated ("json" -> "application/json").
func normalize(format string) string {
	f, _, _ := strings.Cut(format, ";")
	f = strings.ToLower(strings.TrimSpace(f))
	if full, ok := aliases[f]; ok {
		return full
	}
	if !strings.Contains(f, "/") {
		return "application/" + f
	}
	return f
}

// limitReader returns ErrPayloadTooLarge when going past max bytes. Unlike
// io.LimitReader, it distinguishes "ended at the limit" from "had more".
type limitReader struct {
	r   io.Reader
	n   int64
	max int64
}

func (l *limitReader) Read(p []byte) (int, error) {
	if l.n <= 0 {
		var probe [1]byte
		n, err := l.r.Read(probe[:])
		if n > 0 {
			return 0, fmt.Errorf("%w (max %d bytes)", ErrPayloadTooLarge, l.max)
		}
		return 0, err
	}
	if int64(len(p)) > l.n {
		p = p[:l.n]
	}
	n, err := l.r.Read(p)
	l.n -= int64(n)
	return n, err
}

// ---------------------------------------------------------------- default

var defaultRegistry atomic.Pointer[Registry]

func init() {
	r, err := New(Config{Formats: []string{"json", "cbor", "xml"}})
	if err != nil {
		panic(err)
	}
	defaultRegistry.Store(r)
}

// Default returns the process Registry. Until SetDefault is called during
// bootstrap, it is a registry with json, cbor and xml, permissive mode and
// limit DefaultMaxPayloadBytes (5 MiB).
func Default() *Registry { return defaultRegistry.Load() }

// SetDefault replaces the process Registry (done once during bootstrap).
func SetDefault(r *Registry) {
	if r == nil {
		panic("codec: SetDefault(nil)")
	}
	defaultRegistry.Store(r)
}
