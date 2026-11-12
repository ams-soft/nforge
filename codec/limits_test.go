package codec

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

type dupDoc struct {
	Role string `json:"role"`
	Name string `json:"name"`
}

// G14: duplicate members are refused, in every mode, at any depth, and when
// they differ only in case (encoding/json treats those as one field).
func TestDuplicateMembersAreRefused(t *testing.T) {
	for _, strict := range []bool{false, true} {
		r, _ := New(Config{Formats: []string{"json"}, StrictUnknownFields: strict})
		for name, payload := range map[string]string{
			"exact":           `{"role":"user","role":"admin"}`,
			"case folded":     `{"role":"user","ROLE":"admin"}`,
			"escaped":         `{"role":"user","role":"admin"}`,
			"nested":          `{"name":"x","role":"a","extra":{"k":1,"k":2}}`,
			"inside an array": `{"name":"x","list":[{"a":1},{"b":1,"b":2}]}`,
		} {
			var v dupDoc
			if err := r.DecodeAs("json", []byte(payload), &v); !errors.Is(err, ErrDuplicateField) {
				t.Errorf("strict=%v %s: err = %v, want ErrDuplicateField", strict, name, err)
			}
		}
		// the same names in different objects, and repeated array values, are fine
		var ok struct {
			A []map[string]int `json:"a"`
			B map[string]int   `json:"b"`
		}
		if err := r.DecodeAs("json", []byte(`{"a":[{"k":1},{"k":2}],"b":{"k":3}}`), &ok); strict && err != nil && !errors.Is(err, ErrUnknownField) {
			t.Errorf("strict=%v legitimate repetition: %v", strict, err)
		} else if !strict && err != nil {
			t.Errorf("legitimate repetition refused: %v", err)
		}
	}
}

func TestDuplicateScanIsLinearOnWideObjects(t *testing.T) {
	var b strings.Builder
	b.WriteString("{")
	for i := range 100_000 {
		fmt.Fprintf(&b, `"key%d":1,`, i)
	}
	b.WriteString(`"last":1}`)
	r, _ := New(Config{Formats: []string{"json"}, MaxElements: 1_000_000, MaxPayloadBytes: 8 << 20})
	var v map[string]int
	if err := r.DecodeAs("json", []byte(b.String()), &v); err != nil || len(v) != 100_001 {
		t.Fatalf("wide object: %v (%d keys)", err, len(v))
	}
}

// G9: the element limit bounds the work a payload inside the size limit can cause.
func TestElementLimitBoundsDecodingCost(t *testing.T) {
	r, _ := New(Config{Formats: []string{"json", "xml"}, MaxPayloadBytes: 8 << 20, MaxElements: 10_000})
	numbers := "[" + strings.Repeat("0,", 50_000) + "0]"
	var any1 any
	if err := r.DecodeAs("json", []byte(numbers), &any1); !errors.Is(err, ErrTooComplex) {
		t.Fatalf("json array: %v, want ErrTooComplex", err)
	}
	siblings := "<Doc>" + strings.Repeat("<tag>x</tag>", 50_000) + "</Doc>"
	type xdoc struct {
		Tag []string `xml:"tag"`
	}
	var x xdoc
	if err := r.DecodeAs("xml", []byte(siblings), &x); !errors.Is(err, ErrTooComplex) {
		t.Fatalf("xml siblings: %v, want ErrTooComplex", err)
	}
	attrs := "<Doc " + func() string {
		var b strings.Builder
		for i := range 20_000 {
			fmt.Fprintf(&b, `a%d="1" `, i)
		}
		return b.String()
	}() + "></Doc>"
	if err := r.DecodeAs("xml", []byte(attrs), &x); !errors.Is(err, ErrTooComplex) {
		t.Fatalf("xml attributes: %v, want ErrTooComplex", err)
	}
	// inside the limit everything decodes as before
	var ok []int
	if err := r.DecodeAs("json", []byte("[1,2,3]"), &ok); err != nil || len(ok) != 3 {
		t.Fatalf("small document: %v %v", err, ok)
	}
}

func TestElementLimitValidationAndDefaults(t *testing.T) {
	if _, err := New(Config{MaxElements: -1}); err == nil {
		t.Error("a negative max_elements must be refused")
	}
	if _, err := New(Config{MaxElements: MaxElementsCeiling + 1}); err == nil {
		t.Error("max_elements over the ceiling must be refused")
	}
	if DefaultMaxPayloadBytes != 1<<20 {
		t.Errorf("default payload limit = %d, want 1 MiB", DefaultMaxPayloadBytes)
	}
}

// The amplification the review measured: a payload inside the limit must not
// cost hundreds of megabytes at the defaults.
func TestDefaultsKeepAmplificationBounded(t *testing.T) {
	r, _ := New(Config{Formats: []string{"json"}}) // defaults: 1 MiB, 250k elements
	payload := "[" + strings.Repeat("0,", 500_000) + "0]"
	if len(payload) > 1<<20 {
		payload = payload[:1<<20-1] // inside the byte limit
	}
	var v any
	cost := allocated(func() { _ = r.DecodeAs("json", []byte(payload), &v) })
	if cost > 80<<20 {
		t.Fatalf("a ≤1 MiB payload allocated %d MB at the defaults", cost>>20)
	}
}
