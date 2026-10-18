// Package codec implements multi-format encode/decode (JSON, CBOR and XML)
// from a single struct definition, as described in encoder.md.
//
// Business code does not import encoding/json, encoding/xml or the CBOR lib:
// it always uses a Registry (EncodeAs/DecodeAs/EncodeTo/DecodeFrom).
//
// Struct tags:
//
//	type User struct {
//		XMLName xml.Name `json:"-" xml:"user"`      // XML root element name
//		ID      int64    `json:"id" xml:"id,attr"`  // attribute in XML
//		Name    string   `json:"name" codec:"required"`
//		Email   string   `json:"email,omitempty"`   // cbor and xml inherit from json
//	}
//
// When the format's tag (`cbor`, `xml`) is missing, the `json` tag applies;
// when that is missing too, the field name. omitempty must be the same in all
// tags present on the field (otherwise ErrInvalidTags).
package codec

import "io"

// Content types of the built-in codecs. The short form ("json", "cbor",
// "xml") is also accepted by any function that takes a format.
const (
	JSON = "application/json"
	CBOR = "application/cbor"
	XML  = "application/xml"
)

// Codec is the interface implemented by each format.
type Codec interface {
	// Encode serializes v in the codec's format.
	Encode(v any) ([]byte, error)
	// Decode deserializes data into v, which must be a non-nil pointer.
	Decode(data []byte, v any) error
	// ContentType identifies the format in the Registry (e.g. "application/json").
	ContentType() string
}

// StreamCodec is implemented by codecs that can read/write directly from
// io.Reader/io.Writer. The Registry uses this interface when available and
// otherwise falls back to Encode/Decode with an in-memory buffer.
type StreamCodec interface {
	Codec
	EncodeTo(w io.Writer, v any) error
	DecodeFrom(r io.Reader, v any) error
}

// Validator may be implemented by the target type; the Registry calls
// Validate right after a successful decode and wraps the error in
// ValidationError.
type Validator interface {
	Validate() error
}

// Options configures the built-in codecs. The zero value is the safe one:
// permissive mode for unknown fields and DTD forbidden in XML.
type Options struct {
	// StrictUnknownFields rejects payload fields that do not exist in the struct.
	StrictUnknownFields bool
	// AllowDTD allows <!DOCTYPE ...> declarations in XML. Even when allowed,
	// external entities are never expanded (the parser does not resolve them).
	AllowDTD bool
}
