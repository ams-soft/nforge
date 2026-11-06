package codec

import (
	"encoding/xml"
	"errors"
	"reflect"
	"strings"
	"testing"
)

var errZip = errors.New("zip must have 5 digits")

type vZip struct {
	Zip string `json:"zip" xml:"zip"`
}

func (z *vZip) Validate() error {
	if len(z.Zip) != 5 {
		return errZip
	}
	return nil
}

// vTag has a value receiver.
type vTag struct {
	Label string `json:"label" xml:"label"`
}

func (t vTag) Validate() error {
	if t.Label == "" {
		return errors.New("label is empty")
	}
	return nil
}

var validationOrder []string

type vOrder struct {
	XMLName  xml.Name        `json:"-" cbor:"-" xml:"order"`
	Name     string          `json:"name" xml:"name"`
	Ship     vZip            `json:"ship" xml:"ship"`
	Bill     *vZip           `json:"bill" xml:"bill"`
	Tags     []vTag          `json:"tags" xml:"tag"`
	ByCode   map[string]vZip `json:"by_code" xml:"-"`
	Internal vZip            `json:"-" cbor:"-" xml:"-"`
}

func (o *vOrder) Validate() error {
	validationOrder = append(validationOrder, "order")
	if o.Name == "" {
		return errors.New("name is required")
	}
	return nil
}

func validatingRegistry(t *testing.T) *Registry {
	t.Helper()
	r, err := New(Config{Formats: []string{"json", "cbor", "xml"}, MaxPayloadBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// Validate used to run on the root value only: a Validate method on a nested
// type, on slice elements or on map values was silently never called.
func TestValidateRunsOnNestedValues(t *testing.T) {
	r := validatingRegistry(t)
	good := `{"name":"n","ship":{"zip":"12345"},"bill":{"zip":"54321"},"tags":[{"label":"a"}],"by_code":{"x":{"zip":"11111"}}}`
	var o vOrder
	if err := r.DecodeAs("json", []byte(good), &o); err != nil {
		t.Fatalf("valid payload: %v", err)
	}

	cases := map[string]struct{ payload, path string }{
		"struct field":  {`{"name":"n","ship":{"zip":"1"}}`, "ship: "},
		"pointer field": {`{"name":"n","ship":{"zip":"12345"},"bill":{"zip":"x"}}`, "bill: "},
		"slice element": {`{"name":"n","ship":{"zip":"12345"},"tags":[{"label":"a"},{"label":""}]}`, "tags[1]: "},
		"map value":     {`{"name":"n","ship":{"zip":"12345"},"by_code":{"k":{"zip":"1"}}}`, "by_code.k: "},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			var o vOrder
			err := r.DecodeAs("json", []byte(c.payload), &o)
			if !errors.Is(err, ErrValidation) {
				t.Fatalf("want ErrValidation, got %v", err)
			}
			var ve *ValidationError
			if !errors.As(err, &ve) || !strings.HasPrefix(ve.Err.Error(), c.path) {
				t.Errorf("the message must name %q: %v", c.path, err)
			}
		})
	}
	// the nested error stays reachable
	var o2 vOrder
	if err := r.DecodeAs("json", []byte(`{"name":"n","ship":{"zip":"1"}}`), &o2); !errors.Is(err, errZip) {
		t.Errorf("errors.Is must reach the nested cause: %v", err)
	}
}

func TestValidateOrderAndRootBehavior(t *testing.T) {
	r := validatingRegistry(t)
	validationOrder = nil
	var o vOrder
	if err := r.DecodeAs("json", []byte(`{"name":"n","ship":{"zip":"12345"}}`), &o); err != nil {
		t.Fatal(err)
	}
	if len(validationOrder) != 1 || validationOrder[0] != "order" {
		t.Errorf("the root Validate must run exactly once: %v", validationOrder)
	}

	// a failing child stops before the parent runs
	validationOrder = nil
	if err := r.DecodeAs("json", []byte(`{"name":"","ship":{"zip":"1"}}`), &o); !errors.Is(err, errZip) {
		t.Fatalf("children are validated before the parent: %v", err)
	}
	if len(validationOrder) != 0 {
		t.Errorf("the parent must not run after a child failed: %v", validationOrder)
	}

	// a root failure is not prefixed with a path
	var ve *ValidationError
	err := r.DecodeAs("json", []byte(`{"name":"","ship":{"zip":"12345"}}`), &o)
	if !errors.As(err, &ve) || ve.Err.Error() != "name is required" {
		t.Errorf("root error = %v", err)
	}
}

func TestValidateOnlyVisitsDecodedFields(t *testing.T) {
	r := validatingRegistry(t)
	// Internal is json:"-": never decoded, its (invalid) zero value is not validated
	var o vOrder
	if err := r.DecodeAs("json", []byte(`{"name":"n","ship":{"zip":"12345"},"internal":{"zip":"1"}}`), &o); err != nil {
		t.Fatalf("an excluded field must not be validated: %v", err)
	}
}

func TestValidateOtherFormats(t *testing.T) {
	r := validatingRegistry(t)
	for _, f := range []string{"cbor", "xml"} {
		bad := vOrder{Name: "n", Ship: vZip{Zip: "1"}}
		data, err := r.EncodeAs(f, &bad)
		if err != nil {
			t.Fatalf("%s encode: %v", f, err)
		}
		var o vOrder
		if err := r.DecodeAs(f, data, &o); !errors.Is(err, errZip) {
			t.Errorf("%s: nested validation: %v", f, err)
		}
	}
}

func TestHasValidator(t *testing.T) {
	type plain struct{ A string }
	type holder struct {
		P []plain
		Z map[string]*vZip
	}
	for _, c := range []struct {
		v    any
		want bool
	}{
		{plain{}, false},
		{[]plain{}, false},
		{vZip{}, true},
		{&vZip{}, true},
		{vTag{}, true},
		{holder{}, true},
		{vOrder{}, true},
		{map[string]any{}, false},
	} {
		if got := hasValidator(typeOfValue(c.v)); got != c.want {
			t.Errorf("hasValidator(%T) = %v, want %v", c.v, got, c.want)
		}
	}
}

func typeOfValue(v any) reflect.Type { return reflect.TypeOf(v) }
