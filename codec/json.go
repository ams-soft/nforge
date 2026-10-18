package codec

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
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
	return jsonDecodeAll(bytes.NewReader(data), v)
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
		return &TypeError{Format: "json", Field: typ.Field, Expected: typ.Type.String(), Got: "value " + typ.Value}
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
