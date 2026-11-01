package database

import (
	"fmt"
	"strings"
	"testing"
)

func TestRedactDSN(t *testing.T) {
	cases := []struct {
		dsn      string
		mustHide []string
	}{
		{"postgres://u:pw1@h/db", []string{"pw1"}},
		{"postgres://u@h/db?password=pw2&sslmode=disable", []string{"pw2"}},
		{"postgres://u@h/db?sslpassword=pw3&sslkey=/etc/key.pem", []string{"pw3"}},
		{"postgres://u:pw4@h/db?password=pw5&sslpassword=pw6", []string{"pw4", "pw5", "pw6"}},
		{"host=h user=u password=pw7 dbname=db", []string{"pw7"}},
		{"postgres://u:p%40ss@h/db", []string{"p%40ss", "p@ss"}},
		{"", nil},
	}
	for _, c := range cases {
		got := redactDSN(c.dsn)
		for _, secret := range c.mustHide {
			if strings.Contains(got, secret) {
				t.Errorf("redactDSN(%q) = %q still shows %q", c.dsn, got, secret)
			}
		}
	}
	if got := redactDSN("postgres://u@h/db?sslmode=disable&application_name=api"); !strings.Contains(got, "sslmode=disable") || !strings.Contains(got, "application_name=api") {
		t.Errorf("non-secret parameters were dropped: %q", got)
	}
}

func TestConfigFormatRedactsEveryVerb(t *testing.T) {
	cfg := Config{Password: "S3CRET-PW", DSN: "postgres://u:S3CRET-DSN@h/db?password=S3CRET-QP", Host: "h"}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
		out := fmt.Sprintf(verb, cfg)
		for _, secret := range []string{"S3CRET-PW", "S3CRET-DSN", "S3CRET-QP"} {
			if strings.Contains(out, secret) {
				t.Errorf("%s leaks %s: %s", verb, secret, out)
			}
		}
	}
	if cfg.Password != "S3CRET-PW" {
		t.Error("Format mutated the receiver")
	}
}
