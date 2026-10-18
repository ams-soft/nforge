package jose

import (
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	gojose "github.com/go-jose/go-jose/v4"
)

var ctx = context.Background()

// splice replaces segment i of a compact token.
func splice(token string, i int, seg string) string {
	parts := strings.Split(token, ".")
	parts[i] = seg
	return strings.Join(parts, ".")
}

func header(t *testing.T, members map[string]any) string {
	t.Helper()
	b, err := json.Marshal(members)
	if err != nil {
		t.Fatal(err)
	}
	return b64(b)
}

func flip(seg string) string {
	b, _ := base64.RawURLEncoding.DecodeString(seg)
	if len(b) == 0 {
		return "AA"
	}
	b[len(b)/2] ^= 0x01
	return b64(b)
}

func TestSignVerifyAllAlgorithms(t *testing.T) {
	s := newService(t, testConfig(t))
	for _, profile := range []string{"msg-ed", "msg-es", "msg-ps", "msg-rs", "msg-hs"} {
		t.Run(profile, func(t *testing.T) {
			for _, payload := range [][]byte{[]byte("hello"), {}, {0, 1, 2, 255}} {
				token, err := s.Sign(ctx, profile, payload)
				if err != nil {
					t.Fatal(err)
				}
				msg, err := s.Verify(ctx, profile, token)
				if err != nil {
					t.Fatal(err)
				}
				if string(msg.Payload) != string(payload) || msg.Header.Type != "msg+jws" || msg.Header.KeyID == "" || msg.Header.Encryption != "" {
					t.Fatalf("msg = %+v", msg)
				}
				// tampering with any segment fails
				for i := range 3 {
					seg := strings.Split(token, ".")[i]
					_, err := s.Verify(ctx, profile, splice(token, i, flip(seg)))
					if err == nil {
						t.Fatalf("segment %d tampering accepted", i)
					}
				}
			}
		})
	}
}

func TestVerifyRejects(t *testing.T) {
	s := newService(t, testConfig(t))
	token, err := s.Sign(ctx, "msg-ed", []byte("payload"))
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(token, ".")
	esToken, _ := s.Sign(ctx, "msg-es", []byte("payload"))
	hsToken, _ := s.Sign(ctx, "msg-hs", []byte("payload"))

	// HS256 signed with the public key bytes: the classic algorithm confusion
	m := testMaterial(t)
	confHeader := header(t, map[string]any{"alg": "HS256", "kid": "ed-sign", "typ": "msg+jws"})
	mac := hmac.New(sha256.New, m.ed.Public().(ed25519.PublicKey))
	mac.Write([]byte(confHeader + "." + parts[1]))
	confusion := confHeader + "." + parts[1] + "." + b64(mac.Sum(nil))

	cases := []struct {
		name, token string
		want        error
	}{
		{"alg none", header(t, map[string]any{"alg": "none", "kid": "ed-sign", "typ": "msg+jws"}) + "." + parts[1] + ".", ErrUnsupportedAlgorithm},
		{"algorithm confusion", confusion, ErrUnsupportedAlgorithm},
		{"other profile key", esToken, ErrUnsupportedAlgorithm},
		{"hs token on ed profile", hsToken, ErrUnsupportedAlgorithm},
		{"missing kid", splice(token, 0, header(t, map[string]any{"alg": "EdDSA", "typ": "msg+jws"})), ErrMalformedToken},
		{"unknown kid", splice(token, 0, header(t, map[string]any{"alg": "EdDSA", "kid": "nope", "typ": "msg+jws"})), ErrUnknownKey},
		{"wrong typ", splice(token, 0, header(t, map[string]any{"alg": "EdDSA", "kid": "ed-sign", "typ": "JWT"})), ErrInvalidType},
		{"missing typ", splice(token, 0, header(t, map[string]any{"alg": "EdDSA", "kid": "ed-sign"})), ErrInvalidType},
		{"crit header", splice(token, 0, header(t, map[string]any{"alg": "EdDSA", "kid": "ed-sign", "typ": "msg+jws", "crit": []string{"exp"}})), ErrMalformedToken},
		{"jwk header", splice(token, 0, header(t, map[string]any{"alg": "EdDSA", "kid": "ed-sign", "typ": "msg+jws", "jwk": map[string]any{}})), ErrMalformedToken},
		{"b64 header", splice(token, 0, header(t, map[string]any{"alg": "EdDSA", "kid": "ed-sign", "typ": "msg+jws", "b64": false})), ErrMalformedToken},
		{"cty on jws", splice(token, 0, header(t, map[string]any{"alg": "EdDSA", "kid": "ed-sign", "typ": "msg+jws", "cty": "JWT"})), ErrMalformedToken},
		{"kid not a string", splice(token, 0, header(t, map[string]any{"alg": "EdDSA", "kid": 1, "typ": "msg+jws"})), ErrMalformedToken},
		{"duplicate header member", splice(token, 0, b64([]byte(`{"alg":"EdDSA","kid":"ed-sign","typ":"msg+jws","alg":"EdDSA"}`))), ErrMalformedToken},
		{"invalid utf-8 header", splice(token, 0, b64([]byte("{\"alg\":\"EdDSA\",\"kid\":\"ed-sign\",\"typ\":\"\xff\"}"))), ErrMalformedToken},
		{"header not object", splice(token, 0, b64([]byte(`[]`))), ErrMalformedToken},
		{"two segments", parts[0] + "." + parts[1], ErrMalformedToken},
		{"four segments", token + ".x", ErrMalformedToken},
		{"json serialization", `{"payload":"` + parts[1] + `","protected":"` + parts[0] + `","signature":"` + parts[2] + `"}`, ErrMalformedToken},
		{"padding", token + "=", ErrMalformedToken},
		{"leading space", " " + token, ErrMalformedToken},
		{"trailing newline", token + "\n", ErrMalformedToken},
		{"non-canonical base64", splice(token, 2, parts[2][:len(parts[2])-1]+"B"), ErrMalformedToken},
		{"short signature", splice(token, 2, b64(make([]byte, 10))), ErrInvalidSignature},
		{"wrong signature", splice(token, 2, b64(make([]byte, 64))), ErrInvalidSignature},
		{"empty", "", ErrMalformedToken},
		{"oversize", strings.Repeat("a", 20000), ErrPayloadTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg, err := s.Verify(ctx, "msg-ed", tc.token)
			if msg != nil {
				t.Fatal("message returned on failure")
			}
			mustIs(t, err, tc.want)
			if strings.Contains(err.Error(), parts[2]) || strings.Contains(err.Error(), parts[1]) {
				t.Fatalf("error leaks token data: %v", err)
			}
		})
	}
}

func TestProfileKindAndAvailability(t *testing.T) {
	s := newService(t, testConfig(t))
	_, err := s.Sign(ctx, "box-dir", []byte("x"))
	mustIs(t, err, ErrProfileKind)
	_, err = s.Sign(ctx, "access", []byte("x"))
	mustIs(t, err, ErrProfileKind)
	_, err = s.Encrypt(ctx, "msg-ed", []byte("x"))
	mustIs(t, err, ErrProfileKind)
	_, err = s.Verify(ctx, "missing", "a.b.c")
	mustIs(t, err, ErrUnknownProfile)
	_, err = s.IssueJWT(ctx, "msg-ed", RegisteredClaims{Subject: "u"}, nil)
	mustIs(t, err, ErrProfileKind)
	_, err = s.ValidateJWT(ctx, "box-rsa", "a.b.c.d.e", nil)
	mustIs(t, err, ErrProfileKind)
}

func TestPayloadLimits(t *testing.T) {
	cfg := testConfig(t)
	cfg.Limits.MaxPayloadBytes = 100
	cfg.Limits.MaxTokenBytes = 400
	s := newService(t, cfg)
	_, err := s.Sign(ctx, "msg-ed", make([]byte, 101))
	mustIs(t, err, ErrPayloadTooLarge)
	_, err = s.Sign(ctx, "msg-rs", make([]byte, 100)) // fits the payload limit, not the token limit
	mustIs(t, err, ErrPayloadTooLarge)
	if _, err := s.Sign(ctx, "msg-ed", make([]byte, 100)); err != nil {
		t.Fatal(err)
	}
}

// TestVerifyExternallyProducedJWS checks interoperability with tokens built
// without this package: by hand with the stdlib, and by go-jose directly.
func TestVerifyExternallyProducedJWS(t *testing.T) {
	m := testMaterial(t)
	s := newService(t, testConfig(t))

	h := b64([]byte(`{"alg":"EdDSA","kid":"ed-sign","typ":"msg+jws"}`))
	p := b64([]byte("external"))
	sig := ed25519.Sign(m.ed, []byte(h+"."+p))
	msg, err := s.Verify(ctx, "msg-ed", h+"."+p+"."+b64(sig))
	if err != nil || string(msg.Payload) != "external" {
		t.Fatalf("stdlib-produced jws: %v", err)
	}

	signer, err := gojose.NewSigner(gojose.SigningKey{Algorithm: gojose.HS256, Key: m.hs},
		(&gojose.SignerOptions{}).WithType("msg+jws").WithHeader("kid", "hs-sign"))
	if err != nil {
		t.Fatal(err)
	}
	obj, _ := signer.Sign([]byte("from go-jose"))
	compact, _ := obj.CompactSerialize()
	if msg, err := s.Verify(ctx, "msg-hs", compact); err != nil || string(msg.Payload) != "from go-jose" {
		t.Fatalf("go-jose-produced jws: %v", err)
	}
}

// TestRFC7515Vector verifies the RFC 7515 appendix A.1 HS256 example at the
// crypto layer and shows the profile policy rejects it (no kid, no typ
// match), as mandated.
func TestRFC7515Vector(t *testing.T) {
	const token = "eyJ0eXAiOiJKV1QiLA0KICJhbGciOiJIUzI1NiJ9" +
		".eyJpc3MiOiJqb2UiLA0KICJleHAiOjEzMDA4MTkzODAsDQogImh0dHA6Ly9leGFtcGxlLmNvbS9pc19yb290Ijp0cnVlfQ" +
		".dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	k, _ := base64.RawURLEncoding.DecodeString("AyM1SysPpbyDfgZld3umj1qzKObwVMkoqQ-EstJQLr_T-1qS0gZH75aKtMN3Yj0iPS4hcgUuTwjAzZr1Z9CAow")
	obj, err := gojose.ParseSignedCompact(token, []gojose.SignatureAlgorithm{gojose.HS256})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := obj.Verify(k); err != nil {
		t.Fatalf("vector does not verify: %v", err)
	}

	cfg := testConfig(t)
	cfg.Keys[4].Source.File = writeKey(t, t.TempDir(), "vector", octFile(k))
	cfg.Profiles[4].Typ = "JWT"
	s := newService(t, cfg)
	_, err = s.Verify(ctx, "msg-hs", token)
	mustIs(t, err, ErrMalformedToken) // kid is mandatory
}
