package jose

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestDisabledDoesNoIO(t *testing.T) {
	cfg := Config{Keys: []KeyConfig{{KID: "k", Source: KeySourceConfig{Format: "pem", File: "/nonexistent/key.pem"}}}}
	_, err := New(context.Background(), cfg)
	mustIs(t, err, ErrDisabled)
	ApplyDefaults(&cfg)
	if cfg.Limits.MaxTokenBytes != 0 || cfg.JWKS.Path != "" {
		t.Fatalf("defaults must not apply when disabled: %+v", cfg)
	}
	if err := ValidateConfig(cfg); err != nil {
		t.Fatalf("disabled config must be valid: %v", err)
	}
}

func TestApplyDefaults(t *testing.T) {
	cfg := testConfig(t)
	zero := time.Duration(0)
	cfg.Profiles[7].JWT.ClockSkew = &zero // access: explicit 0s must be kept
	ApplyDefaults(&cfg)

	l := cfg.Limits
	if l.MaxTokenBytes != 16384 || l.MaxHeaderBytes != 2048 || l.MaxPayloadBytes != 8192 ||
		l.MaxJSONDepth != 16 || l.MaxKeyFileBytes != 65536 || l.MaxProfiles != 32 || l.MaxKeys != 64 {
		t.Errorf("limits = %+v", l)
	}
	access := cfg.Profiles[7].JWT
	if access.TTL != 15*time.Minute || access.MaxTTL != 15*time.Minute || *access.ClockSkew != 0 {
		t.Errorf("access jwt = %+v (skew %v)", access, *access.ClockSkew)
	}
	refresh := cfg.Profiles[8].JWT // only ttl set: max_ttl follows it
	if refresh.TTL != time.Hour || refresh.MaxTTL != time.Hour || *refresh.ClockSkew != 30*time.Second {
		t.Errorf("refresh jwt = %+v", refresh)
	}
	if cfg.Profiles[5].ContentEncryption != A256GCM || cfg.Profiles[0].ContentEncryption != "" {
		t.Errorf("content encryption defaults wrong")
	}
	if cfg.JWKS.Enabled || cfg.JWKS.Path != "/.well-known/jwks.json" || *cfg.JWKS.CacheMaxAge != time.Minute {
		t.Errorf("jwks = %+v", cfg.JWKS)
	}

	only := Config{Enabled: true, Profiles: []ProfileConfig{{Kind: KindJWTSigned, JWT: JWTConfig{MaxTTL: 5 * time.Minute}}}}
	ApplyDefaults(&only)
	if j := only.Profiles[0].JWT; j.TTL != 5*time.Minute || j.MaxTTL != 5*time.Minute {
		t.Errorf("only max_ttl: %+v", j)
	}
}

func TestValidateConfigErrors(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
		field  string
	}{
		{"no keys", func(c *Config) { c.Keys = nil }, "jose.keys"},
		{"no profiles", func(c *Config) { c.Profiles = nil }, "jose.profiles"},
		{"bad kid", func(c *Config) { c.Keys[0].KID = "bad kid" }, "jose.keys[0].kid"},
		{"duplicate kid", func(c *Config) { c.Keys[1].KID = c.Keys[0].KID }, "jose.keys[1].kid"},
		{"none alg", func(c *Config) { c.Keys[0].Algorithm = "none" }, "jose.keys[0].algorithm"},
		{"RSA1_5", func(c *Config) { c.Keys[5].Algorithm = "RSA1_5" }, "jose.keys[5].algorithm"},
		{"PBES2", func(c *Config) { c.Keys[6].Algorithm = "PBES2-HS256+A128KW" }, "jose.keys[6].algorithm"},
		{"op mismatch", func(c *Config) { c.Keys[0].Operations = []string{OpEncrypt} }, "jose.keys[0].operations"},
		{"dup op", func(c *Config) { c.Keys[0].Operations = []string{OpSign, OpSign} }, "jose.keys[0].operations"},
		{"publish oct", func(c *Config) { c.Keys[4].Publish = true }, "jose.keys[4].publish"},
		{"pem secret", func(c *Config) { c.Keys[4].Source.Format = "pem" }, "jose.keys[4].source.format"},
		{"oct asymmetric", func(c *Config) { c.Keys[0].Source.Format = "oct" }, "jose.keys[0].source.format"},
		{"bad format", func(c *Config) { c.Keys[0].Source.Format = "der" }, "jose.keys[0].source.format"},
		{"no file", func(c *Config) { c.Keys[0].Source.File = "" }, "jose.keys[0].source.file"},
		{"bad kind", func(c *Config) { c.Profiles[0].Kind = "jwt" }, "jose.profiles[0].kind"},
		{"no typ", func(c *Config) { c.Profiles[0].Typ = "" }, "jose.profiles[0].typ"},
		{"typ with space", func(c *Config) { c.Profiles[0].Typ = "a b" }, "jose.profiles[0].typ"},
		{"dup name", func(c *Config) { c.Profiles[1].Name = c.Profiles[0].Name }, "jose.profiles[1].name"},
		{"dup jwt typ", func(c *Config) { c.Profiles[8].Typ = c.Profiles[7].Typ }, "jose.profiles[8].typ"},
		{"alg without key", func(c *Config) { c.Profiles[0].SigningKey = "" }, "jose.profiles[0].signing"},
		{"secret and source", func(c *Config) { c.Keys[4].Secret = strings.Repeat("s", 32) }, "jose.keys[4].secret"},
		{"secret on asymmetric", func(c *Config) {
			c.Keys[0].Source, c.Keys[0].Secret = KeySourceConfig{}, strings.Repeat("s", 32)
		}, "jose.keys[0].secret"},
		{"short hs256 secret", func(c *Config) { c.Keys[4].Source, c.Keys[4].Secret = KeySourceConfig{}, "short" }, "jose.keys[4].secret"},
		{"dir secret length", func(c *Config) { c.Keys[6].Source, c.Keys[6].Secret = KeySourceConfig{}, strings.Repeat("s", 31) }, "jose.keys[6].secret"},
		{"no secret nor source", func(c *Config) { c.Keys[4].Source = KeySourceConfig{} }, "jose.keys[4]"},
		{"alg mismatch", func(c *Config) { c.Profiles[0].SigningAlgorithm = ES256 }, "jose.profiles[0].signing_key"},
		{"unknown kid", func(c *Config) { c.Profiles[0].VerificationKeys = []string{"nope"} }, "jose.profiles[0].verification_keys"},
		{"kid not in allowlist", func(c *Config) { c.Profiles[0].VerificationKeys = []string{"ed-sign", "es-sign"} }, "jose.profiles[0].verification_keys"},
		{"mix HS and asymmetric", func(c *Config) {
			c.Profiles[0].VerificationAlgorithms = []string{EdDSA, HS256}
			c.Profiles[0].VerificationKeys = []string{"ed-sign", "hs-sign"}
		}, "jose.profiles[0].verification_algorithms"},
		{"enc on jws", func(c *Config) { c.Profiles[0].EncryptionKey = "rsa-enc" }, "jose.profiles[0]"},
		{"sign on jwe", func(c *Config) { c.Profiles[5].SigningKey = "ed-sign" }, "jose.profiles[5]"},
		{"jwt on jws", func(c *Config) { c.Profiles[0].JWT.Issuer = "x" }, "jose.profiles[0].jwt"},
		{"enc on signed jwt", func(c *Config) { c.Profiles[7].DecryptionKeys = []string{"rsa-enc"} }, "jose.profiles[7]"},
		{"nested half producer", func(c *Config) { c.Profiles[9].EncryptionKey, c.Profiles[9].EncryptionAlgorithm = "", "" }, "jose.profiles[9]"},
		{"nested half consumer", func(c *Config) { c.Profiles[9].DecryptionKeys, c.Profiles[9].DecryptionAlgorithms = nil, nil }, "jose.profiles[9]"},
		{"bad content enc", func(c *Config) { c.Profiles[5].ContentEncryption = "A128CBC-HS256" }, "jose.profiles[5].content_encryption"},
		{"no issuer", func(c *Config) { c.Profiles[7].JWT.Issuer = "" }, "jose.profiles[7].jwt.issuer"},
		{"no audience", func(c *Config) { c.Profiles[7].JWT.Audiences = nil }, "jose.profiles[7].jwt.audiences"},
		{"dup audience", func(c *Config) { c.Profiles[7].JWT.Audiences = []string{"a", "a"} }, "jose.profiles[7].jwt.audiences"},
		{"ttl over max", func(c *Config) { c.Profiles[7].JWT.TTL, c.Profiles[7].JWT.MaxTTL = time.Hour, time.Minute }, "jose.profiles[7].jwt.ttl"},
		{"negative ttl", func(c *Config) { c.Profiles[7].JWT.TTL = -time.Minute }, "jose.profiles[7].jwt.ttl"},
		{"skew too big", func(c *Config) { d := 3 * time.Minute; c.Profiles[7].JWT.ClockSkew = &d }, "jose.profiles[7].jwt.clock_skew"},
		{"token limit", func(c *Config) { c.Limits.MaxTokenBytes = 2 << 20 }, "jose.limits.max_token_bytes"},
		{"payload over token", func(c *Config) { c.Limits.MaxTokenBytes, c.Limits.MaxPayloadBytes = 1000, 2000 }, "jose.limits.max_payload_bytes"},
		{"depth", func(c *Config) { c.Limits.MaxJSONDepth = 65 }, "jose.limits.max_json_depth"},
		{"too many keys", func(c *Config) { c.Limits.MaxKeys = 2 }, "jose.keys"},
		{"jwks path", func(c *Config) { c.JWKS.Enabled, c.JWKS.Path = true, "jwks" }, "jose.jwks.path"},
		{"jwks age", func(c *Config) { d := 1500 * time.Millisecond; c.JWKS.CacheMaxAge = &d }, "jose.jwks.cache_max_age"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig(t)
			tc.mutate(&cfg)
			ApplyDefaults(&cfg)
			err := ValidateConfig(cfg)
			mustIs(t, err, ErrInvalidConfig)
			var e *Error
			if !errors.As(err, &e) || e.Field != tc.field {
				t.Fatalf("field = %q, want %q (%v)", e.Field, tc.field, err)
			}
			if _, err := New(context.Background(), cfg); !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("New must reject too: %v", err)
			}
		})
	}
}

func TestConsumerOnlyProfileNeedsNoPrivateKey(t *testing.T) {
	m := testMaterial(t)
	dir := t.TempDir()
	cfg := Config{
		Enabled: true,
		Keys: []KeyConfig{{KID: "pub", Algorithm: EdDSA, Operations: []string{OpVerify},
			Source: KeySourceConfig{Format: "pem", File: writeKey(t, dir, "pub.pem", pkixPEM(t, m.ed.Public()))}}},
		Profiles: []ProfileConfig{{Name: "access", Kind: KindJWTSigned, Typ: "at+jwt",
			VerificationAlgorithms: []string{EdDSA}, VerificationKeys: []string{"pub"},
			JWT: JWTConfig{Issuer: testIssuer, Audiences: []string{testAud}}}},
	}
	s := newService(t, cfg)
	info, ok := s.Profile("access")
	if !ok || info.CanProduce || !info.CanConsume {
		t.Fatalf("info = %+v", info)
	}
	_, err := s.IssueJWT(context.Background(), "access", RegisteredClaims{Subject: "u"}, nil)
	mustIs(t, err, ErrOperationUnavailable)
}

func TestConfigRedaction(t *testing.T) {
	cfg := testConfig(t)
	secretPath := cfg.Keys[0].Source.File
	for _, s := range []string{
		fmt.Sprintf("%v", cfg), fmt.Sprintf("%+v", cfg), fmt.Sprintf("%#v", cfg),
		fmt.Sprintf("%v", cfg.Keys[0]), fmt.Sprintf("%#v", cfg.Keys[0].Source),
	} {
		if strings.Contains(s, secretPath) {
			t.Fatalf("key path leaked: %s", s)
		}
	}
	var buf strings.Builder
	slog.New(slog.NewTextHandler(&buf, nil)).Info("cfg", "jose", cfg, "key", cfg.Keys[0])
	if strings.Contains(buf.String(), secretPath) {
		t.Fatalf("key path leaked in slog: %s", buf.String())
	}
}

func TestCloneIsDeep(t *testing.T) {
	cfg := testConfig(t)
	ApplyDefaults(&cfg)
	c := cfg.Clone()
	c.Keys[0].Operations[0] = "x"
	c.Profiles[7].JWT.Audiences[0] = "x"
	*c.Profiles[7].JWT.ClockSkew = time.Hour
	*c.JWKS.CacheMaxAge = time.Hour
	if cfg.Keys[0].Operations[0] == "x" || cfg.Profiles[7].JWT.Audiences[0] == "x" ||
		*cfg.Profiles[7].JWT.ClockSkew == time.Hour || *cfg.JWKS.CacheMaxAge == time.Hour {
		t.Fatal("Clone shares memory with the original")
	}
}

func TestOptionsValidation(t *testing.T) {
	cfg := testConfig(t)
	noop := func(context.Context, RegisteredClaims, any) error { return nil }
	cases := []struct {
		name string
		opts []Option
		want error
	}{
		{"nil clock", []Option{WithClock(nil)}, ErrInvalidConfig},
		{"unknown profile", []Option{WithClaimsValidator("nope", noop)}, ErrUnknownProfile},
		{"non jwt profile", []Option{WithClaimsValidator("msg-ed", noop)}, ErrProfileKind},
		{"duplicate", []Option{WithClaimsValidator("access", noop), WithClaimsValidator("access", noop)}, ErrInvalidConfig},
		{"nil validator", []Option{WithClaimsValidator("access", nil)}, ErrInvalidConfig},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(context.Background(), cfg, tc.opts...)
			mustIs(t, err, tc.want)
		})
	}
}

func TestNewDoesNotMutateCallerConfig(t *testing.T) {
	cfg := testConfig(t)
	newService(t, cfg)
	if cfg.Limits.MaxTokenBytes != 0 || cfg.Profiles[7].JWT.ClockSkew != nil {
		t.Fatal("New applied defaults to the caller's config")
	}
}

func TestHS256DefaultsAndInlineSecret(t *testing.T) {
	secret := "a-long-enough-secret-for-hs256-tokens!" // >= 32 bytes, as from ${JWT_SECRET}
	cfg := Config{
		Enabled: true,
		Keys: []KeyConfig{
			{KID: "jwt", Secret: secret},
			{KID: "enc", Algorithm: Dir, Secret: strings.Repeat("k", 32)},
		},
		Profiles: []ProfileConfig{
			{Name: "access", Kind: KindJWTSigned, Typ: "at+jwt", SigningKey: "jwt", VerificationKeys: []string{"jwt"},
				JWT: JWTConfig{Issuer: testIssuer, Audiences: []string{testAud}}},
			{Name: "nested", Kind: KindJWTNested, Typ: "secret+jwt", SigningKey: "jwt", VerificationKeys: []string{"jwt"},
				EncryptionKey: "enc", DecryptionKeys: []string{"enc"},
				JWT: JWTConfig{Issuer: testIssuer, Audiences: []string{testAud}}},
		},
	}
	applied := cfg.Clone()
	ApplyDefaults(&applied)
	k, p := applied.Keys[0], applied.Profiles[0]
	if k.Algorithm != HS256 || strings.Join(k.Operations, ",") != "sign,verify" {
		t.Fatalf("key defaults = %+v", k.Operations)
	}
	if p.SigningAlgorithm != HS256 || strings.Join(p.VerificationAlgorithms, ",") != HS256 {
		t.Fatalf("profile algorithms not derived: %q %v", p.SigningAlgorithm, p.VerificationAlgorithms)
	}
	if n := applied.Profiles[1]; n.EncryptionAlgorithm != Dir || n.DecryptionAlgorithms[0] != Dir {
		t.Fatalf("nested algorithms not derived: %+v", n)
	}

	s := newService(t, cfg)
	for _, profile := range []string{"access", "nested"} {
		token, err := s.IssueJWT(ctx, profile, RegisteredClaims{Subject: "u"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		v, err := s.ValidateJWT(ctx, profile, token, nil)
		if err != nil || v.Signing.Algorithm != HS256 {
			t.Fatalf("%s: %v %+v", profile, err, v)
		}
	}
	if jwks, _ := s.PublicJWKS(ctx); string(jwks) != `{"keys":[]}` {
		t.Fatalf("symmetric keys must never be published: %s", jwks)
	}

	// the inline secret never leaks through formatting or logs
	var buf strings.Builder
	slog.New(slog.NewTextHandler(&buf, nil)).Info("cfg", "jose", cfg, "key", cfg.Keys[0])
	for _, out := range []string{fmt.Sprintf("%v %+v %#v", cfg, cfg.Keys[0], cfg.Keys), buf.String()} {
		if strings.Contains(out, secret) {
			t.Fatalf("secret leaked: %s", out)
		}
	}
	bad := cfg.Clone()
	bad.Keys[0].Secret = strings.Repeat("x", 31)
	_, err := New(ctx, bad)
	mustIs(t, err, ErrInvalidConfig)
	if strings.Contains(err.Error(), "xxxx") {
		t.Fatalf("secret leaked in error: %v", err)
	}
}
