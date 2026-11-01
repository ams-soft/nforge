package codec

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sync"
)

type jsonCodec struct {
	strict bool
}

// NewJSON creates the JSON codec. Numbers in `any` fields become json.Number,
// so 64-bit integers do not lose precision (nothing goes through float64).
func NewJSON(opts Options) StreamCodec {
	return &jsonCodec{strict: opts.StrictUnknownFields}
}

func (c *jsonCodec) ContentType() string { return JSON }

func (c *jsonCodec) Encode(v any) ([]byte, error) {
	if err := analyze(reflect.TypeOf(v)); err != nil {
		return nil, err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, jsonEncodeErr(err)
	}
	return b, nil
}

func (c *jsonCodec) EncodeTo(w io.Writer, v any) error {
	if err := analyze(reflect.TypeOf(v)); err != nil {
		return err
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		return jsonEncodeErr(err)
	}
	return nil
}

func (c *jsonCodec) Decode(data []byte, v any) error {
	if err := checkTarget(v); err != nil {
		return err
	}
	t := reflect.TypeOf(v).Elem()
	if err := analyze(t); err != nil {
		return err
	}
	if c.strict || hasRequired(t) {
		if err := checkPresenceJSON(data, t, c.strict); err != nil {
			return err
		}
	}
	return jsonDecodeBytes(data, v, t)
}

// DecodeFrom decodes directly from the reader. With strict mode or required
// fields the payload must be read in full (two passes); the size limit is
// applied by the Registry before reaching this point.
func (c *jsonCodec) DecodeFrom(r io.Reader, v any) error {
	if err := checkTarget(v); err != nil {
		return err
	}
	t := reflect.TypeOf(v).Elem()
	if c.strict || hasRequired(t) {
		data, err := io.ReadAll(r)
		if err != nil {
			return err
		}
		return c.Decode(data, v)
	}
	if err := analyze(t); err != nil {
		return err
	}
	return jsonDecodeAll(r, v)
}

// jsonDecodeBytes decodes data into v. json.Unmarshal is markedly cheaper than
// a Decoder, but it cannot enable UseNumber, so it is used only when no
// interface is reachable from t (see reachesInterface); other targets keep the
// Decoder path and its int64-safe numbers.
//
// A syntax error is rerun through jsonDecodeAll so the error (empty payload,
// truncated payload, data after the value, offsets) stays exactly what the
// Decoder path reports. The rerun only happens for invalid input; a custom
// UnmarshalJSON that ran before the syntax error was found may run twice, and
// the target keeps the same partial state a failed decode always left.
func jsonDecodeBytes(data []byte, v any, t reflect.Type) error {
	if reachesInterface(t) {
		return jsonDecodeAll(bytes.NewReader(data), v)
	}
	err := json.Unmarshal(data, v)
	if err == nil {
		return nil
	}
	var syn *json.SyntaxError
	if errors.As(err, &syn) {
		return jsonDecodeAll(bytes.NewReader(data), v)
	}
	return jsonDecodeErr(err)
}

var needsNumberCache sync.Map // reflect.Type -> bool

// reachesInterface reports whether a value of type t can reach an interface
// (any, map[string]any, []any, ...). The JSON decoder then needs json.Number
// so 64-bit integers are not rounded through float64, and the response and
// cycle checks must look at the dynamic values. Types with their own
// (un)marshalers are opaque: the codec never looks inside them. The result is
// cached per type.
func reachesInterface(t reflect.Type) bool {
	if v, ok := needsNumberCache.Load(t); ok {
		return v.(bool)
	}
	r := reachesInterfaceRec(t, map[reflect.Type]bool{})
	needsNumberCache.Store(t, r)
	return r
}

func reachesInterfaceRec(t reflect.Type, seen map[reflect.Type]bool) bool {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() == reflect.Interface {
		return true
	}
	if seen[t] || isOpaque(t) {
		return false
	}
	seen[t] = true
	switch t.Kind() {
	case reflect.Slice, reflect.Array, reflect.Map:
		return reachesInterfaceRec(t.Elem(), seen)
	case reflect.Struct:
		for i := range t.NumField() {
			if reachesInterfaceRec(t.Field(i).Type, seen) {
				return true
			}
		}
	}
	return false
}

// jsonDecodeAll decodes a single value and requires nothing after it
// (same behavior as json.Unmarshal).
func jsonDecodeAll(r io.Reader, v any) error {
	d := json.NewDecoder(r)
	d.UseNumber()
	if err := d.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return &SyntaxError{Format: "json", Offset: 0, Msg: "empty payload"}
		}
		return jsonDecodeErr(err)
	}
	if _, err := d.Token(); !errors.Is(err, io.EOF) {
		if errors.Is(err, ErrPayloadTooLarge) {
			return err
		}
		return &SyntaxError{Format: "json", Offset: d.InputOffset(), Msg: "data after the JSON value"}
	}
	return nil
}

func jsonDecodeErr(err error) error {
	var (
		syn *json.SyntaxError
		typ *json.UnmarshalTypeError
	)
	switch {
	case errors.Is(err, ErrPayloadTooLarge):
		return err
	case errors.As(err, &syn):
		return &SyntaxError{Format: "json", Offset: syn.Offset, Msg: syn.Error()}
	case errors.As(err, &typ):
		return &TypeError{Format: "json", Field: typ.Field, Expected: typ.Type.String(), Wire: WireType(typ.Type), Got: "value " + typ.Value}
	case errors.Is(err, io.ErrUnexpectedEOF):
		return &SyntaxError{Format: "json", Offset: -1, Msg: "unexpected end of payload"}
	}
	// Errors from the user's own UnmarshalJSON/UnmarshalText: propagated.
	return fmt.Errorf("codec: json: %w", err)
}

func jsonEncodeErr(err error) error {
	var (
		ut *json.UnsupportedTypeError
		uv *json.UnsupportedValueError
	)
	switch {
	case errors.As(err, &ut):
		return &UnsupportedTypeError{Format: "json", Type: ut.Type}
	case errors.As(err, &uv):
		return &EncodeError{Format: "json", Msg: uv.Error()}
	}
	return fmt.Errorf("codec: json: %w", err)
}
