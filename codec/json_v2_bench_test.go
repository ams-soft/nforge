package codec

// TEMPORARY investigation file: JSON v1 vs JSON v2 benchmarks. Not part of
// the framework; delete after the investigation.
//
// Layout:
//   - this file (no build tag): fixtures, the nForge codec, the Registry and
//     the stdlib encoding/json (whatever engine the toolchain selects);
//   - json_v2_engine_bench_test.go (//go:build goexperiment.jsonv2): adds the
//     native encoding/json/v2 rows, which only exist when the experiment is on.
//
// Run: go test ./codec -run '^$' -bench '^BenchmarkJSON' -benchmem

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"testing"
	"time"
)

// ------------------------------------------------------------ fixtures

// Plain types: no codec tags, so nForge does no presence pass.
type benchAddr struct {
	Street  string `json:"street"`
	City    string `json:"city"`
	Zip     string `json:"zip"`
	Country string `json:"country"`
}

type benchLine struct {
	SKU         string   `json:"sku"`
	Description string   `json:"description"`
	Qty         int      `json:"qty"`
	UnitCents   int64    `json:"unit_cents"`
	TotalCents  int64    `json:"total_cents"`
	Taxable     bool     `json:"taxable"`
	Tags        []string `json:"tags"`
}

type benchOrder struct {
	ID         int64       `json:"id"`
	Customer   string      `json:"customer"`
	Email      string      `json:"email"`
	Paid       bool        `json:"paid"`
	TotalCents int64       `json:"total_cents"`
	Created    string      `json:"created"`
	Shipping   benchAddr   `json:"shipping"`
	Billing    *benchAddr  `json:"billing,omitempty"`
	Lines      []benchLine `json:"lines"`
	Notes      string      `json:"notes,omitempty"`
}

type benchDoc struct {
	Version int          `json:"version"`
	Orders  []benchOrder `json:"orders"`
}

// Required-field twins: identical wire format, but nForge sees
// codec:"required" and runs the presence pass.
type benchAddrReq struct {
	Street  string `json:"street" codec:"required"`
	City    string `json:"city"`
	Zip     string `json:"zip"`
	Country string `json:"country"`
}

type benchLineReq struct {
	SKU         string   `json:"sku" codec:"required"`
	Description string   `json:"description"`
	Qty         int      `json:"qty"`
	UnitCents   int64    `json:"unit_cents"`
	TotalCents  int64    `json:"total_cents"`
	Taxable     bool     `json:"taxable"`
	Tags        []string `json:"tags"`
}

type benchOrderReq struct {
	ID         int64          `json:"id" codec:"required"`
	Customer   string         `json:"customer" codec:"required"`
	Email      string         `json:"email"`
	Paid       bool           `json:"paid"`
	TotalCents int64          `json:"total_cents"`
	Created    string         `json:"created"`
	Shipping   benchAddrReq   `json:"shipping"`
	Billing    *benchAddrReq  `json:"billing,omitempty"`
	Lines      []benchLineReq `json:"lines" codec:"required"`
	Notes      string         `json:"notes,omitempty"`
}

type benchDocReq struct {
	Version int             `json:"version"`
	Orders  []benchOrderReq `json:"orders" codec:"required"`
}

// bigBase is 2^53+1: the smallest positive integer a float64 cannot hold.
const bigBase int64 = 1<<53 + 1

// buildDoc is deterministic: no randomness, no clock. The text avoids
// <, > and & on purpose so v1 (HTML-escaping) and v2 emit the same bytes.
func buildDoc(orders, lines int) benchDoc {
	d := benchDoc{Version: 3, Orders: make([]benchOrder, orders)}
	for i := range d.Orders {
		o := &d.Orders[i]
		o.ID = bigBase + int64(i)*7919
		o.Customer = "Customer " + strconv.Itoa(i) + " São Paulo"
		o.Email = "customer" + strconv.Itoa(i) + "@example.com"
		o.Paid = i%2 == 0
		o.TotalCents = bigBase + int64(i)*104729
		o.Created = "2026-01-" + fmt.Sprintf("%02d", i%28+1) + "T10:20:30Z"
		o.Shipping = benchAddr{Street: "Rua " + strconv.Itoa(i) + ", 100", City: "São Paulo", Zip: "01000-000", Country: "BR"}
		if i%3 == 0 {
			b := o.Shipping
			b.Street = "Av. Billing " + strconv.Itoa(i)
			o.Billing = &b
		}
		if i%5 == 0 {
			o.Notes = "deliver in the afternoon, ring twice"
		}
		o.Lines = make([]benchLine, lines)
		for j := range o.Lines {
			l := &o.Lines[j]
			l.SKU = "SKU-" + strconv.Itoa(i) + "-" + strconv.Itoa(j)
			l.Description = "Item number " + strconv.Itoa(j) + " of the order, with a longer description text"
			l.Qty = j + 1
			l.UnitCents = bigBase + int64(j)*13
			l.TotalCents = l.UnitCents * int64(l.Qty)
			l.Taxable = j%2 == 1
			l.Tags = []string{"new", "tag" + strconv.Itoa(j)} // never nil: v1 null vs v2 [] is covered separately
		}
	}
	return d
}

type benchFixture struct {
	name    string
	doc     *benchDoc
	payload []byte
}

var benchFixtures = func() []benchFixture {
	small := buildDoc(1, 1)
	medium := fitDoc(20_000)
	large := fitDoc(1_000_000)
	var out []benchFixture
	for _, f := range []struct {
		name string
		doc  benchDoc
	}{{"small", small}, {"medium", medium}, {"large", large}} {
		doc := f.doc
		b, err := json.Marshal(&doc)
		if err != nil {
			panic(err)
		}
		out = append(out, benchFixture{name: f.name, doc: &doc, payload: b})
	}
	return out
}()

// fitDoc grows the order count until the encoding reaches target bytes.
func fitDoc(target int) benchDoc {
	for n := 1; ; n++ {
		d := buildDoc(n, 3)
		b, err := json.Marshal(&d)
		if err != nil {
			panic(err)
		}
		if len(b) >= target {
			return d
		}
	}
}

// ------------------------------------------------------------ implementations

type benchOps struct {
	name       string
	marshal    func(any) ([]byte, error)
	unmarshal  func([]byte, any) error
	encodeTo   func(io.Writer, any) error
	decodeFrom func(io.Reader, any) error
}

// strictCase is one row of the strict/required decode benchmark.
type strictCase struct {
	name   string
	newDst func() any
	decode func([]byte, any) error
	note   string
}

// Rows appended by json_v2_engine_bench_test.go when encoding/json/v2 exists.
var (
	extraOps    []benchOps
	extraStrict []strictCase
)

// stdDecode is what nForge's jsonDecodeAll does, minus error mapping and the
// target checks: Decoder + UseNumber + "nothing after the value".
func stdDecode(r io.Reader, v any) error {
	d := json.NewDecoder(r)
	d.UseNumber()
	if err := d.Decode(v); err != nil {
		return err
	}
	if _, err := d.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing data")
	}
	return nil
}

func stdDecodeStrict(r io.Reader, v any) error {
	d := json.NewDecoder(r)
	d.UseNumber()
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if _, err := d.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing data")
	}
	return nil
}

func benchRegistry() *Registry {
	r, err := New(Config{Formats: []string{"json"}, MaxPayloadBytes: 16 << 20})
	if err != nil {
		panic(err)
	}
	return r
}

func benchImpls() []benchOps {
	nf := NewJSON(Options{})
	reg := benchRegistry()
	ops := []benchOps{
		{
			name:       "nforge",
			marshal:    nf.Encode,
			unmarshal:  nf.Decode,
			encodeTo:   nf.EncodeTo,
			decodeFrom: nf.DecodeFrom,
		},
		{
			name:       "registry",
			marshal:    func(v any) ([]byte, error) { return reg.EncodeAs("json", v) },
			unmarshal:  func(b []byte, v any) error { return reg.DecodeAs("json", b, v) },
			encodeTo:   func(w io.Writer, v any) error { return reg.EncodeTo("json", w, v) },
			decodeFrom: func(r io.Reader, v any) error { return reg.DecodeFrom("json", r, v) },
		},
		{
			name:       "stdjson",
			marshal:    json.Marshal,
			unmarshal:  json.Unmarshal,
			encodeTo:   func(w io.Writer, v any) error { return json.NewEncoder(w).Encode(v) },
			decodeFrom: stdDecode,
		},
		{
			// nForge's exact decode path without analyze/target checks:
			// isolates the framework's own overhead from the stdlib cost.
			name:      "stdjson-nforge-path",
			unmarshal: func(b []byte, v any) error { return stdDecode(bytes.NewReader(b), v) },
		},
	}
	return append(ops, extraOps...)
}

func strictCases() []strictCase {
	plain := func() any { return new(benchDoc) }
	req := func() any { return new(benchDocReq) }
	nfStrict := NewJSON(Options{StrictUnknownFields: true})
	nfPlain := NewJSON(Options{})
	cases := []strictCase{
		{"nforge/baseline-plain", plain, nfPlain.Decode, "no strict, no required (control)"},
		{"nforge/strict", plain, nfStrict.Decode, "strict unknown fields via presence pass"},
		{"nforge/required", req, nfPlain.Decode, "required fields via presence pass"},
		{"nforge/strict+required", req, nfStrict.Decode, "both"},
		{"stdjson/baseline-req-types", req, func(b []byte, v any) error { return stdDecode(bytes.NewReader(b), v) }, "control: same types, no checks"},
		{"stdjson/disallow-unknown", plain, func(b []byte, v any) error { return stdDecodeStrict(bytes.NewReader(b), v) }, "unknown fields only; NO required support"},
	}
	return append(cases, extraStrict...)
}

// ------------------------------------------------------------ sinks

var (
	sinkBytes []byte
	sinkAny   any
	sinkErr   error
)

func sized(b *testing.B, n int) { b.SetBytes(int64(n)); b.ReportAllocs() }

// ------------------------------------------------------------ benchmarks

func BenchmarkJSONEncode(b *testing.B) {
	for _, fx := range benchFixtures {
		for _, op := range benchImpls() {
			if op.marshal == nil {
				continue
			}
			b.Run(fx.name+"/"+op.name, func(b *testing.B) {
				v := any(fx.doc)
				out, err := op.marshal(v)
				if err != nil {
					b.Fatal(err)
				}
				sized(b, len(out))
				for b.Loop() {
					sinkBytes, sinkErr = op.marshal(v)
				}
				if sinkErr != nil {
					b.Fatal(sinkErr)
				}
			})
		}
	}
}

func BenchmarkJSONDecode(b *testing.B) {
	for _, fx := range benchFixtures {
		for _, op := range benchImpls() {
			if op.unmarshal == nil {
				continue
			}
			b.Run(fx.name+"/"+op.name, func(b *testing.B) {
				sized(b, len(fx.payload))
				for b.Loop() {
					dst := new(benchDoc)
					sinkErr = op.unmarshal(fx.payload, dst)
					sinkAny = dst
				}
				if sinkErr != nil {
					b.Fatal(sinkErr)
				}
			})
		}
	}
}

// BenchmarkJSONEncodeStream writes to io.Discard: it measures the encoder,
// not the sink.
func BenchmarkJSONEncodeStream(b *testing.B) {
	for _, fx := range benchFixtures {
		for _, op := range benchImpls() {
			if op.encodeTo == nil {
				continue
			}
			b.Run(fx.name+"/"+op.name, func(b *testing.B) {
				v := any(fx.doc)
				sized(b, len(fx.payload))
				for b.Loop() {
					sinkErr = op.encodeTo(io.Discard, v)
				}
				if sinkErr != nil {
					b.Fatal(sinkErr)
				}
			})
		}
	}
}

func BenchmarkJSONDecodeStream(b *testing.B) {
	for _, fx := range benchFixtures {
		for _, op := range benchImpls() {
			if op.decodeFrom == nil {
				continue
			}
			b.Run(fx.name+"/"+op.name, func(b *testing.B) {
				rd := bytes.NewReader(fx.payload)
				sized(b, len(fx.payload))
				for b.Loop() {
					rd.Reset(fx.payload)
					dst := new(benchDoc)
					sinkErr = op.decodeFrom(rd, dst)
					sinkAny = dst
				}
				if sinkErr != nil {
					b.Fatal(sinkErr)
				}
			})
		}
	}
}

// BenchmarkJSONDecodeStrict compares strict / required decoding. The rows
// are NOT equivalent: only nForge implements required fields and full-path
// errors, and the strict rows differ in how unknown fields are found. Read
// each row with its note.
func BenchmarkJSONDecodeStrict(b *testing.B) {
	for _, fx := range benchFixtures {
		for _, sc := range strictCases() {
			b.Run(fx.name+"/"+sc.name, func(b *testing.B) {
				sized(b, len(fx.payload))
				for b.Loop() {
					dst := sc.newDst()
					sinkErr = sc.decode(fx.payload, dst)
					sinkAny = dst
				}
				if sinkErr != nil {
					b.Fatal(sinkErr)
				}
			})
		}
	}
}

// ------------------------------------------------------------ correctness

func TestJSONBenchFixtures(t *testing.T) {
	want := map[string][2]int{"small": {256, 2048}, "medium": {10_000, 50_000}, "large": {1_000_000, 1_100_000}}
	for _, fx := range benchFixtures {
		r := want[fx.name]
		if n := len(fx.payload); n < r[0] || n > r[1] {
			t.Errorf("%s payload is %d bytes, want %d..%d", fx.name, n, r[0], r[1])
		}
		t.Logf("%s payload: %d bytes, %d orders", fx.name, len(fx.payload), len(fx.doc.Orders))
	}

	for _, fx := range benchFixtures {
		for _, op := range benchImpls() {
			if op.marshal != nil {
				out, err := op.marshal(fx.doc)
				if err != nil {
					t.Fatalf("%s/%s marshal: %v", fx.name, op.name, err)
				}
				if !bytes.Equal(bytes.TrimSpace(out), fx.payload) {
					t.Errorf("%s/%s: encoded bytes differ from the stdlib v1 reference", fx.name, op.name)
				}
			}
			if op.unmarshal != nil {
				var got benchDoc
				if err := op.unmarshal(fx.payload, &got); err != nil {
					t.Fatalf("%s/%s unmarshal: %v", fx.name, op.name, err)
				}
				if !reflect.DeepEqual(&got, fx.doc) {
					t.Errorf("%s/%s: round trip changed the document (int64 precision?)", fx.name, op.name)
				}
			}
			if op.decodeFrom != nil {
				var got benchDoc
				if err := op.decodeFrom(bytes.NewReader(fx.payload), &got); err != nil {
					t.Fatalf("%s/%s decodeFrom: %v", fx.name, op.name, err)
				}
				if !reflect.DeepEqual(&got, fx.doc) {
					t.Errorf("%s/%s: stream round trip changed the document", fx.name, op.name)
				}
			}
			if op.encodeTo != nil {
				var buf bytes.Buffer
				if err := op.encodeTo(&buf, fx.doc); err != nil {
					t.Fatalf("%s/%s encodeTo: %v", fx.name, op.name, err)
				}
				if !bytes.Equal(bytes.TrimSpace(buf.Bytes()), fx.payload) {
					t.Errorf("%s/%s: streamed bytes differ from the reference", fx.name, op.name)
				}
			}
		}
		for _, sc := range strictCases() {
			dst := sc.newDst()
			if err := sc.decode(fx.payload, dst); err != nil {
				t.Fatalf("%s/%s: %v", fx.name, sc.name, err)
			}
		}
	}

	// The strict/required rows must actually reject what they claim to.
	bad := []byte(`{"version":1,"orders":[{"id":1,"customer":"x","lines":[],"surprise":true}]}`)
	missing := []byte(`{"version":1,"orders":[{"id":1,"lines":[]}]}`)
	for _, sc := range strictCases() {
		t.Logf("%-34s unknown-field: %-5v missing-required: %-5v  (%s)", sc.name,
			sc.decode(bad, sc.newDst()) != nil, sc.decode(missing, sc.newDst()) != nil, sc.note)
	}
}

// semanticsReport describes how an engine treats the cases that differ
// between JSON v1 and v2. It only reports; it asserts nothing.
func semanticsReport(marshal func(any) ([]byte, error), unmarshal func([]byte, any) error) []string {
	var out []string
	add := func(name string, v any, err error) {
		if err != nil {
			out = append(out, fmt.Sprintf("%-26s error: %v", name, err))
		} else {
			out = append(out, fmt.Sprintf("%-26s %v", name, v))
		}
	}
	str := func(v any) any {
		b, err := marshal(v)
		if err != nil {
			return "ERR " + err.Error()
		}
		return string(b)
	}
	type nilSlice struct {
		S []int `json:"s"`
	}
	type named struct {
		Name string `json:"name"`
	}
	type num struct {
		N int64 `json:"n"`
	}
	type dur struct {
		D time.Duration `json:"d"`
	}
	add("marshal nil slice", str(nilSlice{}), nil)
	add("marshal nil map", str(map[string]int(nil)), nil)
	add("marshal <a&b> string", str("<a&b>"), nil)
	add("marshal time.Duration", str(dur{D: 1_000_000_000}), nil)
	distinct := map[string]bool{}
	for range 40 {
		b, _ := marshal(map[string]int{"b": 1, "a": 2, "c": 3, "d": 4, "e": 5})
		distinct[string(b)] = true
	}
	add("marshal map key order", fmt.Sprintf("%d distinct outputs in 40 runs", len(distinct)), nil)

	try := func(name, in string, dst any) {
		err := unmarshal([]byte(in), dst)
		if err != nil {
			add(name, nil, err)
			return
		}
		add(name, fmt.Sprintf("accepted -> %+v", reflect.ValueOf(dst).Elem().Interface()), nil)
	}
	try("case-insensitive key", `{"NAME":"x"}`, new(named))
	try("duplicate key", `{"name":"a","name":"b"}`, new(named))
	try("unknown key", `{"name":"a","zzz":1}`, new(named))
	try("trailing data", `{"name":"a"} x`, new(named))
	try("invalid UTF-8", "{\"name\":\"\xff\"}", new(named))
	try("string into int64", `{"n":"5"}`, new(num))
	try("1.0 into int64", `{"n":1.0}`, new(num))
	try("1e2 into int64", `{"n":1e2}`, new(num))
	try("null into string", `{"name":null}`, new(named))
	var anyv map[string]any
	if err := unmarshal([]byte(`{"n":9007199254740993}`), &anyv); err != nil {
		add("big int into any", nil, err)
	} else {
		add("big int into any", fmt.Sprintf("%T %v", anyv["n"], anyv["n"]), nil)
	}
	return out
}

func TestJSONSemanticsStdjson(t *testing.T) {
	for _, l := range semanticsReport(json.Marshal, json.Unmarshal) {
		t.Log("stdjson  ", l)
	}
}
