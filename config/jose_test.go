package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const joseYAML = `
app:
  name: test
  version: 1.0.0
http:
  server:
    port: 8080
  auth:
    default_profile: access
jose:
  enabled: true
  keys:
    - kid: access-sign
      algorithm: EdDSA
      operations: [sign, verify]
      source:
        format: pem
        file: ${NF_TEST_JOSE_KEY}
      publish: true
  profiles:
    - name: access
      kind: jwt_signed
      typ: at+jwt
      signing_algorithm: EdDSA
      signing_key: access-sign
      verification_algorithms: [EdDSA]
      verification_keys: [access-sign]
      jwt:
        issuer: https://issuer.test
        audiences: [api]
        clock_skew: 0s
  jwks:
    enabled: true
`

func writeConfig(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestJOSEConfigLoad(t *testing.T) {
	t.Setenv("NF_TEST_JOSE_KEY", "/run/secrets/access.pem")
	cfg, err := LoadFlags(writeConfig(t, joseYAML), "")
	if err != nil {
		t.Fatal(err)
	}
	j := cfg.JOSE
	if !j.Enabled || len(j.Keys) != 1 || j.Keys[0].Source.File != "/run/secrets/access.pem" || !j.Keys[0].Publish {
		t.Fatalf("keys = %+v", j.Keys)
	}
	p := j.Profiles[0]
	if p.JWT.TTL != 15*time.Minute || p.JWT.ClockSkew == nil || *p.JWT.ClockSkew != 0 {
		t.Fatalf("jwt defaults: %+v", p.JWT)
	}
	if j.Limits.MaxTokenBytes != 16384 || j.JWKS.Path != "/.well-known/jwks.json" || *j.JWKS.CacheMaxAge != time.Minute {
		t.Fatalf("defaults: %+v %+v", j.Limits, j.JWKS)
	}
	if cfg.HTTP.Auth.Realm != "nforge-api" || cfg.HTTP.Auth.DefaultProfile != "access" {
		t.Fatalf("auth = %+v", cfg.HTTP.Auth)
	}
}

func TestJOSEUnknownKeysRejected(t *testing.T) {
	t.Setenv("NF_TEST_JOSE_KEY", "/run/secrets/access.pem")
	text := strings.Replace(joseYAML, "      publish: true", "      publsh: true", 1)
	if _, err := LoadFlags(writeConfig(t, text), ""); err == nil || !strings.Contains(err.Error(), "publsh") {
		t.Fatalf("typo inside jose must fail: %v", err)
	}
	// outside jose, unknown keys keep being ignored
	text = joseYAML + "\nunknown_section:\n  a: 1\n"
	if _, err := LoadFlags(writeConfig(t, text), ""); err != nil {
		t.Fatalf("unknown keys outside jose: %v", err)
	}
}

func TestJOSEDisabledByDefault(t *testing.T) {
	cfg, err := LoadFlags(writeConfig(t, "app: {name: a, version: '1'}\nhttp: {server: {port: 1}}\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.JOSE.Enabled || cfg.JOSE.Limits.MaxTokenBytes != 0 {
		t.Fatalf("jose = %+v", cfg.JOSE)
	}
}

func TestJOSEValidationAtLoad(t *testing.T) {
	t.Setenv("NF_TEST_JOSE_KEY", "/run/secrets/access.pem")
	cases := map[string]struct{ from, to string }{
		"no issuer":          {"issuer: https://issuer.test", "issuer: ''"},
		"unknown default":    {"default_profile: access", "default_profile: other"},
		"realm with quote":   {"default_profile: access", "default_profile: access\n    realm: 'a\"b'"},
		"bad algorithm":      {"algorithm: EdDSA", "algorithm: none"},
		"default needs jose": {"  enabled: true\n  keys:", "  enabled: false\n  keys:"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			text := strings.Replace(joseYAML, tc.from, tc.to, 1)
			if text == joseYAML {
				t.Fatal("replacement did not apply")
			}
			if _, err := LoadFlags(writeConfig(t, text), ""); err == nil {
				t.Fatal("expected a validation error")
			}
		})
	}
}

func TestJOSEEnvFile(t *testing.T) {
	t.Setenv("NF_TEST_JOSE_KEY", "/run/secrets/access.pem")
	env := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(env, []byte("JOSE_LIMITS_MAX_TOKEN_BYTES=4096\nJOSE_LIMITS_MAX_PAYLOAD_BYTES=2048\nJOSE_JWKS_CACHE_MAX_AGE=10s\nHTTP_AUTH_REALM=other\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFlags(writeConfig(t, joseYAML), env)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.JOSE.Limits.MaxTokenBytes != 4096 || *cfg.JOSE.JWKS.CacheMaxAge != 10*time.Second || cfg.HTTP.Auth.Realm != "other" {
		t.Fatalf("env overrides not applied: %+v %v %q", cfg.JOSE.Limits, *cfg.JOSE.JWKS.CacheMaxAge, cfg.HTTP.Auth.Realm)
	}

	// sequences of structs are YAML-only
	if err := os.WriteFile(env, []byte("JOSE_KEYS=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFlags(writeConfig(t, joseYAML), env); err == nil || !strings.Contains(err.Error(), "jose.keys") {
		t.Fatalf("JOSE_KEYS must be rejected explicitly: %v", err)
	}
}

func TestJOSEInlineSecretFromEnv(t *testing.T) {
	text := `
app: {name: a, version: '1'}
http: {server: {port: 8080}, auth: {default_profile: access}}
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
      jwt: {issuer: https://issuer.test, audiences: [api]}
`
	t.Setenv("NF_TEST_JWT_SECRET", "0123456789abcdef0123456789abcdef")
	cfg, err := LoadFlags(writeConfig(t, text), "")
	if err != nil {
		t.Fatal(err)
	}
	if k := cfg.JOSE.Keys[0]; k.Algorithm != "HS256" || k.Secret != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("key = %v", k)
	}
	if p := cfg.JOSE.Profiles[0]; p.SigningAlgorithm != "HS256" {
		t.Fatalf("profile algorithm = %q", p.SigningAlgorithm)
	}

	t.Setenv("NF_TEST_JWT_SECRET", "")
	if _, err := LoadFlags(writeConfig(t, text), ""); err == nil || !strings.Contains(err.Error(), "secret or source is required") {
		t.Fatalf("unset secret must fail clearly: %v", err)
	}
}
