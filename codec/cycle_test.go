package codec

import (
	"encoding/xml"
	"errors"
	"reflect"
	"testing"
)

type cycNode struct {
	Name string   `json:"name" xml:"name"`
	Next *cycNode `json:"next" xml:"next"`
}

// A back-reference excluded from the format is not a cycle for that format.
type cycParent struct {
	Name     string       `json:"name" xml:"name"`
	Parent   *cycParent   `json:"-" cbor:"-" xml:"-"`
	Children []*cycParent `json:"children,omitempty" xml:"child,omitempty"`
}

type cycAny struct {
	XMLName xml.Name `json:"-" cbor:"-" xml:"a"`
	V       any      `json:"v" xml:"v"`
}

func selfRef() *cycNode {
	a := &cycNode{Name: "a"}
	a.Next = a
	return a
}

// A cyclic value must be an EncodeError for every format; the CBOR library and
// the XML encoder used to overflow the stack, which kills the process (G-27).
func TestEncodeRejectsCycles(t *testing.T) {
	r, err := New(Config{Formats: []string{"json", "cbor", "xml"}})
	if err != nil {
		t.Fatal(err)
	}
	longer := &cycNode{Name: "a", Next: &cycNode{Name: "b"}}
	longer.Next.Next = longer

	for _, f := range []string{"json", "cbor", "xml"} {
		for name, v := range map[string]any{"self": selfRef(), "two nodes": longer} {
			if _, err := r.EncodeAs(f, v); err == nil {
				t.Errorf("%s/%s: want an error", f, name)
			} else if f != "json" && !errors.Is(err, ErrEncode) {
				t.Errorf("%s/%s: want ErrEncode, got %v", f, name, err)
			}
		}
	}
}

func TestCheckRequiredHandlesCycles(t *testing.T) {
	// the type has a required field so the walk runs
	type node struct {
		Name string `json:"name" codec:"required"`
		Next *node  `json:"next"`
	}
	a := &node{Name: "a"}
	a.Next = a
	err := CheckRequired(a)
	if !errors.Is(err, ErrEncode) {
		t.Fatalf("want ErrEncode for a cycle, got %v", err)
	}

	// an interface hides the type: the dynamic value is walked
	m := map[string]any{}
	m["self"] = m
	if err := CheckRequired(cycAny{V: m}); !errors.Is(err, ErrEncode) {
		t.Fatalf("want ErrEncode for a map that contains itself, got %v", err)
	}
}

func TestSharedPointersAndExcludedBackReferencesAreNotCycles(t *testing.T) {
	r, _ := New(Config{Formats: []string{"json", "cbor", "xml"}})

	shared := &cycNode{Name: "shared"}
	type pair struct {
		XMLName xml.Name `json:"-" cbor:"-" xml:"pair"`
		A       *cycNode `json:"a" xml:"a"`
		B       *cycNode `json:"b" xml:"b"`
	}
	root := &cycParent{Name: "root"}
	root.Children = []*cycParent{{Name: "c1", Parent: root}, {Name: "c2", Parent: root}}

	for _, f := range []string{"json", "cbor", "xml"} {
		if _, err := r.EncodeAs(f, pair{A: shared, B: shared}); err != nil {
			t.Errorf("%s: a pointer reached twice by different routes is not a cycle: %v", f, err)
		}
		if _, err := r.EncodeAs(f, root); err != nil {
			t.Errorf("%s: a back-reference the format does not encode is not a cycle: %v", f, err)
		}
	}
}

func TestMayCycle(t *testing.T) {
	type flat struct {
		A string
		B []int
	}
	type tree struct {
		Items []flat
		Inner *flat
	}
	cases := []struct {
		v    any
		want bool
	}{
		{flat{}, false},
		{tree{}, false},
		{&tree{}, false},
		{cycNode{}, true},
		{cycParent{}, true},
		{cycAny{}, true},
		{map[string]any{}, true},
		{[]flat{}, false},
	}
	for _, c := range cases {
		if got := mayCycle(reflect.TypeOf(c.v)); got != c.want {
			t.Errorf("mayCycle(%T) = %v, want %v", c.v, got, c.want)
		}
	}
}

// With no required field and no interface there is nothing to check (G-28):
// the 1 MB response must not be walked.
func TestCheckRequiredSkipsTypesWithoutRequiredFields(t *testing.T) {
	d := fitDoc(1_000_000)
	if n := testing.AllocsPerRun(5, func() { _ = CheckRequired(&d) }); n > 0 {
		t.Errorf("CheckRequired on an untagged response allocated %v times, want 0", n)
	}
	// a tagged twin is still checked
	req := benchDocReq{Orders: []benchOrderReq{{ID: 1, Customer: "x", Lines: nil}}}
	if err := CheckRequired(&req); err == nil {
		t.Error("a missing required field must still be reported")
	}
}
