package codec

import (
	"fmt"
	"reflect"
	"strconv"
	"testing"
	"time"
)

// The streaming JSON presence check must report exactly what the tree-based
// reference reports, for every payload and target type.

type pAddress struct {
	Zip  string `json:"zip" codec:"required"`
	City string `json:"city"`
}

type pItem struct {
	ID   int64     `json:"id"`
	Name string    `json:"name" codec:"required"`
	Addr *pAddress `json:"addr"`
}

type pEmbedded struct {
	Code string `json:"code" codec:"required"`
}

type pTarget struct {
	pEmbedded
	Name    string              `json:"name" codec:"required"`
	Inner   pAddress            `json:"inner"`
	Items   []pItem             `json:"items"`
	ByKey   map[string]pItem    `json:"by_key"`
	Ptrs    []*pAddress         `json:"ptrs"`
	Matrix  [][]pItem           `json:"matrix"`
	When    time.Time           `json:"when"`
	Raw     []byte              `json:"raw"`
	Any     any                 `json:"any"`
	Plain   map[string]string   `json:"plain"`
	Nested  map[string][]pItem  `json:"nested"`
	Fixed   [2]pAddress         `json:"fixed"`
	Opt     *pItem              `json:"opt"`
	NoReq   struct{ A, B int }  `json:"no_req"`
	Mixed   map[string]any      `json:"mixed"`
	Strings []string            `json:"strings"`
	Deep    map[string]pTarget2 `json:"deep"`
}

type pTarget2 struct {
	Level pItem `json:"level"`
}

// pWide has more than 64 fields, so the walker uses the large bitset.
var pWide = func() reflect.Type {
	fields := make([]reflect.StructField, 70)
	for i := range fields {
		tag := `json:"f` + strconv.Itoa(i) + `"`
		if i%10 == 9 {
			tag += ` codec:"required"`
		}
		fields[i] = reflect.StructField{Name: "F" + strconv.Itoa(i), Type: reflect.TypeFor[string](), Tag: reflect.StructTag(tag)}
	}
	return reflect.StructOf(fields)
}()

var presenceTargets = []reflect.Type{
	reflect.TypeFor[pTarget](),
	reflect.TypeFor[[]pItem](),
	reflect.TypeFor[map[string]pAddress](),
	reflect.TypeFor[*pItem](),
	reflect.TypeFor[strictTarget](),
	pWide,
}

var presenceCorpus = []string{
	`{"code":"c","name":"a"}`,
	`{"code":"c"}`,
	`{"name":"a"}`,
	`{}`,
	`null`,
	`[]`,
	`"str"`,
	`42`,
	`{"code":"c","name":null}`,
	`{"code":"c","name":"a","inner":{}}`,
	`{"code":"c","name":"a","inner":{"zip":null}}`,
	`{"code":"c","name":"a","inner":{"zip":"1","bogus":1}}`,
	`{"code":"c","name":"a","bogus":true,"inner":{"zip":"1"}}`,
	`{"zzz":1,"aaa":2,"code":"c","name":"a"}`,
	`{"inner":{},"items":[{}],"code":"c","name":"a"}`,
	`{"items":[{"name":"x"},{"id":2},{"id":3}],"code":"c","name":"a"}`,
	`{"items":[{"name":"x","addr":{}}],"code":"c","name":"a"}`,
	`{"by_key":{"b":{},"a":{}},"code":"c","name":"a"}`,
	`{"by_key":{"":{}},"code":"c","name":"a"}`,
	`{"nested":{"k":[{"name":"n"},{}]},"code":"c","name":"a"}`,
	`{"matrix":[[{"name":"n"}],[{"id":1}]],"code":"c","name":"a"}`,
	`{"ptrs":[null,{}],"code":"c","name":"a"}`,
	`{"fixed":[{"zip":"1"},{}],"code":"c","name":"a"}`,
	`{"opt":{"id":1},"code":"c","name":"a"}`,
	`{"opt":null,"code":"c","name":"a"}`,
	`{"when":"2026-01-01T00:00:00Z","raw":"AQI=","any":{"x":{}},"code":"c","name":"a"}`,
	`{"plain":{"a":"b"},"mixed":{"q":[1,{"z":null}]},"code":"c","name":"a"}`,
	`{"no_req":{"A":1,"C":2},"code":"c","name":"a"}`,
	`{"deep":{"k":{"level":{}}},"code":"c","name":"a"}`,
	`{"strings":["a","b"],"code":"c","name":"a"}`,
	`{"inner":"wrong type","items":{"not":"array"},"code":"c","name":"a"}`,
	`{"Name":"a","CODE":"c"}`,
	`{"NAME":null,"code":"c"}`,
	`{"name":"a","Name":"b","code":"c"}`,
	`{"name":"a","name":null,"code":"c"}`,
	`{"name":null,"name":"a","code":"c"}`,
	`{"n\u0061me":"a","code":"c"}`,
	`{"code":"c","name":"a"} trailing`,
	`{"code":"c","name":"a"}{}`,
	`{"code":"c","name":`,
	`{"code":"c",}`,
	``,
	"{\"na\xffme\":\"a\",\"code\":\"c\"}",
	"{\"name\":\"a\xff\",\"code\":\"c\"}",
	`[{"name":"a"},{"id":1},{}]`,
	`[{"name":"a"},null]`,
	`{"b":{},"a":{"zip":"1"},"c":{"zip":null}}`,
	`{"zip":"1"}`,
	`{"inner":{"other":"x"},"name":"a"}`,
	`{"f9":"x","f19":"x","f29":"x","f39":"x","f49":"x","f59":"x","f69":"x"}`,
	`{"f9":"x","f19":"x","f29":"x","f39":"x","f49":"x","f59":"x"}`,
	`{"f69":"x","f68":"y"}`,
}

func presenceResult(err error) string {
	if err == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%T: %v", err, err)
}

func checkPresenceEquivalence(t *testing.T, doc []byte) {
	t.Helper()
	for _, typ := range presenceTargets {
		for _, strict := range []bool{false, true} {
			if !strict && !hasRequired(typ) {
				continue // Decode does not run the check
			}
			want := presenceResult(checkPresenceTree(doc, typ, strict))
			got := presenceResult(checkPresenceJSON(doc, typ, strict))
			if got != want {
				t.Errorf("type %v strict=%v doc %q:\n got  %s\n want %s", typ, strict, doc, got, want)
			}
		}
	}
}

func TestPresenceJSONMatchesTree(t *testing.T) {
	for _, doc := range presenceCorpus {
		checkPresenceEquivalence(t, []byte(doc))
	}
}

func FuzzPresenceJSON(f *testing.F) {
	for _, doc := range presenceCorpus {
		f.Add([]byte(doc))
	}
	f.Fuzz(func(t *testing.T, doc []byte) {
		checkPresenceEquivalence(t, doc)
	})
}

func TestPresenceJSONAllocations(t *testing.T) {
	var b []byte
	b = append(b, `{"code":"c","name":"a","inner":{"zip":"1"},"items":[`...)
	for i := range 100 {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, `{"id":1,"name":"n","addr":{"zip":"z","city":"c"}}`...)
	}
	b = append(b, `],"plain":{"a":"b"}}`...)
	typ := reflect.TypeFor[pTarget]()
	if err := checkPresenceJSON(b, typ, false); err != nil {
		t.Fatal(err)
	}
	if raceEnabled {
		t.Skip("the race detector drops sync.Pool items on purpose")
	}
	// Independent of the payload size: no per-value allocations.
	if n := testing.AllocsPerRun(100, func() { _ = checkPresenceJSON(b, typ, false) }); n > 2 {
		t.Errorf("checkPresenceJSON allocates %v times per run, want <= 2", n)
	}
}
