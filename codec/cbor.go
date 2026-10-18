package codec

import (
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"

	"github.com/fxamacker/cbor/v2"
)

const cborPkgPath = "github.com/fxamacker/cbor/v2"

type cborCodec struct {
	strict bool
	enc    cbor.EncMode
	dec    cbor.DecMode
}

// NewCBOR creates the CBOR codec. time.Time is sent as RFC 3339 text with
// nanoseconds (the lib's default, Unix seconds, would lose precision on a
// round-trip) and duplicate map keys are rejected.
func NewCBOR(opts Options) StreamCodec {
	enc, err := cbor.EncOptions{Time: cbor.TimeRFC3339Nano}.EncMode()
	if err != nil {
		panic(err) // fixed options: only fails if the lib itself changes
	}
	dec, err := cbor.DecOptions{DupMapKey: cbor.DupMapKeyEnforcedAPF}.DecMode()
	if err != nil {
		panic(err)
	}
	return &cborCodec{strict: opts.StrictUnknownFields, enc: enc, dec: dec}
}

func (c *cborCodec) ContentType() string { return CBOR }

func (c *cborCodec) Encode(v any) ([]byte, error) {
	if err := analyze(reflect.TypeOf(v)); err != nil {
		return nil, err
	}
	b, err := c.enc.Marshal(v)
	if err != nil {
		return nil, cborEncodeErr(err)
	}
	return b, nil
}

func (c *cborCodec) EncodeTo(w io.Writer, v any) error {
	if err := analyze(reflect.TypeOf(v)); err != nil {
		return err
	}
	if err := c.enc.NewEncoder(w).Encode(v); err != nil {
		return cborEncodeErr(err)
	}
	return nil
}

func (c *cborCodec) Decode(data []byte, v any) error {
	if err := checkTarget(v); err != nil {
		return err
	}
	t := reflect.TypeOf(v).Elem()
	if err := analyze(t); err != nil {
		return err
	}
	if len(data) == 0 {
		return &SyntaxError{Format: "cbor", Offset: 0, Msg: "empty payload"}
	}
	if c.strict || hasRequired(t) {
		var tree any
		if err := c.dec.Unmarshal(data, &tree); err != nil {
			return cborDecodeErr(err)
		}
		if err := checkPresence(fmtCBOR, t, tree, "", c.strict); err != nil {
			return err
		}
	}
	if err := c.dec.Unmarshal(data, v); err != nil {
		return cborDecodeErr(err)
	}
	return nil
}

func (c *cborCodec) DecodeFrom(r io.Reader, v any) error {
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
	d := c.dec.NewDecoder(r)
	if err := d.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return &SyntaxError{Format: "cbor", Offset: 0, Msg: "empty payload"}
		}
		return cborDecodeErr(err)
	}
	// Same behavior as Unmarshal: nothing may be left after the item.
	var probe [1]byte
	if n, err := io.MultiReader(d.Buffered(), r).Read(probe[:]); n > 0 {
		return &SyntaxError{Format: "cbor", Offset: int64(d.NumBytesRead()), Msg: "data after the CBOR item"}
	} else if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func cborDecodeErr(err error) error {
	var typ *cbor.UnmarshalTypeError
	switch {
	case errors.Is(err, ErrPayloadTooLarge):
		return err
	case errors.As(err, &typ):
		// The lib reports "<struct type>.<field>"; only the field matters.
		field := typ.StructFieldName
		if i := strings.LastIndexByte(field, '.'); i >= 0 {
			field = field[i+1:]
		}
		return &TypeError{Format: "cbor", Field: field, Expected: typ.GoType, Got: "cbor " + typ.CBORType}
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return &SyntaxError{Format: "cbor", Offset: -1, Msg: "unexpected end of payload"}
	case isCBORLibError(err):
		return &SyntaxError{Format: "cbor", Offset: -1, Msg: err.Error()}
	}
	// Errors from the user's own UnmarshalCBOR/UnmarshalText: propagated.
	return fmt.Errorf("codec: cbor: %w", err)
}

func cborEncodeErr(err error) error {
	var ut *cbor.UnsupportedTypeError
	switch {
	case errors.As(err, &ut):
		return &UnsupportedTypeError{Format: "cbor", Type: ut.Type}
	case isCBORLibError(err):
		return &EncodeError{Format: "cbor", Msg: err.Error()}
	}
	return fmt.Errorf("codec: cbor: %w", err)
}

// isCBORLibError identifies errors defined by fxamacker/cbor, which are
// converted to package types instead of leaking into the public API. Some of
// the lib's errors are created with errors.New, hence the prefix fallback.
func isCBORLibError(err error) bool {
	t := reflect.TypeOf(err)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t.PkgPath() == cborPkgPath || strings.HasPrefix(err.Error(), "cbor: ")
}
