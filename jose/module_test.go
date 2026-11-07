package jose_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ams-soft/nforge/app"
	"github.com/ams-soft/nforge/internal/apptest"
	"github.com/ams-soft/nforge/jose"
	"github.com/ams-soft/nforge/module"
)

const secretYAML = `
jose:
  enabled: true
  keys:
    - kid: jwt
      secret: ${NF_TEST_JWT_SECRET}
  profiles:
    - name: access
      kind: jwt_signed
      typ: at+jwt
      signing_key: jwt
      verification_keys: [jwt]
      jwt: {issuer: https://issuer.test, audiences: [api], clock_skew: 0s}
`

func readJOSE(got *jose.Config) module.Descriptor {
	return module.New("reader", module.Definition{Optional: []module.Handle{jose.Capability}, Check: func(v *module.View) error {
		cfg, _, _, err := module.ConfigOf[jose.Config](v, jose.Capability)
		*got = cfg
		return err
	}})
}

func TestModuleLoadsAndStarts(t *testing.T) {
	t.Setenv("NF_TEST_JWT_SECRET", "0123456789abcdef0123456789abcdef")
	var cfg jose.Config
	a := apptest.Start(t, secretYAML, jose.Module(), readJOSE(&cfg))
	if k := cfg.Keys[0]; k.Algorithm != "HS256" || k.Secret != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("key = %v", k)
	}
	p := cfg.Profiles[0]
	if p.SigningAlgorithm != "HS256" || p.JWT.TTL != 15*time.Minute || p.JWT.ClockSkew == nil || *p.JWT.ClockSkew != 0 {
		t.Fatalf("profile = %+v", p)
	}
	if cfg.Limits.MaxTokenBytes != 16384 || cfg.JWKS.Path != "/.well-known/jwks.json" || *cfg.JWKS.CacheMaxAge != time.Minute {
		t.Fatalf("defaults: %+v %+v", cfg.Limits, cfg.JWKS)
	}
	svc, ok := app.Value(a, jose.Capability)
	if !ok || svc.Check(context.Background()) != nil || svc.JWKS().Path != "/.well-known/jwks.json" {
		t.Fatal("service not published")
	}
	if err := a.Reload(context.Background()); err != nil {
		t.Fatalf("reload: %v", err)
	}
}

func TestModuleDisabledByDefault(t *testing.T) {
	var cfg jose.Config
	a := apptest.Start(t, "", jose.Module(), readJOSE(&cfg))
	if cfg.Enabled || cfg.Limits.MaxTokenBytes != 0 {
		t.Fatalf("jose = %+v", cfg)
	}
	if _, ok := app.Value(a, jose.Capability); ok {
		t.Fatal("a disabled jose publishes nothing")
	}
}

func TestModuleValidatesAtConfiguration(t *testing.T) {
	t.Setenv("NF_TEST_JWT_SECRET", "0123456789abcdef0123456789abcdef")
	cases := map[string]struct{ from, to, want string }{
		"no issuer":     {"issuer: https://issuer.test", "issuer: ''", "issuer"},
		"bad algorithm": {"secret: ${NF_TEST_JWT_SECRET}", "secret: ${NF_TEST_JWT_SECRET}\n      algorithm: none", "algorithm"},
		"typo":          {"typ: at+jwt", "typ: at+jwt\n      publsh: true", "publsh"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			text := strings.Replace(secretYAML, tc.from, tc.to, 1)
			if text == secretYAML {
				t.Fatal("replacement did not apply")
			}
			if _, err := apptest.New(t, text, jose.Module()); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v", err)
			}
		})
	}
	t.Setenv("NF_TEST_JWT_SECRET", "")
	if _, err := apptest.New(t, secretYAML, jose.Module()); err == nil || !strings.Contains(err.Error(), "secret or source is required") {
		t.Fatalf("unset secret must fail clearly: %v", err)
	}
}

func TestModuleEnvFile(t *testing.T) {
	t.Setenv("NF_TEST_JWT_SECRET", "0123456789abcdef0123456789abcdef")
	var cfg jose.Config
	d := apptest.Load(t, secretYAML, "JOSE_LIMITS_MAX_TOKEN_BYTES=4096\nJOSE_LIMITS_MAX_PAYLOAD_BYTES=2048\nJOSE_JWKS_CACHE_MAX_AGE=10s\n")
	if _, err := app.New(d, app.Modules(jose.Module(), readJOSE(&cfg)), apptest.Quiet()); err != nil {
		t.Fatal(err)
	}
	if cfg.Limits.MaxTokenBytes != 4096 || *cfg.JWKS.CacheMaxAge != 10*time.Second {
		t.Fatalf("env overrides not applied: %+v", cfg.Limits)
	}
	d = apptest.Load(t, secretYAML, "JOSE_KEYS=x\n")
	if _, err := app.New(d, app.Modules(jose.Module()), apptest.Quiet()); err == nil || !strings.Contains(err.Error(), "jose.keys") {
		t.Fatalf("JOSE_KEYS must be rejected explicitly: %v", err)
	}
}

// A missing key file fails the start with a sanitized error.
func TestModuleStartFailureIsSanitized(t *testing.T) {
	yaml := `
jose:
  enabled: true
  keys:
    - {kid: k, algorithm: EdDSA, source: {format: pem, file: /nonexistent/secret-path/key.pem}}
  profiles:
    - {name: access, kind: jwt_signed, typ: at+jwt, verification_keys: [k], jwt: {issuer: https://i, audiences: [a]}}
`
	a, err := apptest.New(t, yaml, jose.Module())
	if err != nil {
		t.Fatal(err)
	}
	err = a.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "unable to setup jose") || strings.Contains(err.Error(), "secret-path") {
		t.Fatalf("err = %v", err)
	}
}
