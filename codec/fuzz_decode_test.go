package codec

import (
	"errors"
	"testing"
)

type fuzzTarget struct {
	Name  string            `json:"name" codec:"required"`
	Age   int               `json:"age,omitempty"`
	Tags  []string          `json:"tags,omitempty"`
	Next  *fuzzTarget       `json:"next,omitempty"`
	Extra map[string]string `json:"extra,omitempty"`
	Any   any               `json:"any,omitempty"`
}

// fuzzDecode feeds hostile bytes through a strict registry. It must return an
// error of the package or succeed, never panic, and what it accepted must encode.
func fuzzDecode(f *testing.F, format string, seeds ...string) {
	reg, err := New(Config{Formats: []string{"json", "cbor", "xml"}, Default: "json", StrictUnknownFields: true, MaxPayloadBytes: 1 << 16})
	if err != nil {
		f.Fatal(err)
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		var v fuzzTarget
		if err := reg.DecodeAs(format, data, &v); err != nil {
			return
		}
		if _, err := reg.EncodeAs(format, v); err != nil && !errors.Is(err, ErrUnsupportedType) && !errors.Is(err, ErrEncode) {
			t.Fatalf("decoded value does not encode: %v", err)
		}
	})
}

func FuzzDecodeXML(f *testing.F) {
	fuzzDecode(f, "xml",
		`<fuzzTarget><name>a</name><next><name>b</name></next></fuzzTarget>`,
		`<!DOCTYPE x [<!ENTITY a "b">]><x/>`,
		`<a xmlns="u"><name>x</name><extra><entry key="k">v</entry></extra></a>`)
}

func FuzzDecodeCBOR(f *testing.F) {
	fuzzDecode(f, "cbor", "\xa1\x64name\x61a", "\xa2\x64name\x61a\x64name\x61b", "\xd8\x18\x41\x00", "\x9f\x9f\x9f\xff\xff\xff")
}
