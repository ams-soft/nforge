package codec

import (
	"errors"
	"fmt"
	"reflect"
)

// Package sentinels. Every error returned by the codec can be identified
// with errors.Is against one of them; the concrete types below carry the
// details (field, expected type, etc.) and can be extracted with errors.As.
var (
	ErrUnsupportedFormat = errors.New("codec: unsupported format")
	ErrInvalidTarget     = errors.New("codec: invalid decode target")
	ErrPayloadTooLarge   = errors.New("codec: payload exceeds the configured limit")
	ErrDTDNotAllowed     = errors.New("codec: DTD/entities are not allowed in XML")
	ErrSyntax            = errors.New("codec: malformed payload")
	ErrUnknownField      = errors.New("codec: unknown field")
	ErrMissingField      = errors.New("codec: missing required field")
	ErrTypeMismatch      = errors.New("codec: type mismatch")
	ErrNamespace         = errors.New("codec: unexpected XML namespace")
	ErrValidation        = errors.New("codec: validation failed")
	ErrInvalidTags       = errors.New("codec: invalid struct tags")
	ErrUnsupportedType   = errors.New("codec: type not supported by the format")
	ErrEncode            = errors.New("codec: encode failed")
)

// EncodeError indicates the format library failed to serialize (e.g. NaN
// in JSON). As with SyntaxError, the lib's error is not exposed via Unwrap.
type EncodeError struct {
	Format string
	Msg    string
}

func (e *EncodeError) Error() string {
	return fmt.Sprintf("codec: failed to encode %s: %s", e.Format, e.Msg)
}

func (e *EncodeError) Is(target error) bool { return target == ErrEncode }

// SyntaxError indicates a syntactically invalid payload for the format. The
// underlying library's message is kept as text, but the original error is not
// exposed via Unwrap so third-party types do not leak into the public API.
type SyntaxError struct {
	Format string
	Offset int64 // -1 when the format does not report the position
	Msg    string
}

func (e *SyntaxError) Error() string {
	if e.Offset >= 0 {
		return fmt.Sprintf("codec: malformed %s (offset %d): %s", e.Format, e.Offset, e.Msg)
	}
	return fmt.Sprintf("codec: malformed %s: %s", e.Format, e.Msg)
}

func (e *SyntaxError) Is(target error) bool { return target == ErrSyntax }

// UnknownFieldError is returned in strict mode when the payload contains a
// field that does not exist in the target struct.
type UnknownFieldError struct {
	Format string
	Field  string // field path, e.g. "user.address.zip"
}

func (e *UnknownFieldError) Error() string {
	return fmt.Sprintf("codec: %s: unknown field %q", e.Format, e.Field)
}

func (e *UnknownFieldError) Is(target error) bool { return target == ErrUnknownField }

// MissingFieldError indicates that a field marked with `codec:"required"` was
// not in the payload. It is a decode error, distinct from ErrValidation.
type MissingFieldError struct {
	Format string
	Field  string
}

func (e *MissingFieldError) Error() string {
	return fmt.Sprintf("codec: %s: missing required field %q", e.Format, e.Field)
}

func (e *MissingFieldError) Is(target error) bool { return target == ErrMissingField }

// TypeError indicates that the payload value is not compatible with the Go
// type of the target field.
type TypeError struct {
	Format   string
	Field    string
	Expected string // expected Go type
	Got      string // description of the received value
}

func (e *TypeError) Error() string {
	field := e.Field
	if field == "" {
		field = "(root)"
	}
	return fmt.Sprintf("codec: %s: field %q expects %s, got %s", e.Format, field, e.Expected, e.Got)
}

func (e *TypeError) Is(target error) bool { return target == ErrTypeMismatch }

// NamespaceError indicates an XML element/attribute in a namespace different
// from the one declared in the struct tags.
type NamespaceError struct {
	Field    string
	Expected string
	Got      string
}

func (e *NamespaceError) Error() string {
	return fmt.Sprintf("codec: xml: %q in namespace %q, expected %q", e.Field, e.Got, e.Expected)
}

func (e *NamespaceError) Is(target error) bool { return target == ErrNamespace }

// InvalidTargetError is returned when Decode receives something that is not a
// non-nil pointer.
type InvalidTargetError struct {
	Type reflect.Type
}

func (e *InvalidTargetError) Error() string {
	if e.Type == nil {
		return "codec: Decode requires a non-nil pointer, got nil"
	}
	if e.Type.Kind() != reflect.Pointer {
		return "codec: Decode requires a non-nil pointer, got " + e.Type.String()
	}
	return "codec: Decode requires a non-nil pointer, got " + e.Type.String() + " nil"
}

func (e *InvalidTargetError) Is(target error) bool { return target == ErrInvalidTarget }

// ValidationError wraps the error returned by post-decode validation. The value
// was decoded successfully but did not pass the business rules.
type ValidationError struct {
	Err error
}

func (e *ValidationError) Error() string { return "codec: validation failed: " + e.Err.Error() }

func (e *ValidationError) Unwrap() error { return e.Err }

func (e *ValidationError) Is(target error) bool { return target == ErrValidation }

// TagError indicates struct tags that are inconsistent across formats (e.g.
// omitempty in one format and not in another) or unsupported options.
type TagError struct {
	Type  reflect.Type
	Field string
	Msg   string
}

func (e *TagError) Error() string {
	return fmt.Sprintf("codec: %s.%s: %s", e.Type, e.Field, e.Msg)
}

func (e *TagError) Is(target error) bool { return target == ErrInvalidTags }

// UnsupportedTypeError indicates a Go type the format cannot represent
// (e.g. chan, func, complex).
type UnsupportedTypeError struct {
	Format string
	Type   reflect.Type
}

func (e *UnsupportedTypeError) Error() string {
	return fmt.Sprintf("codec: %s does not support type %s", e.Format, e.Type)
}

func (e *UnsupportedTypeError) Is(target error) bool { return target == ErrUnsupportedType }

func unsupportedFormat(format string) error {
	return fmt.Errorf("%w: %q", ErrUnsupportedFormat, format)
}

// checkTarget ensures v is a non-nil pointer before any decode.
func checkTarget(v any) error {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return &InvalidTargetError{Type: reflect.TypeOf(v)}
	}
	return nil
}
