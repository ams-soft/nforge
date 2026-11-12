package codec

import (
	"encoding/xml"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"
)

type deepNode struct {
	XMLName xml.Name  `json:"-" xml:"a"`
	A       *deepNode `json:"next" xml:"a"`
	V       int       `json:"v" codec:"required"`
}

// allocated reports the bytes allocated while fn runs (cumulative, so it
// does not depend on when the GC runs).
func allocated(fn func()) uint64 {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	fn()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// A nested document must be rejected at a fixed depth, not decoded at a cost
// that grows with the square of its depth (683 KB used 10 GB).
func TestXMLNestingIsBounded(t *testing.T) {
	r, err := New(Config{Formats: []string{"xml"}, Default: "xml"})
	if err != nil {
		t.Fatal(err)
	}
	const depth = 100_000
	payload := strings.Repeat("<a>", depth) + strings.Repeat("</a>", depth)

	var got error
	start := time.Now()
	bytes := allocated(func() {
		var v deepXML
		got = r.DecodeAs("xml", []byte(payload), &v)
	})
	if !errors.Is(got, ErrSyntax) {
		t.Fatalf("want ErrSyntax for depth %d, got %v", depth, got)
	}
	if bytes > 64<<20 {
		t.Errorf("decoding a %d-deep document allocated %d MB, want < 64 MB", depth, bytes>>20)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("took %v", elapsed)
	}
}

func TestXMLNestingWithinTheLimitStillDecodes(t *testing.T) {
	r, _ := New(Config{Formats: []string{"xml"}, Default: "xml"})
	const depth = maxXMLDepth - 1
	payload := strings.Repeat("<a>", depth) + strings.Repeat("</a>", depth)
	var v deepXML
	if err := r.DecodeAs("xml", []byte(payload), &v); err != nil {
		t.Fatalf("depth %d must decode: %v", depth, err)
	}
}

// Skipping unknown deep content must not recurse or build paths either.
func TestXMLUnknownDeepContentIsSkippedCheaply(t *testing.T) {
	r, _ := New(Config{Formats: []string{"xml"}, Default: "xml", MaxPayloadBytes: 8 << 20, MaxElements: 1_000_000})
	const depth = 200_000
	payload := "<a><zzz>" + strings.Repeat("<x>", depth) + strings.Repeat("</x>", depth) + "</zzz></a>"
	var got error
	bytes := allocated(func() {
		var v deepXML
		got = r.DecodeAs("xml", []byte(payload), &v)
	})
	if got != nil {
		t.Fatalf("unknown content must be skipped: %v", got)
	}
	if bytes > 256<<20 {
		t.Errorf("skipping allocated %d MB", bytes>>20)
	}
}

// A duplicate member is refused by a linear pre-scan, however deep the
// document, without building paths or trees (the old fallback used 337 MB for a
// 131 KB payload).
func TestJSONDuplicateMemberIsRefusedCheaplyOnDeepPayloads(t *testing.T) {
	r, _ := New(Config{Formats: []string{"json"}, StrictUnknownFields: true})
	const depth = 9000
	deep := func(head string) string {
		return head + `"next":` + strings.Repeat(`{"v":1,"next":`, depth) + `null` + strings.Repeat(`}`, depth) + `}`
	}
	var got error
	bytes := allocated(func() {
		var v deepNode
		got = r.DecodeAs("json", []byte(deep(`{"v":1,"v":2,`)), &v)
	})
	if !errors.Is(got, ErrDuplicateField) {
		t.Fatalf("duplicate member: %v", got)
	}
	if bytes > 64<<20 {
		t.Errorf("refusing allocated %d MB", bytes>>20)
	}
	var v deepNode
	if err := r.DecodeAs("json", []byte(deep(`{"v":1,`)), &v); err != nil {
		t.Fatalf("the same document without a duplicate must decode: %v", err)
	}
}

func TestXPathString(t *testing.T) {
	var root *xpath
	cases := []struct {
		p    *xpath
		want string
	}{
		{root, ""},
		{root.field("a"), "a"},
		{root.field("a").field("b"), "a.b"},
		{root.field("a").at(2), "a[2]"},
		{root.field("a").at(2).field("b"), "a[2].b"},
		{root.field("a").attr("id"), "a@id"},
		{root.at(0).field("x"), "[0].x"},
	}
	for _, c := range cases {
		if got := c.p.String(); got != c.want {
			t.Errorf("String() = %q, want %q", got, c.want)
		}
	}
	if got := root.leaf("name"); got != "name" {
		t.Errorf("root.leaf = %q", got)
	}
	if got := root.field("a").attrLeaf("id"); got != "a@id" {
		t.Errorf("attrLeaf = %q", got)
	}
	if got := root.or("fallback"); got != "fallback" {
		t.Errorf("or = %q", got)
	}
	if got := root.field("a").or("fallback"); got != "a" {
		t.Errorf("or = %q", got)
	}
}

// deepXML has no required field, so a bare nested document decodes.
type deepXML struct {
	XMLName xml.Name `xml:"a"`
	A       *deepXML `xml:"a"`
}
