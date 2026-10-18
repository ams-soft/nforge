// Package capture holds the ResponseWriter handed to nhttp typed-route
// handlers: it stores the status and message (via Send*) or the bytes
// written, so the route can validate them and send them in its codec.
package capture

import (
	"bytes"
	"fmt"
	stdhttp "net/http"
	"reflect"

	"github.com/ams-soft/nforge/codec"
	"github.com/ams-soft/nforge/http/internal/envelope"
)

// Writer captures a typed handler's response. Headers go straight to the
// real writer.
type Writer struct {
	stdhttp.ResponseWriter
	status   int
	message  any
	captured bool
	buf      bytes.Buffer
}

// NewWriter wraps w.
func NewWriter(w stdhttp.ResponseWriter) *Writer {
	return &Writer{ResponseWriter: w}
}

func (c *Writer) WriteHeader(status int) {
	if c.status == 0 {
		c.status = status
	}
}

func (c *Writer) Write(b []byte) (int, error) {
	if c.status == 0 {
		c.status = stdhttp.StatusOK
	}
	return c.buf.Write(b)
}

// Unwrap exposes the real writer to http.ResponseController and Find.
func (c *Writer) Unwrap() stdhttp.ResponseWriter { return c.ResponseWriter }

// Capture stores a Send* call instead of writing it.
func (c *Writer) Capture(status int, message any) {
	c.status, c.message, c.captured = status, message, true
}

// Find looks for the Writer in w, walking through writers that wrap another
// one (Unwrap), such as those from middlewares.
func Find(w stdhttp.ResponseWriter) *Writer {
	for w != nil {
		if cw, ok := w.(*Writer); ok {
			return cw
		}
		u, ok := w.(interface{ Unwrap() stdhttp.ResponseWriter })
		if !ok {
			return nil
		}
		w = u.Unwrap()
	}
	return nil
}

// Result returns the status (200 if the handler did not set one) and the final
// message. On success with a body and a declared response, the message is
// converted to the response type and validated; an error here is a handler
// bug (500).
func (c *Writer) Result(format string, resType reflect.Type) (int, any, error) {
	status := c.status
	if status == 0 {
		status = stdhttp.StatusOK
	}
	success := status < stdhttp.StatusBadRequest && envelope.BodyAllowed(status)
	message := c.message
	if !c.captured && c.buf.Len() > 0 {
		// the handler wrote bytes directly: decode them in the route's codec
		var decoded any
		if success && resType != nil {
			ptr := reflect.New(resType)
			if err := codec.Default().DecodeAs(format, c.buf.Bytes(), ptr.Interface()); err != nil {
				return 0, nil, err
			}
			decoded = ptr.Interface()
		} else if err := codec.Default().DecodeAs(format, c.buf.Bytes(), &decoded); err != nil {
			decoded = c.buf.String()
		}
		message = decoded
	}
	if !success || resType == nil {
		return status, message, nil
	}
	v, err := conform(format, message, resType)
	return status, v, err
}

// conform converts message to resType (directly, if it already is that type
// or a pointer to it; otherwise by encode/decode in the route's codec, which
// checks the types) and runs the required fields and validation.
func conform(format string, message any, resType reflect.Type) (any, error) {
	if message == nil {
		return nil, fmt.Errorf("nhttp: empty response, expected %s", resType)
	}
	registry := codec.Default()
	mv := reflect.ValueOf(message)
	var ptr reflect.Value
	switch {
	case mv.Type() == resType:
		ptr = reflect.New(resType)
		ptr.Elem().Set(mv)
	case mv.Type() == reflect.PointerTo(resType) && !mv.IsNil():
		ptr = mv
	default:
		data, err := registry.EncodeAs(format, message)
		if err != nil {
			return nil, err
		}
		ptr = reflect.New(resType)
		if err := registry.DecodeAs(format, data, ptr.Interface()); err != nil {
			return nil, fmt.Errorf("nhttp: response does not match %s: %w", resType, err)
		}
	}
	if err := codec.CheckRequired(ptr.Interface()); err != nil {
		return nil, err
	}
	if err := registry.Validate(ptr.Interface()); err != nil {
		return nil, err
	}
	return ptr.Elem().Interface(), nil
}
