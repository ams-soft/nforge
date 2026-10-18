package jose

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	gojose "github.com/go-jose/go-jose/v4"
)

type accessFields struct {
	TenantID string   `json:"tenant_id"`
	Roles    []string `json:"roles,omitempty"`
	Scope    string   `json:"scope,omitempty"`
}

func TestIssueValidateSigned(t *testing.T) {
	clock := newTestClock()
	s := newService(t, testConfig(t), clock.Option())

	token, err := s.IssueJWT(ctx, "access", RegisteredClaims{Subject: "user-1"}, accessFields{TenantID: "t1", Roles: []string{"admin"}, Scope: "a b"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(token, ".") != 2 {
		t.Fatalf("signed jwt must be a compact jws: %s", token)
	}
	var custom accessFields
	v, err := s.ValidateJWT(ctx, "access", token, &custom)
	if err != nil {
		t.Fatal(err)
	}
	r := v.Registered
	now := baseTime.Unix()
	if r.Issuer != testIssuer || r.Subject != "user-1" || strings.Join(r.Audience, ",") != "api,other" ||
		int64(*r.IssuedAt) != now || int64(*r.NotBefore) != now || int64(*r.ExpiresAt) != now+900 || len(r.ID) != 22 {
		t.Fatalf("registered = %+v", r)
	}
	if custom.TenantID != "t1" || custom.Roles[0] != "admin" || custom.Scope != "a b" {
		t.Fatalf("custom = %+v", custom)
	}
	if v.Signing.Type != "at+jwt" || v.Signing.Algorithm != EdDSA || v.Encryption != nil {
		t.Fatalf("headers = %+v %+v", v.Signing, v.Encryption)
	}

	// nil target validates without returning custom fields; map targets work
	if _, err := s.ValidateJWT(ctx, "access", token, nil); err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if _, err := s.ValidateJWT(ctx, "access", token, &m); err != nil || m["tenant_id"] != "t1" || m["iss"] != nil {
		t.Fatalf("map target = %v %v", m, err)
	}

	// jti values are independent
	other, _ := s.IssueJWT(ctx, "access", RegisteredClaims{Subject: "user-1"}, nil)
	v2, _ := s.ValidateJWT(ctx, "access", other, nil)
	if v2.Registered.ID == r.ID {
		t.Fatal("jti must be random per token")
	}
}

func TestIssueValidateNested(t *testing.T) {
	clock := newTestClock()
	s := newService(t, testConfig(t), clock.Option())
	token, err := s.IssueJWT(ctx, "nested", RegisteredClaims{Subject: "user-1"}, map[string]any{"tenant_id": "t1"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(token, ".") != 4 {
		t.Fatalf("nested jwt must be a compact jwe")
	}
	var custom accessFields
	v, err := s.ValidateJWT(ctx, "nested", token, &custom)
	if err != nil {
		t.Fatal(err)
	}
	if v.Encryption == nil || v.Encryption.ContentType != "JWT" || v.Encryption.Type != "secret+jwt" || v.Signing.Type != "secret+jwt" || custom.TenantID != "t1" {
		t.Fatalf("v = %+v %+v %+v", v.Signing, v.Encryption, custom)
	}
	if int64(*v.Registered.ExpiresAt)-int64(*v.Registered.IssuedAt) != 300 {
		t.Fatal("nested profile ttl not applied")
	}

	signed, _ := s.IssueJWT(ctx, "access", RegisteredClaims{Subject: "u"}, nil)
	_, err = s.ValidateJWT(ctx, "nested", signed, nil)
	mustIs(t, err, ErrMalformedToken)
	_, err = s.ValidateJWT(ctx, "access", token, nil)
	mustIs(t, err, ErrMalformedToken)
}

func TestNestedLayersAreIndependent(t *testing.T) {
	clock := newTestClock()
	m := testMaterial(t)
	s := newService(t, testConfig(t), clock.Option())
	encrypt := func(plaintext []byte, cty string) string {
		opts := (&gojose.EncrypterOptions{}).WithType("secret+jwt")
		if cty != "" {
			opts = opts.WithContentType(gojose.ContentType(cty))
		}
		enc, err := gojose.NewEncrypter(gojose.A256GCM, gojose.Recipient{Algorithm: gojose.RSA_OAEP_256, Key: &m.rsaEnc.PublicKey, KeyID: "rsa-enc"}, opts)
		if err != nil {
			t.Fatal(err)
		}
		obj, _ := enc.Encrypt(plaintext)
		out, _ := obj.CompactSerialize()
		return out
	}
	sign := func(key any, kid string, claims []byte) string {
		signer, err := gojose.NewSigner(gojose.SigningKey{Algorithm: gojose.EdDSA, Key: key},
			(&gojose.SignerOptions{}).WithType("secret+jwt").WithHeader("kid", kid))
		if err != nil {
			t.Fatal(err)
		}
		obj, _ := signer.Sign(claims)
		out, _ := obj.CompactSerialize()
		return out
	}
	now := baseTime.Unix()
	claims := []byte(fmt.Sprintf(`{"iss":%q,"sub":"u","aud":"secret-api","iat":%d,"exp":%d,"jti":"id"}`, testIssuer, now, now+60))

	if _, err := s.ValidateJWT(ctx, "nested", encrypt([]byte(sign(m.ed, "ed-sign", claims)), "JWT"), nil); err != nil {
		t.Fatalf("hand-built nested token: %v", err)
	}
	cases := []struct {
		name, token string
		want        error
	}{
		{"valid encryption, invalid inner signature", encrypt([]byte(sign(m.ed2, "ed-sign", claims)), "JWT"), ErrInvalidSignature},
		{"encrypted unsigned claims", encrypt(claims, "JWT"), ErrMalformedToken},
		{"missing cty", encrypt([]byte(sign(m.ed, "ed-sign", claims)), ""), ErrInvalidType},
		{"wrong cty", encrypt([]byte(sign(m.ed, "ed-sign", claims)), "jwt+json"), ErrInvalidType},
		{"three layers", encrypt([]byte(encrypt([]byte(sign(m.ed, "ed-sign", claims)), "JWT")), "JWT"), ErrMalformedToken},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v, err := s.ValidateJWT(ctx, "nested", tc.token, nil)
			if v != nil {
				t.Fatal("identity returned on failure")
			}
			mustIs(t, err, tc.want)
		})
	}
}

// signClaims signs raw claims with the access profile's key, bypassing
// issuance checks, to test validation of arbitrary payloads.
func signClaims(t *testing.T, claims string) string {
	t.Helper()
	signer, err := gojose.NewSigner(gojose.SigningKey{Algorithm: gojose.EdDSA, Key: testMaterial(t).ed},
		(&gojose.SignerOptions{}).WithType("at+jwt").WithHeader("kid", "ed-sign"))
	if err != nil {
		t.Fatal(err)
	}
	obj, err := signer.Sign([]byte(claims))
	if err != nil {
		t.Fatal(err)
	}
	out, _ := obj.CompactSerialize()
	return out
}

func TestValidateClaims(t *testing.T) {
	clock := newTestClock()
	s := newService(t, testConfig(t), clock.Option())
	now := baseTime.Unix()
	claims := func(overrides string) string {
		base := map[string]any{"iss": testIssuer, "sub": "u", "aud": []string{testAud}, "iat": now - 60, "nbf": now - 60, "exp": now + 60, "jti": "id"}
		if overrides != "" {
			var o map[string]any
			if err := json.Unmarshal([]byte(overrides), &o); err != nil {
				t.Fatal(err)
			}
			for k, v := range o {
				if v == "<delete>" {
					delete(base, k)
				} else {
					base[k] = v
				}
			}
		}
		b, _ := json.Marshal(base)
		return string(b)
	}
	cases := []struct {
		name, claims string
		want         error
	}{
		{"valid", claims(""), nil},
		{"string audience", claims(`{"aud":"api"}`), nil},
		{"any accepted audience", claims(`{"aud":["x","other"]}`), nil},
		{"no nbf", claims(`{"nbf":"<delete>"}`), nil},
		{"wrong issuer", claims(`{"iss":"https://evil.test"}`), ErrInvalidIssuer},
		{"issuer case", claims(`{"iss":"HTTPS://issuer.test"}`), ErrInvalidIssuer},
		{"wrong audience", claims(`{"aud":["API"]}`), ErrInvalidAudience},
		{"empty audience array", claims(`{"aud":[]}`), ErrInvalidClaims},
		{"duplicate audience", claims(`{"aud":["api","api"]}`), ErrInvalidClaims},
		{"null audience", claims(`{"aud":null}`), ErrInvalidClaims},
		{"numeric audience", claims(`{"aud":1}`), ErrInvalidClaims},
		{"missing sub", claims(`{"sub":"<delete>"}`), ErrInvalidClaims},
		{"empty sub", claims(`{"sub":""}`), ErrInvalidClaims},
		{"missing jti", claims(`{"jti":"<delete>"}`), ErrInvalidClaims},
		{"missing iat", claims(`{"iat":"<delete>"}`), ErrInvalidClaims},
		{"missing exp", claims(`{"exp":"<delete>"}`), ErrInvalidClaims},
		{"missing iss", claims(`{"iss":"<delete>"}`), ErrInvalidClaims},
		{"control char in sub", claims(`{"sub":"u\u0000"}`), ErrInvalidClaims},
		{"sub not a string", claims(`{"sub":1}`), ErrInvalidClaims},
		{"exp string", claims(`{"exp":"123"}`), ErrInvalidClaims},
		{"exp null", claims(`{"exp":null}`), ErrInvalidClaims},
		{"nbf null", claims(`{"nbf":null}`), ErrInvalidClaims},
		{"exp fraction", fmt.Sprintf(`{"iss":%q,"sub":"u","aud":"api","iat":%d,"exp":%d.5,"jti":"i"}`, testIssuer, now, now+10), ErrInvalidClaims},
		{"exp exponent", fmt.Sprintf(`{"iss":%q,"sub":"u","aud":"api","iat":%d,"exp":1e10,"jti":"i"}`, testIssuer, now), ErrInvalidClaims},
		{"exp overflow", fmt.Sprintf(`{"iss":%q,"sub":"u","aud":"api","iat":%d,"exp":99999999999999999999,"jti":"i"}`, testIssuer, now), ErrInvalidClaims},
		{"exp year 10000", fmt.Sprintf(`{"iss":%q,"sub":"u","aud":"api","iat":%d,"exp":253402300800,"jti":"i"}`, testIssuer, now), ErrInvalidClaims},
		{"lifetime over max", claims(fmt.Sprintf(`{"iat":%d,"nbf":%d,"exp":%d}`, now-1000, now-1000, now+1000)), ErrInvalidClaims},
		{"exp before iat", claims(fmt.Sprintf(`{"iat":%d,"nbf":%d,"exp":%d}`, now, now, now-1)), ErrInvalidClaims},
		{"nbf before iat", claims(fmt.Sprintf(`{"nbf":%d}`, now-120)), ErrInvalidClaims},
		{"nbf equals exp", claims(fmt.Sprintf(`{"nbf":%d,"exp":%d}`, now+10, now+10)), ErrInvalidClaims},
		{"iat in the future", claims(fmt.Sprintf(`{"iat":%d,"nbf":%d,"exp":%d}`, now+31, now+31, now+100)), ErrNotYetValid},
		{"duplicate claim", fmt.Sprintf(`{"iss":%q,"sub":"u","sub":"admin","aud":"api","iat":%d,"exp":%d,"jti":"i"}`, testIssuer, now, now+60), ErrInvalidClaims},
		{"duplicate nested member", claims(`{"tenant":{"a":1}}`)[:0] + fmt.Sprintf(`{"iss":%q,"sub":"u","aud":"api","iat":%d,"exp":%d,"jti":"i","x":{"a":1,"a":2}}`, testIssuer, now, now+60), ErrInvalidClaims},
		{"invalid utf-8", fmt.Sprintf("{\"iss\":%q,\"sub\":\"\xff\",\"aud\":\"api\",\"iat\":%d,\"exp\":%d,\"jti\":\"i\"}", testIssuer, now, now+60), ErrInvalidClaims},
		{"array payload", `[1]`, ErrInvalidClaims},
		{"empty payload", ``, ErrInvalidClaims},
		{"deep custom claim", claims(`{"x":` + strings.Repeat("[", 20) + strings.Repeat("]", 20) + `}`), ErrInvalidClaims},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v, err := s.ValidateJWT(ctx, "access", signClaims(t, tc.claims), nil)
			if tc.want == nil {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if v != nil {
				t.Fatal("identity returned on failure")
			}
			mustIs(t, err, tc.want)
		})
	}
}

func TestTimeBoundaries(t *testing.T) {
	clock := newTestClock()
	s := newService(t, testConfig(t), clock.Option()) // access: skew 30s
	now := baseTime.Unix()
	token := signClaims(t, fmt.Sprintf(`{"iss":%q,"sub":"u","aud":"api","iat":%d,"nbf":%d,"exp":%d,"jti":"i"}`, testIssuer, now, now+100, now+200))

	check := func(at int64, want error) {
		t.Helper()
		clock.Set(time.Unix(at, 0))
		_, err := s.ValidateJWT(ctx, "access", token, nil)
		if want == nil && err != nil {
			t.Fatalf("at %d: %v", at-now, err)
		}
		if want != nil && !errors.Is(err, want) {
			t.Fatalf("at %d: got %v, want %v", at-now, err, want)
		}
	}
	check(now+69, ErrNotYetValid) // now + skew < nbf
	check(now+70, nil)            // now + skew == nbf
	check(now+229, nil)           // now < exp + skew
	check(now+230, ErrExpired)    // equality is expired

	clock.Set(time.Unix(now, 0))
	future := signClaims(t, fmt.Sprintf(`{"iss":%q,"sub":"u","aud":"api","iat":%d,"exp":%d,"jti":"i"}`, testIssuer, now+30, now+100))
	if _, err := s.ValidateJWT(ctx, "access", future, nil); err != nil {
		t.Fatalf("iat within skew: %v", err)
	}

	// zero skew is honored
	cfg := testConfig(t)
	zero := time.Duration(0)
	cfg.Profiles[7].JWT.ClockSkew = &zero
	strict := newService(t, cfg, clock.Option())
	clock.Set(time.Unix(now+200, 0))
	_, err := strict.ValidateJWT(ctx, "access", token, nil)
	mustIs(t, err, ErrExpired)
	clock.Set(time.Unix(now+199, 0))
	if _, err := strict.ValidateJWT(ctx, "access", token, nil); err != nil {
		t.Fatal(err)
	}
}

func TestIssueRules(t *testing.T) {
	clock := newTestClock()
	s := newService(t, testConfig(t), clock.Option())
	at := func(secs int64) *NumericDate { return NewNumericDate(clock.At(secs)) }
	cases := []struct {
		name    string
		claims  RegisteredClaims
		private any
		want    error
	}{
		{"defaults", RegisteredClaims{Subject: "u"}, nil, nil},
		{"audience subset", RegisteredClaims{Subject: "u", Audience: Audience{"other"}}, nil, nil},
		{"future activation", RegisteredClaims{Subject: "u", NotBefore: at(60), ExpiresAt: at(600)}, nil, nil},
		{"backdated but valid", RegisteredClaims{Subject: "u", IssuedAt: at(-60)}, nil, nil},
		{"no subject", RegisteredClaims{}, nil, ErrInvalidClaims},
		{"other issuer", RegisteredClaims{Subject: "u", Issuer: "https://x"}, nil, ErrInvalidIssuer},
		{"unknown audience", RegisteredClaims{Subject: "u", Audience: Audience{"x"}}, nil, ErrInvalidAudience},
		{"duplicate audience", RegisteredClaims{Subject: "u", Audience: Audience{"api", "api"}}, nil, ErrInvalidAudience},
		{"over max ttl", RegisteredClaims{Subject: "u", ExpiresAt: at(901)}, nil, ErrInvalidClaims},
		{"iat in the future", RegisteredClaims{Subject: "u", IssuedAt: at(10)}, nil, ErrInvalidClaims},
		{"backdated and expired", RegisteredClaims{Subject: "u", IssuedAt: at(-900), NotBefore: at(-900), ExpiresAt: at(0)}, nil, ErrExpired},
		{"nbf after exp", RegisteredClaims{Subject: "u", NotBefore: at(700), ExpiresAt: at(600)}, nil, ErrInvalidClaims},
		{"date out of range", RegisteredClaims{Subject: "u", ExpiresAt: NewNumericDate(time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC))}, nil, ErrInvalidClaims},
		{"control chars", RegisteredClaims{Subject: "u\n"}, nil, ErrInvalidClaims},
		{"reserved custom name", RegisteredClaims{Subject: "u"}, map[string]any{"sub": "admin"}, ErrInvalidClaims},
		{"reserved name even if empty", RegisteredClaims{Subject: "u"}, map[string]string{"jti": ""}, ErrInvalidClaims},
		{"reserved via struct tag", RegisteredClaims{Subject: "u"}, struct {
			Exp int `json:"exp"`
		}{1}, ErrInvalidClaims},
		{"reserved via embedded field", RegisteredClaims{Subject: "u"}, struct{ embeddedIss }{embeddedIss{"x"}}, ErrInvalidClaims},
		{"duplicate via marshaler", RegisteredClaims{Subject: "u"}, dupMarshaler{}, ErrInvalidClaims},
		{"non-object marshaler", RegisteredClaims{Subject: "u"}, arrayMarshaler{}, ErrInvalidClaims},
		{"non-object value", RegisteredClaims{Subject: "u"}, []string{"a"}, ErrInvalidClaims},
		{"int-keyed map", RegisteredClaims{Subject: "u"}, map[int]string{1: "a"}, ErrInvalidClaims},
		{"too large", RegisteredClaims{Subject: "u"}, map[string]string{"blob": strings.Repeat("a", 9000)}, ErrPayloadTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			token, err := s.IssueJWT(ctx, "access", tc.claims, tc.private)
			if tc.want == nil {
				if err != nil {
					t.Fatal(err)
				}
				clock.Add(time.Duration(60) * time.Second)
				defer clock.Set(baseTime)
				if _, err := s.ValidateJWT(ctx, "access", token, nil); err != nil {
					t.Fatalf("issued token does not validate: %v", err)
				}
				return
			}
			if token != "" {
				t.Fatal("partial token returned")
			}
			mustIs(t, err, tc.want)
		})
	}
}

type embeddedIss struct {
	Iss string `json:"iss"`
}

type dupMarshaler struct{}

func (dupMarshaler) MarshalJSON() ([]byte, error) { return []byte(`{"a":1,"a":2}`), nil }

type arrayMarshaler struct{}

func (arrayMarshaler) MarshalJSON() ([]byte, error) { return []byte(`[1]`), nil }

func TestRefreshTokenRejectedByAccessProfile(t *testing.T) {
	s := newService(t, testConfig(t), newTestClock().Option())
	refresh, err := s.IssueJWT(ctx, "refresh", RegisteredClaims{Subject: "u"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.ValidateJWT(ctx, "access", refresh, nil)
	mustIs(t, err, ErrInvalidType)
	access, _ := s.IssueJWT(ctx, "access", RegisteredClaims{Subject: "u"}, nil)
	_, err = s.ValidateJWT(ctx, "refresh", access, nil)
	mustIs(t, err, ErrInvalidType)
}

func TestTargetUnchangedOnFailure(t *testing.T) {
	clock := newTestClock()
	s := newService(t, testConfig(t), clock.Option())
	token, _ := s.IssueJWT(ctx, "access", RegisteredClaims{Subject: "u"}, accessFields{TenantID: "evil"})

	target := accessFields{TenantID: "original"}
	clock.Add(time.Hour)
	_, err := s.ValidateJWT(ctx, "access", token, &target)
	mustIs(t, err, ErrExpired)
	if target.TenantID != "original" {
		t.Fatal("target mutated on failure")
	}

	clock.Set(baseTime)
	var wrong struct {
		TenantID int `json:"tenant_id"`
	}
	_, err = s.ValidateJWT(ctx, "access", token, &wrong)
	mustIs(t, err, ErrInvalidClaims)

	for _, bad := range []any{accessFields{}, (*accessFields)(nil), new(int), new([]string), new(map[int]string)} {
		_, err := s.ValidateJWT(ctx, "access", token, bad)
		mustIs(t, err, ErrInvalidTarget)
	}
}

func TestClaimsValidator(t *testing.T) {
	var seen []any
	errDB := errors.New("db down: host=10.0.0.1")
	var result error
	validator := func(_ context.Context, c RegisteredClaims, custom any) error {
		seen = append(seen, custom)
		return result
	}
	clock := newTestClock()
	s := newService(t, testConfig(t), clock.Option(), WithClaimsValidator("access", validator))

	token, err := s.IssueJWT(ctx, "access", RegisteredClaims{Subject: "u"}, accessFields{TenantID: "t1", Roles: []string{"r"}})
	if err != nil {
		t.Fatal(err)
	}
	if m, ok := seen[0].(map[string]any); !ok || m["tenant_id"] != "t1" {
		t.Fatalf("issuance must see the normalized map: %#v", seen[0])
	}
	var target accessFields
	if _, err := s.ValidateJWT(ctx, "access", token, &target); err != nil {
		t.Fatal(err)
	}
	if p, ok := seen[1].(*accessFields); !ok || p.TenantID != "t1" {
		t.Fatalf("validation with a target must see the decoded target: %#v", seen[1])
	}
	if _, err := s.ValidateJWT(ctx, "access", token, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := seen[2].(map[string]any); !ok {
		t.Fatalf("validation without a target must see the normalized map: %#v", seen[2])
	}

	cases := []struct {
		name   string
		result error
		want   error
	}{
		{"rejected", fmt.Errorf("account disabled: %w", ErrClaimsRejected), ErrClaimsRejected},
		{"unavailable", fmt.Errorf("%w: %w", ErrValidationUnavailable, errDB), ErrValidationUnavailable},
		{"unknown", errDB, ErrInternal},
		{"canceled", context.Canceled, context.Canceled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result = tc.result
			defer func() { result = nil }()
			target := accessFields{TenantID: "keep"}
			v, err := s.ValidateJWT(ctx, "access", token, &target)
			if v != nil || target.TenantID != "keep" {
				t.Fatal("identity or target leaked on validator failure")
			}
			mustIs(t, err, tc.want)
			if strings.Contains(err.Error(), "10.0.0.1") {
				t.Fatalf("application error text leaked: %v", err)
			}
			if _, err := s.IssueJWT(ctx, "access", RegisteredClaims{Subject: "u"}, nil); !errors.Is(err, tc.want) {
				t.Fatalf("issue: %v", err)
			}
		})
	}
}

func TestCanceledContext(t *testing.T) {
	s := newService(t, testConfig(t))
	c, cancel := context.WithCancel(ctx)
	cancel()
	_, err := s.Sign(c, "msg-ed", nil)
	mustIs(t, err, context.Canceled)
	_, err = s.ValidateJWT(c, "access", "a.b.c", nil)
	mustIs(t, err, context.Canceled)
}

func TestNumericDateJSON(t *testing.T) {
	for in, ok := range map[string]bool{
		"0": true, "-1": true, "1700000000": true, "253402300799": true, "-62135596800": true,
		"253402300800": false, "-62135596801": false, "1.0": false, "1e3": false, `"1"`: false,
		"null": false, "01": false, "-": false, "": false, "true": false,
	} {
		var d NumericDate
		if err := d.UnmarshalJSON([]byte(in)); (err == nil) != ok {
			t.Errorf("%q: err = %v", in, err)
		}
	}
	b, _ := json.Marshal(RegisteredClaims{Audience: Audience{"a"}, ExpiresAt: NewNumericDate(time.Unix(10, 900))})
	if string(b) != `{"aud":["a"],"exp":10}` {
		t.Errorf("marshal = %s", b)
	}
	if NumericDate(10).Time() != time.Unix(10, 0).UTC() {
		t.Error("Time()")
	}
}
