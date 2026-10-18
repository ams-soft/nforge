package nhttp

import (
	"errors"
	"fmt"
	"mime"
	stdhttp "net/http"
	"strings"

	"github.com/ams-soft/nforge/codec"
	"github.com/ams-soft/nforge/http/internal/capture"
	"github.com/ams-soft/nforge/http/internal/envelope"
)

// SuccessResponse is the envelope of every response sent by SendJSON,
// SendXML and SendCBOR. On success (status < 400) it carries Data; on error,
// Error.
type SuccessResponse = envelope.Response

// ErrorResponse is the error part of SuccessResponse: Title describes the
// status, Detail is the message and Instance the request path.
type ErrorResponse = envelope.Error

// SendJSON responds in JSON (Content-Type application/json) with the
// SuccessResponse envelope: status < 400 puts message in Data; status >= 400
// puts message in Error.Detail, with Title describing the status and Instance
// holding the request path.
func SendJSON(w stdhttp.ResponseWriter, r *stdhttp.Request, status int, message any) error {
	return send(w, r, status, codec.JSON, message)
}

// SendXML is SendJSON in XML (Content-Type application/xml).
func SendXML(w stdhttp.ResponseWriter, r *stdhttp.Request, status int, message any) error {
	return send(w, r, status, codec.XML, message)
}

// SendCBOR is SendJSON in CBOR (Content-Type application/cbor).
func SendCBOR(w stdhttp.ResponseWriter, r *stdhttp.Request, status int, message any) error {
	return send(w, r, status, codec.CBOR, message)
}

// Envelope builds the standard response for status and message.
func Envelope(r *stdhttp.Request, status int, message any) SuccessResponse {
	return envelope.New(r, status, message)
}

// ErrorTitle describes the error status, e.g. 404 -> "404 ERROR NOT FOUND".
func ErrorTitle(status int) string { return envelope.Title(status) }

// ErrUnsupportedMediaType indicates the request's Content-Type is not the
// format expected by the route. ParseStatus maps it to 415.
var ErrUnsupportedMediaType = errors.New("nhttp: unsupported content-type")

// UnsupportedMediaTypeError details the received and the expected Content-Type.
type UnsupportedMediaTypeError struct {
	Expected string
	Got      string
}

func (e *UnsupportedMediaTypeError) Error() string {
	return fmt.Sprintf("nhttp: unsupported content-type %q, expected %s", e.Got, e.Expected)
}

func (e *UnsupportedMediaTypeError) Is(target error) bool { return target == ErrUnsupportedMediaType }

// ParseJSON decodes the request's JSON body into v (a pointer), honoring the
// payload limit and the strict mode of the configured codec. A Content-Type
// other than application/json (or */*+json) is rejected with
// ErrUnsupportedMediaType; a request without Content-Type is accepted.
func ParseJSON(r *stdhttp.Request, v any) error {
	return parse(r, codec.JSON, v)
}

// ParseXML decodes the request's XML body into v (a pointer), honoring the
// payload limit, strict mode and DTD blocking of the configured codec.
// Accepts application/xml, text/xml and */*+xml.
func ParseXML(r *stdhttp.Request, v any) error {
	return parse(r, codec.XML, v)
}

// ParseCBOR decodes the request's CBOR body into v (a pointer), honoring the
// payload limit and the strict mode of the configured codec.
// Accepts application/cbor and */*+cbor.
func ParseCBOR(r *stdhttp.Request, v any) error {
	return parse(r, codec.CBOR, v)
}

// ParseStatus maps an error from ParseJSON/ParseXML/ParseCBOR to the HTTP
// response status:
//
//   - 415: wrong Content-Type or format not enabled in the codec;
//   - 413: payload larger than codec.max_payload_bytes;
//   - 422: well-formed payload that failed validation (Validator);
//   - 400: any other error (syntax, unknown/missing field, type...).
func ParseStatus(err error) int {
	switch {
	case err == nil:
		return stdhttp.StatusOK
	case errors.Is(err, ErrUnsupportedMediaType), errors.Is(err, codec.ErrUnsupportedFormat):
		return stdhttp.StatusUnsupportedMediaType
	case errors.Is(err, codec.ErrPayloadTooLarge):
		return stdhttp.StatusRequestEntityTooLarge
	case errors.Is(err, codec.ErrValidation):
		return stdhttp.StatusUnprocessableEntity
	}
	return stdhttp.StatusBadRequest
}

func parse(r *stdhttp.Request, format string, v any) error {
	if err := checkContentType(r.Header.Get("Content-Type"), format); err != nil {
		return err
	}
	return codec.Default().DecodeFrom(format, r.Body, v)
}

// checkContentType accepts the format's content type, the structured suffix
// (+json, +xml, +cbor, RFC 6839) and text/xml for XML. Parameters such as
// charset are ignored.
func checkContentType(contentType, format string) error {
	if contentType == "" {
		return nil
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err == nil {
		_, subtype, _ := strings.Cut(format, "/")
		if mediaType == format || strings.HasSuffix(mediaType, "+"+subtype) ||
			(format == codec.XML && mediaType == "text/xml") {
			return nil
		}
	}
	return &UnsupportedMediaTypeError{Expected: format, Got: contentType}
}

// send delivers the response. Inside a typed route the writer is the
// capture.Writer and the response is only stored: the route serializes it,
// in its own codec.
func send(w stdhttp.ResponseWriter, r *stdhttp.Request, status int, format string, message any) error {
	if cw := capture.Find(w); cw != nil {
		cw.Capture(status, message)
		return nil
	}
	return write(w, r, status, format, message)
}

// Constant so the per-response header costs no concatenation.
const (
	jsonUTF8 = codec.JSON + "; charset=utf-8"
	xmlUTF8  = codec.XML + "; charset=utf-8"
)

// responseContentType adds the charset to the text formats; it only makes
// sense for them.
func responseContentType(contentType string) string {
	switch contentType {
	case codec.JSON:
		return jsonUTF8
	case codec.XML:
		return xmlUTF8
	}
	return contentType
}

// write serializes before writing anything: if encoding fails nothing has
// been sent and the caller can still respond with an error.
func write(w stdhttp.ResponseWriter, r *stdhttp.Request, status int, format string, message any) error {
	registry := codec.Default()
	c, err := registry.Lookup(format)
	if err != nil {
		return err
	}
	if !envelope.BodyAllowed(status) {
		w.WriteHeader(status)
		return nil
	}
	data, err := c.Encode(Envelope(r, status, message))
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", responseContentType(c.ContentType()))
	w.WriteHeader(status)
	_, err = w.Write(data) //nolint:gosec // G705: encoded envelope with an explicit non-HTML Content-Type
	return err
}
