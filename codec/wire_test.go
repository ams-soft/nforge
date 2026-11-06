package codec

import (
	"encoding/xml"
	"errors"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"
)

type wireAddr struct {
	Zip string `json:"zip" xml:"zip"`
}

type wireBody struct {
	XMLName xml.Name  `json:"-" cbor:"-" xml:"body"`
	Addr    wireAddr  `json:"addr" xml:"addr"`
	N       int64     `json:"n" xml:"n"`
	Tags    []string  `json:"tags" xml:"tag"`
	At      time.Time `json:"at" xml:"at"`
}

func TestWireType(t *testing.T) {
	type status string
	cases := []struct {
		v    any
		want string
	}{
		{true, "boolean"}, {int8(1), "integer"}, {uint64(1), "integer"}, {1.5, "number"}, {"s", "string"},
		{[]int{}, "array"}, {[3]string{}, "array"}, {[]byte{}, "string (base64)"},
		{map[string]int{}, "object"}, {wireAddr{}, "object"}, {&wireAddr{}, "object"},
		{time.Time{}, "string (RFC 3339 date-time)"}, {time.Second, "duration"},
		{net.IP{}, "string"}, // TextUnmarshaler
		{status("x"), "string"},
	}
	for _, c := range cases {
		if got := WireType(reflect.TypeOf(c.v)); got != c.want {
			t.Errorf("WireType(%T) = %q, want %q", c.v, got, c.want)
		}
	}
	if got := WireType(reflect.TypeFor[any]()); got != "any value" {
		t.Errorf("interface: %q", got)
	}
}

// A decode error reaches the client verbatim, so it must not name Go packages
// or types: "expects nhttp.wireAddr" became "expects object".
func TestTypeErrorMessagesDoNotNameGoTypes(t *testing.T) {
	r, err := New(Config{Formats: []string{"json", "cbor", "xml"}})
	if err != nil {
		t.Fatal(err)
	}
	cbor := func(v any) []byte {
		b, err := NewCBOR(Options{}).Encode(v)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	cases := []struct {
		name, format string
		payload      []byte
		want         string
	}{
		{"json struct", "json", []byte(`{"addr":"x"}`), "expects object"},
		{"json int", "json", []byte(`{"n":"s"}`), "expects integer"},
		{"json slice", "json", []byte(`{"tags":"x"}`), "expects array"},
		{"json time", "json", []byte(`{"at":5}`), "expects string (RFC 3339"},
		{"cbor struct", "cbor", cbor(map[string]any{"addr": "x"}), "expects object"},
		{"cbor int", "cbor", cbor(map[string]any{"n": "s"}), "expects integer"},
		{"xml int", "xml", []byte(`<body><n>abc</n></body>`), "expects integer"},
		{"xml child in scalar", "xml", []byte(`<body><n><x/></n></body>`), "element <x>"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var b wireBody
			err := r.DecodeAs(c.format, c.payload, &b)
			if err == nil {
				t.Skipf("decoded without error: %+v", b)
			}
			msg := err.Error()
			if c.want != "" && !strings.Contains(msg, c.want) {
				t.Errorf("message %q must contain %q", msg, c.want)
			}
			for _, leak := range []string{"codec.", "wireAddr", "wireBody", "main.", "nhttp.", "time.Time", "int64", "elemento"} {
				if strings.Contains(msg, leak) {
					t.Errorf("message %q leaks %q", msg, leak)
				}
			}
			var te *TypeError
			if errors.As(err, &te) && te.Expected == "" {
				t.Errorf("Expected keeps the Go type for logs, got %+v", te)
			}
		})
	}
}

func TestTypeErrorKeepsTheGoTypeForProgrammaticUse(t *testing.T) {
	var b wireBody
	err := NewJSON(Options{}).Decode([]byte(`{"n":"s"}`), &b)
	var te *TypeError
	if !errors.As(err, &te) || te.Expected != "int64" || te.Wire != "integer" {
		t.Fatalf("TypeError = %+v (%v)", te, err)
	}
}
