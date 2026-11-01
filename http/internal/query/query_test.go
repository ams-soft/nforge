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
// parameter in payload terms, not with a Go type name (G-06).
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
