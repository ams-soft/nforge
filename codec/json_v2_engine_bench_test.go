//go:build goexperiment.jsonv2

package codec

// TEMPORARY investigation file (see json_v2_bench_test.go): native
// encoding/json/v2 rows. encoding/json/v2 only exists with GOEXPERIMENT=jsonv2
// in Go 1.27.3, hence the build tag.

import (
	"bytes"
	"io"
	"testing"

	jsonv2 "encoding/json/v2"
)

func init() {
	extraOps = append(extraOps, benchOps{
		name:       "jsonv2",
		marshal:    func(v any) ([]byte, error) { return jsonv2.Marshal(v) },
		unmarshal:  func(b []byte, v any) error { return jsonv2.Unmarshal(b, v) },
		encodeTo:   func(w io.Writer, v any) error { return jsonv2.MarshalWrite(w, v) },
		decodeFrom: func(r io.Reader, v any) error { return jsonv2.UnmarshalRead(r, v) },
	})
	extraStrict = append(extraStrict, strictCase{
		name:   "jsonv2/reject-unknown",
		newDst: func() any { return new(benchDoc) },
		decode: func(b []byte, v any) error { return jsonv2.Unmarshal(b, v, jsonv2.RejectUnknownMembers(true)) },
		note:   "unknown fields only; NO required support",
	})
}

func TestJSONSemanticsV2(t *testing.T) {
	for _, l := range semanticsReport(
		func(v any) ([]byte, error) { return jsonv2.Marshal(v) },
		func(b []byte, v any) error { return jsonv2.Unmarshal(b, v) },
	) {
		t.Log("jsonv2   ", l)
	}
	_ = bytes.MinRead
}
