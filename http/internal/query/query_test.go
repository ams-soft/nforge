package query

import (
	"errors"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ams-soft/nforge/codec"
)

type listQuery struct {
	Page  int           `query:"page"`
	Wait  time.Duration `query:"wait"`
	Since time.Time     `query:"since"`
	Tags  []string      `query:"tag"`
}

// A type error reaches the client in the 400 detail: it must describe the
// parameter in payload terms, not with a Go type name.
func TestTypeErrorsDoNotNameGoTypes(t *testing.T) {
	cases := map[string]string{
		"page=abc":   "expects integer",
		"wait=soon":  "expects duration",
		"since=when": "expects string (RFC 3339",
	}
	for raw, want := range cases {
		vals, _ := url.ParseQuery(raw)
		_, err := Decode(vals, reflect.TypeOf(listQuery{}), false)
		var te *codec.TypeError
		if !errors.As(err, &te) {
			t.Fatalf("%s: want a TypeError, got %v", raw, err)
		}
		msg := err.Error()
		if !strings.Contains(msg, want) {
			t.Errorf("%s: %q must contain %q", raw, msg, want)
		}
		for _, leak := range []string{"time.", "int", "query."} {
			if leak == "int" && strings.Contains(want, "integer") {
				continue
			}
			if strings.Contains(msg, leak) {
				t.Errorf("%s: %q leaks %q", raw, msg, leak)
			}
		}
		if te.Expected == "" {
			t.Errorf("%s: Expected keeps the Go type for logs", raw)
		}
	}
}

// ParseFloat accepts NaN and Inf spellings; a query float must be finite.
func TestFloatRejectsNonFinite(t *testing.T) {
	type q struct {
		Score float64 `query:"score"`
	}
	for _, in := range []string{"NaN", "nan", "Inf", "+Inf", "-inf", "infinity", "1e999"} {
		_, err := Decode(url.Values{"score": {in}}, reflect.TypeOf(q{}), false)
		var te *codec.TypeError
		if !errors.As(err, &te) {
			t.Errorf("score=%s: want a TypeError, got %v", in, err)
		}
	}
	ptr, err := Decode(url.Values{"score": {"1.5"}}, reflect.TypeOf(q{}), false)
	if err != nil || ptr.Interface().(*q).Score != 1.5 {
		t.Fatalf("finite value: %v", err)
	}
}

// A scalar sent twice is refused; slices, empty values and one value are fine.
func TestRepeatedScalarIsRefused(t *testing.T) {
	type q struct {
		Page int      `query:"page"`
		Opt  *int     `query:"opt"`
		Tags []string `query:"tag"`
		TS   *[]int   `query:"n"`
	}
	typ := reflect.TypeOf(q{})
	for _, raw := range []string{"page=1&page=2", "page=1&page=1", "opt=1&opt=2"} {
		vals, _ := url.ParseQuery(raw)
		_, err := Decode(vals, typ, false)
		var re *RepeatedError
		if !errors.As(err, &re) {
			t.Errorf("%s: want RepeatedError, got %v", raw, err)
		}
	}
	for _, raw := range []string{"page=1", "page=&page=2", "tag=a&tag=b", "tag=a,b&tag=c", "n=1&n=2"} {
		vals, _ := url.ParseQuery(raw)
		if _, err := Decode(vals, typ, false); err != nil {
			t.Errorf("%s: %v", raw, err)
		}
	}
}
