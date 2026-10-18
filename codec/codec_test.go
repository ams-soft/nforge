package codec

import (
	"bytes"
	"encoding/xml"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

type Address struct {
	Street string `json:"street"`
	City   string `json:"city"`
	Zip    string `json:"zip,omitempty"`
}

type Tag struct {
	Key   string `json:"key" xml:"key,attr"`
	Value string `json:"value" xml:",chardata"`
}

// Order is the reference struct: nesting, pointers, slices, maps, XML
// attributes, time.Time, []byte and int64 at the limit.
type Order struct {
	XMLName  xml.Name          `json:"-" cbor:"-" xml:"order"`
	ID       int64             `json:"id" xml:"id,attr"`
	Customer string            `json:"customer"`
	Total    float64           `json:"total"`
	Paid     bool              `json:"paid"`
	Shipping *Address          `json:"shipping,omitempty"`
	Billing  Address           `json:"billing"`
	Items    []Item            `json:"items"`
	Tags     []Tag             `json:"tags,omitempty"`
	Meta     map[string]string `json:"meta,omitempty"`
	Note     *string           `json:"note,omitempty"`
	Created  time.Time         `json:"created"`
	Blob     []byte            `json:"blob,omitempty"`
	Counts   map[string]int    `json:"counts,omitempty"`
}

type Item struct {
	SKU   string `json:"sku" xml:"sku,attr"`
	Qty   uint32 `json:"qty"`
	Price int64  `json:"price"`
}

func referenceOrder() Order {
	note := "deliver in the afternoon & <careful>"
	return Order{
		ID:       math.MaxInt64,
		Customer: "Ana",
		Total:    1234.56,
		Paid:     true,
		Shipping: &Address{Street: "Rua A, 1", City: "São Paulo", Zip: "01000-000"},
		Billing:  Address{Street: "Rua B, 2", City: "Rio"},
		Items: []Item{
			{SKU: "X-1", Qty: 2, Price: math.MinInt64},
			{SKU: "Y-2", Qty: 1, Price: 990},
		},
		Tags:    []Tag{{Key: "prio", Value: "alta"}},
		Meta:    map[string]string{"canal": "web", "cupom": "PROMO"},
		Note:    &note,
		Created: time.Date(2026, 9, 27, 13, 45, 1, 123456789, time.FixedZone("BRT", -3*3600)),
		Blob:    []byte{0, 1, 2, 250, 255},
		Counts:  map[string]int{"a": 1, "b": -2},
	}
}

func allCodecs(opts Options) []StreamCodec {
	return []StreamCodec{NewJSON(opts), NewCBOR(opts), NewXML(opts)}
}

func name(c Codec) string { return c.ContentType() }

func equalOrder(t *testing.T, want, got Order) {
	t.Helper()
	got.XMLName = xml.Name{}
	if !want.Created.Equal(got.Created) {
		t.Fatalf("Created: want %v, got %v", want.Created, got.Created)
	}
	want.Created, got.Created = time.Time{}, time.Time{}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("round-trip diferente:\nwant %#v\ngot  %#v", want, got)
	}
}

func TestRoundTrip(t *testing.T) {
	for _, c := range allCodecs(Options{}) {
		t.Run(name(c), func(t *testing.T) {
			want := referenceOrder()
			data, err := c.Encode(want)
			if err != nil {
				t.Fatal(err)
			}
			var got Order
			if err := c.Decode(data, &got); err != nil {
				t.Fatalf("decode: %v\npayload: %s", err, data)
			}
			equalOrder(t, want, got)
		})
	}
}

func TestRoundTripStreaming(t *testing.T) {
	for _, c := range allCodecs(Options{StrictUnknownFields: true}) {
		t.Run(name(c), func(t *testing.T) {
			want := referenceOrder()
			var buf bytes.Buffer
			if err := c.EncodeTo(&buf, want); err != nil {
				t.Fatal(err)
			}
			var got Order
			if err := c.DecodeFrom(&buf, &got); err != nil {
				t.Fatal(err)
			}
			equalOrder(t, want, got)
		})
	}
}

func TestRoundTripZeroValue(t *testing.T) {
	for _, c := range allCodecs(Options{}) {
		t.Run(name(c), func(t *testing.T) {
			var want Order
			data, err := c.Encode(want)
			if err != nil {
				t.Fatal(err)
			}
			var got Order
			if err := c.Decode(data, &got); err != nil {
				t.Fatal(err)
			}
			// nil vs empty in slices is a format detail; compare the rest.
			got.Items = nil
			equalOrder(t, want, got)
		})
	}
}

func TestXMLShape(t *testing.T) {
	data, err := NewXML(Options{}).Encode(Order{ID: 7, Customer: "Ana", Items: []Item{{SKU: "A", Qty: 1}}, Tags: []Tag{{Key: "k", Value: "v"}}})
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, want := range []string{`<order id="7">`, `<customer>Ana</customer>`, `<items sku="A"><qty>1</qty>`, `<tags key="k">v</tags>`} {
		if !strings.Contains(s, want) {
			t.Errorf("XML missing %q:\n%s", want, s)
		}
	}
	// omitempty inherited from the json tag: empty optional fields are omitted.
	for _, absent := range []string{"<shipping", "<meta", "<note", "<blob"} {
		if strings.Contains(s, absent) {
			t.Errorf("XML should not contain %q:\n%s", absent, s)
		}
	}
}

func TestCBORSmallerThanJSON(t *testing.T) {
	o := referenceOrder()
	j, _ := NewJSON(Options{}).Encode(o)
	c, _ := NewCBOR(Options{}).Encode(o)
	if len(c) >= len(j) {
		t.Fatalf("CBOR (%d bytes) should be smaller than JSON (%d bytes)", len(c), len(j))
	}
}

func TestJSONInt64IntoAny(t *testing.T) {
	var v map[string]any
	if err := NewJSON(Options{}).Decode([]byte(`{"n":9223372036854775807}`), &v); err != nil {
		t.Fatal(err)
	}
	if got := v["n"].(interface{ String() string }).String(); got != "9223372036854775807" {
		t.Fatalf("lost precision: %s", got)
	}
}

// ------------------------------------------------------------ errors

type strictTarget struct {
	Name  string   `json:"name" codec:"required"`
	Inner struct { // nested to test the field path
		Zip string `json:"zip" codec:"required"`
	} `json:"inner"`
	Age int `json:"age,omitempty"`
}

func payloads(t *testing.T, v any) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	for _, c := range allCodecs(Options{}) {
		data, err := c.Encode(v)
		if err != nil {
			t.Fatal(err)
		}
		out[c.ContentType()] = data
	}
	return out
}

func TestUnknownField(t *testing.T) {
	type extra struct {
		XMLName xml.Name `json:"-" cbor:"-" xml:"strictTarget"`
		Name    string   `json:"name"`
		Inner   struct {
			Zip   string `json:"zip"`
			Bogus string `json:"bogus"`
		} `json:"inner"`
	}
	src := extra{Name: "a"}
	src.Inner.Zip, src.Inner.Bogus = "1", "x"
	p := payloads(t, src)

	for _, strict := range []bool{false, true} {
		for _, c := range allCodecs(Options{StrictUnknownFields: strict}) {
			var dst strictTarget
			err := c.Decode(p[c.ContentType()], &dst)
			if !strict {
				if err != nil {
					t.Errorf("%s permissivo: %v", name(c), err)
				}
				continue
			}
			var ue *UnknownFieldError
			if !errors.As(err, &ue) || !errors.Is(err, ErrUnknownField) || ue.Field != "inner.bogus" {
				t.Errorf("%s strict: expected UnknownFieldError inner.bogus, got %v", name(c), err)
			}
		}
	}
}

func TestMissingRequired(t *testing.T) {
	type partial struct {
		XMLName xml.Name `json:"-" cbor:"-" xml:"strictTarget"`
		Name    string   `json:"name"`
		Inner   struct {
			Other string `json:"other"`
		} `json:"inner"`
	}
	p := payloads(t, partial{Name: "a"})
	for _, c := range allCodecs(Options{}) {
		var dst strictTarget
		err := c.Decode(p[c.ContentType()], &dst)
		var me *MissingFieldError
		if !errors.As(err, &me) || me.Field != "inner.zip" {
			t.Errorf("%s: expected MissingFieldError inner.zip, got %v", name(c), err)
		}
		if errors.Is(err, ErrValidation) {
			t.Errorf("%s: missing field must not be a validation error", name(c))
		}
	}
}

func TestTypeMismatch(t *testing.T) {
	type wrong struct {
		XMLName xml.Name `json:"-" cbor:"-" xml:"strictTarget"`
		Name    string   `json:"name"`
		Age     string   `json:"age"`
	}
	p := payloads(t, wrong{Name: "a", Age: "velho"})
	for _, c := range allCodecs(Options{}) {
		var dst struct {
			XMLName xml.Name `json:"-" cbor:"-" xml:"strictTarget"`
			Name    string   `json:"name"`
			Age     int      `json:"age"`
		}
		err := c.Decode(p[c.ContentType()], &dst)
		var te *TypeError
		if !errors.As(err, &te) || !errors.Is(err, ErrTypeMismatch) {
			t.Fatalf("%s: expected TypeError, got %v", name(c), err)
		}
		if !strings.EqualFold(te.Field, "age") || te.Expected != "int" {
			t.Errorf("%s: TypeError without field/type: %+v", name(c), te)
		}
	}
}

func TestSyntaxError(t *testing.T) {
	bad := map[string][]byte{
		JSON: []byte(`{"name": `),
		CBOR: {0xbf, 0x61},
		XML:  []byte(`<strictTarget><name>a</strictTarget>`),
	}
	for _, c := range allCodecs(Options{}) {
		var dst strictTarget
		err := c.Decode(bad[c.ContentType()], &dst)
		var se *SyntaxError
		if !errors.As(err, &se) || !errors.Is(err, ErrSyntax) {
			t.Errorf("%s: expected SyntaxError, got %T %v", name(c), err, err)
		}
		if errors.Unwrap(err) != nil {
			t.Errorf("%s: SyntaxError must not expose the lib's error", name(c))
		}
	}
}

func TestTrailingData(t *testing.T) {
	for _, c := range allCodecs(Options{}) {
		data, _ := c.Encode(Address{City: "x"})
		data = append(data, data...)
		var dst Address
		if err := c.Decode(data, &dst); !errors.Is(err, ErrSyntax) {
			t.Errorf("%s: expected ErrSyntax with trailing data, got %v", name(c), err)
		}
		if err := c.DecodeFrom(bytes.NewReader(data), &dst); !errors.Is(err, ErrSyntax) {
			t.Errorf("%s stream: expected ErrSyntax with trailing data, got %v", name(c), err)
		}
	}
}

func TestInvalidTarget(t *testing.T) {
	for _, c := range allCodecs(Options{}) {
		var a Address
		for _, target := range []any{a, nil, (*Address)(nil)} {
			if err := c.Decode([]byte("{}"), target); !errors.Is(err, ErrInvalidTarget) {
				t.Errorf("%s: expected ErrInvalidTarget for %T, got %v", name(c), target, err)
			}
		}
	}
}

func TestInconsistentOmitempty(t *testing.T) {
	type bad struct {
		A string `json:"a,omitempty" xml:"a"`
	}
	for _, c := range allCodecs(Options{}) {
		if _, err := c.Encode(bad{}); !errors.Is(err, ErrInvalidTags) {
			t.Errorf("%s: expected ErrInvalidTags, got %v", name(c), err)
		}
	}
}

func TestUnsupportedType(t *testing.T) {
	type bad struct {
		C chan int `json:"c"`
	}
	for _, c := range allCodecs(Options{}) {
		if _, err := c.Encode(bad{C: make(chan int)}); !errors.Is(err, ErrUnsupportedType) {
			t.Errorf("%s: expected ErrUnsupportedType, got %v", name(c), err)
		}
	}
}

// ------------------------------------------------------------ XML

func TestXMLRejectsDTD(t *testing.T) {
	payloads := []string{
		`<?xml version="1.0"?><!DOCTYPE a [<!ENTITY xxe SYSTEM "file:///etc/passwd">]><a><street>&xxe;</street></a>`,
		`<!DOCTYPE a [<!ENTITY b "bbbbbbbb"><!ENTITY c "&b;&b;&b;&b;">]><a><street>&c;</street></a>`,
		`<!DOCTYPE a SYSTEM "http://example.com/evil.dtd"><a/>`,
	}
	for _, p := range payloads {
		var dst Address
		if err := NewXML(Options{}).Decode([]byte(p), &dst); !errors.Is(err, ErrDTDNotAllowed) {
			t.Errorf("expected ErrDTDNotAllowed, got %v for %s", err, p)
		}
	}
}

func TestXMLNeverExpandsEntitiesEvenWithDTDAllowed(t *testing.T) {
	p := `<!DOCTYPE a [<!ENTITY xxe SYSTEM "file:///etc/passwd">]><a><street>&xxe;</street></a>`
	var dst Address
	err := NewXML(Options{AllowDTD: true}).Decode([]byte(p), &dst)
	if !errors.Is(err, ErrSyntax) {
		t.Fatalf("expected syntax error (unknown entity), got %v", err)
	}
	if dst.Street != "" {
		t.Fatalf("entidade foi expandida: %q", dst.Street)
	}
}

func TestXMLNamespace(t *testing.T) {
	type NS struct {
		XMLName xml.Name `json:"-" cbor:"-" xml:"urn:pedido pedido"`
		ID      string   `json:"id"`
	}
	c := NewXML(Options{})
	data, err := c.Encode(NS{ID: "1"})
	if err != nil {
		t.Fatal(err)
	}
	var ok NS
	if err := c.Decode(data, &ok); err != nil || ok.ID != "1" {
		t.Fatalf("round-trip with namespace: %v %+v (%s)", err, ok, data)
	}

	cases := []string{
		`<pedido xmlns="urn:outro"><id>1</id></pedido>`,                // root in the wrong namespace
		`<pedido xmlns="urn:pedido"><id xmlns="urn:x">1</id></pedido>`, // child in the wrong namespace
		`<pedido><id>1</id></pedido>`,                                  // no namespace
	}
	for _, p := range cases {
		var dst NS
		if err := c.Decode([]byte(p), &dst); !errors.Is(err, ErrNamespace) {
			t.Errorf("expected ErrNamespace, got %v for %s", err, p)
		}
	}

	// Type without a declared namespace receiving a payload with a namespace.
	var addr Address
	if err := c.Decode([]byte(`<Address xmlns="urn:x"><city>a</city></Address>`), &addr); !errors.Is(err, ErrNamespace) {
		t.Errorf("expected ErrNamespace, got %v", err)
	}
}

func TestXMLWrongRoot(t *testing.T) {
	var o Order
	if err := NewXML(Options{}).Decode([]byte(`<pedido id="1"/>`), &o); !errors.Is(err, ErrTypeMismatch) {
		t.Fatalf("expected ErrTypeMismatch for wrong root, got %v", err)
	}
}

// ------------------------------------------------------------ registry

type fakeCodec struct{ encoded, decoded int }

func (f *fakeCodec) ContentType() string { return "application/x-fake" }
func (f *fakeCodec) Encode(v any) ([]byte, error) {
	f.encoded++
	return []byte("fake"), nil
}
func (f *fakeCodec) Decode(data []byte, v any) error {
	f.decoded++
	*(v.(*string)) = string(data)
	return nil
}

func TestRegistryWithFakeCodec(t *testing.T) {
	fake := &fakeCodec{}
	r, err := New(Config{Formats: []string{"json", "application/x-fake"}, MaxPayloadBytes: 10}, fake)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Formats(); !reflect.DeepEqual(got, []string{JSON, "application/x-fake"}) {
		t.Fatalf("Formats: %v", got)
	}
	if b, err := r.EncodeAs("application/x-fake; charset=utf-8", 1); err != nil || string(b) != "fake" {
		t.Fatalf("EncodeAs: %q %v", b, err)
	}
	var s string
	if err := r.DecodeFrom("x-fake", strings.NewReader("oi"), &s); err != nil || s != "oi" {
		t.Fatalf("DecodeFrom via buffer: %q %v", s, err)
	}
	if fake.encoded != 1 || fake.decoded != 1 {
		t.Fatalf("fake was not used: %+v", fake)
	}
	if err := r.DecodeFrom("x-fake", strings.NewReader("12345678901"), &s); !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("expected ErrPayloadTooLarge, got %v", err)
	}
}

func TestRegistryUnsupportedFormat(t *testing.T) {
	r, err := New(Config{MaxPayloadBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	var a Address
	if _, err := r.EncodeAs("xml", a); !errors.Is(err, ErrUnsupportedFormat) {
		t.Errorf("xml not enabled: expected ErrUnsupportedFormat, got %v", err)
	}
	if err := r.DecodeAs("yaml", nil, &a); !errors.Is(err, ErrUnsupportedFormat) {
		t.Errorf("expected ErrUnsupportedFormat, got %v", err)
	}
	if _, err := New(Config{Formats: []string{"yaml"}, MaxPayloadBytes: 1}); !errors.Is(err, ErrUnsupportedFormat) {
		t.Errorf("enabled format without codec should fail: %v", err)
	}
	if _, err := New(Config{Default: "xml", MaxPayloadBytes: 1}); !errors.Is(err, ErrUnsupportedFormat) {
		t.Errorf("default not enabled should fail: %v", err)
	}
	if _, err := New(Config{MaxPayloadBytes: -1}); err == nil {
		t.Error("negative MaxPayloadBytes should fail")
	}
	if r, err := New(Config{}); err != nil || r.maxPayload != DefaultMaxPayloadBytes {
		t.Errorf("zero MaxPayloadBytes should use the default: %v", err)
	}
}

func TestRegistryDefaultFormat(t *testing.T) {
	r, _ := New(Config{Formats: []string{"json", "xml"}, Default: "xml", MaxPayloadBytes: 1024})
	b, err := r.EncodeAs("", Address{City: "x"})
	if err != nil || !strings.HasPrefix(string(b), xml.Header+"<Address>") {
		t.Fatalf("empty format should use the default xml: %s %v", b, err)
	}
}

func TestRegistryPayloadLimit(t *testing.T) {
	r, _ := New(Config{Formats: []string{"json", "cbor", "xml"}, MaxPayloadBytes: 64})
	big := Address{Street: strings.Repeat("x", 100)}
	for _, f := range r.Formats() {
		data, err := r.EncodeAs(f, big)
		if err != nil {
			t.Fatal(err)
		}
		var dst Address
		if err := r.DecodeAs(f, data, &dst); !errors.Is(err, ErrPayloadTooLarge) {
			t.Errorf("%s DecodeAs: expected ErrPayloadTooLarge, got %v", f, err)
		}
		if err := r.DecodeFrom(f, bytes.NewReader(data), &dst); !errors.Is(err, ErrPayloadTooLarge) {
			t.Errorf("%s DecodeFrom: expected ErrPayloadTooLarge, got %v", f, err)
		}
	}
	// Exactly at the limit passes.
	exact, _ := r.EncodeAs("json", Address{Street: "a"})
	r2, _ := New(Config{MaxPayloadBytes: int64(len(exact))})
	var dst Address
	if err := r2.DecodeFrom("json", bytes.NewReader(exact), &dst); err != nil {
		t.Fatalf("payload at the exact limit failed: %v", err)
	}
}

type validated struct {
	Age int `json:"age"`
}

func (v *validated) Validate() error {
	if v.Age < 18 {
		return errors.New("underage")
	}
	return nil
}

func TestRegistryValidation(t *testing.T) {
	r, _ := New(Config{MaxPayloadBytes: 1024})
	var v validated
	err := r.DecodeAs("json", []byte(`{"age":10}`), &v)
	if !errors.Is(err, ErrValidation) || v.Age != 10 {
		t.Fatalf("expected ErrValidation with decoded value, got %v (%+v)", err, v)
	}
	if err := r.DecodeAs("json", []byte(`{"age":20}`), &v); err != nil {
		t.Fatal(err)
	}

	hookErr := errors.New("hook")
	r2, _ := New(Config{MaxPayloadBytes: 1024, Validate: func(any) error { return hookErr }})
	var a Address
	if err := r2.DecodeAs("json", []byte(`{}`), &a); !errors.Is(err, hookErr) || !errors.Is(err, ErrValidation) {
		t.Fatalf("validation hook: %v", err)
	}
}

// ------------------------------------------------------------ benchmarks

func BenchmarkEncode(b *testing.B) {
	o := referenceOrder()
	for _, c := range allCodecs(Options{}) {
		b.Run(name(c), func(b *testing.B) {
			for b.Loop() {
				if _, err := c.Encode(o); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkDecode(b *testing.B) {
	o := referenceOrder()
	for _, c := range allCodecs(Options{}) {
		data, _ := c.Encode(o)
		b.Run(name(c), func(b *testing.B) {
			for b.Loop() {
				var dst Order
				if err := c.Decode(data, &dst); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
