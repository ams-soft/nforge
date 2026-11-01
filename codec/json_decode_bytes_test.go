package codec

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

type numPlain struct {
	ID    int64             `json:"id"`
	Name  string            `json:"name"`
	Tags  []string          `json:"tags"`
	Inner struct{ N int64 } `json:"inner"`
	At    time.Time         `json:"at"`
	Raw   json.RawMessage   `json:"raw"`
}

type numAny struct {
	ID   int64          `json:"id"`
	Meta map[string]any `json:"meta"`
}

type numDeep struct {
	Items []struct {
		Extra *[]any `json:"extra"`
	} `json:"items"`
}

type numRecursive struct {
	Next *numRecursive `json:"next"`
	V    int           `json:"v"`
}

func TestJSONNeedsNumber(t *testing.T) {
	cases := []struct {
		v    any
		want bool
	}{
		{numPlain{}, false},
		{&numPlain{}, false},
		{[]numPlain{}, false},
		{map[string]numPlain{}, false},
		{numRecursive{}, false},
		{numAny{}, true},
		{numDeep{}, true},
		{new(any), true},
		{map[string]any{}, true},
		{[]any{}, true},
		{json.RawMessage{}, false},
	}
	for _, c := range cases {
		if got := jsonNeedsNumber(reflect.TypeOf(c.v)); got != c.want {
			t.Errorf("jsonNeedsNumber(%T) = %v, want %v", c.v, got, c.want)
		}
	}
}

// The Unmarshal fast path must report exactly what the Decoder path reports.
func TestJSONDecodeBytesMatchesDecoder(t *testing.T) {
	payloads := []string{
		``, `   `, `{`, `{"id":1`, `{"id":1,}`, `{"id":1} x`, `{"id":1}  x`, `{"id":1}}`,
		`{"id":1}{"id":2}`, `[]`, `null`, `{"id":"7"}`, `{"id":1.5}`, `{"name":5}`,
		`{"inner":{"N":"x"}}`, `{"at":"not a time"}`, `{"id":9223372036854775807,"name":"é","tags":["a","b"]}`,
		"{\"name\":\"\xff\"}", `{"id":1,"id":2}`, `{"ID":3}`,
	}
	for _, p := range payloads {
		var viaBytes, viaDecoder numPlain
		tp := reflect.TypeOf(viaBytes)
		e1 := jsonDecodeBytes([]byte(p), &viaBytes, tp)
		e2 := jsonDecodeAll(bytes.NewReader([]byte(p)), &viaDecoder)
		if (e1 == nil) != (e2 == nil) || (e1 != nil && e1.Error() != e2.Error()) {
			t.Errorf("%q: fast path error %v, decoder path error %v", p, e1, e2)
			continue
		}
		if e1 == nil && !reflect.DeepEqual(viaBytes, viaDecoder) {
			t.Errorf("%q: fast path %+v, decoder path %+v", p, viaBytes, viaDecoder)
		}
	}
}

func TestJSONDecodeFromKeepsPrecision(t *testing.T) {
	c := NewJSON(Options{})
	var v numPlain
	if err := c.DecodeFrom(strings.NewReader(`{"id":9007199254740993,"name":"x"}`), &v); err != nil || v.ID != 9007199254740993 {
		t.Fatalf("DecodeFrom: %v %+v", err, v)
	}
	if err := c.DecodeFrom(strings.NewReader(`{"id":1} x`), &v); err == nil {
		t.Fatal("DecodeFrom accepted data after the value")
	}
	var a numAny
	if err := c.DecodeFrom(strings.NewReader(`{"id":1,"meta":{"n":9007199254740993}}`), &a); err != nil {
		t.Fatal(err)
	}
	if n, ok := a.Meta["n"].(json.Number); !ok || n.String() != "9007199254740993" {
		t.Fatalf("number inside any lost its precision: %#v", a.Meta["n"])
	}
}
