// Package envelope holds the standard response envelope of nhttp, shared by
// the response helpers, the typed-route capture and the OpenAPI generator.
package envelope

import (
	"encoding/xml"
	stdhttp "net/http"
	"strconv"
	"strings"
)

// Response is the envelope of every response sent by nhttp. On success
// (status < 400) it carries Data; on error, Error.
type Response struct {
	XMLName xml.Name `json:"-" cbor:"-" xml:"response"`
	Success bool     `json:"success" xml:"success" cbor:"success"`
	Status  int      `json:"status" xml:"status" cbor:"status"`
	Data    any      `json:"data,omitempty" xml:"data,omitempty" cbor:"data,omitempty"`
	Error   *Error   `json:"error,omitempty" xml:"error,omitempty" cbor:"error,omitempty"`
}

// Error is the error part of the envelope: Title describes the status,
// Detail is the message and Instance the request path.
type Error struct {
	Title    string `json:"title" xml:"title" cbor:"title"`
	Detail   any    `json:"detail" xml:"detail" cbor:"detail"`
	Instance string `json:"instance" xml:"instance" cbor:"instance"`
}

// New builds the envelope for status and message.
func New(r *stdhttp.Request, status int, message any) Response {
	if status < stdhttp.StatusBadRequest {
		return Response{Success: true, Status: status, Data: message}
	}
	resp := Response{Status: status, Error: &Error{Title: Title(status), Detail: message}}
	if r != nil {
		resp.Error.Instance = r.URL.Path
	}
	return resp
}

// Title describes the error status, e.g. 404 -> "404 ERROR NOT FOUND".
func Title(status int) string {
	text := stdhttp.StatusText(status)
	if text == "" {
		text = "Unknown"
	}
	return strconv.Itoa(status) + " ERROR " + strings.ToUpper(text)
}

// BodyAllowed follows RFC 9110: 1xx, 204 and 304 have no body.
func BodyAllowed(status int) bool {
	return status >= 200 && status != stdhttp.StatusNoContent && status != stdhttp.StatusNotModified
}
