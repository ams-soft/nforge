package jose

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/ams-soft/nforge/jose/internal/strictjson"
)

var (
	fuzzOnce    sync.Once
	fuzzService *Service
	fuzzTokens  []string
)

// fuzzSetup builds one service with fixed test keys shared by the fuzzers.
func fuzzSetup(t testing.TB) *Service {
	fuzzOnce.Do(func() {
		dir, err := os.MkdirTemp("", "jose-fuzz")
		if err != nil {
			panic(err)
		}
		cfg := testConfig(&tempDirTB{TB: t, dir: dir})
		fuzzService, err = New(ctx, cfg, newTestClock().Option())
		if err != nil {
			panic(err)
		}
		signed, _ := fuzzService.IssueJWT(ctx, "access", RegisteredClaims{Subject: "u"}, nil)
		nested, _ := fuzzService.IssueJWT(ctx, "nested", RegisteredClaims{Subject: "u"}, nil)
		jws, _ := fuzzService.Sign(ctx, "msg-ed", []byte("x"))
		jwe, _ := fuzzService.Encrypt(ctx, "box-dir", []byte("x"))
		fuzzTokens = []string{signed, nested, jws, jwe}
	})
	return fuzzService
}

// tempDirTB lets testConfig run under a fuzz target's shared setup, whose
// files must outlive the first iteration.
type tempDirTB struct {
	testing.TB
	dir string
}

func (t *tempDirTB) TempDir() string { return t.dir }

func FuzzValidateJWT(f *testing.F) {
	s := fuzzSetup(f)
	for _, tok := range fuzzTokens {
		f.Add(tok)
		f.Add(tok[:len(tok)/2])
	}
	f.Add("a.b.c")
	f.Add(b64([]byte(`{"alg":"EdDSA","kid":"ed-sign","typ":"at+jwt","alg":"none"}`)) + ".e30.")
	f.Add(strings.Repeat("A", 100) + "." + strings.Repeat("B", 100) + ".C")
	f.Fuzz(func(t *testing.T, token string) {
		for _, profile := range []string{"access", "nested"} {
			v, err := s.ValidateJWT(ctx, profile, token, nil)
			if err == nil {
				// only tokens issued by the service may authenticate
				known := false
				for _, tok := range fuzzTokens {
					known = known || tok == token
				}
				if !known {
					t.Fatalf("unknown token authenticated: %q %+v", token, v)
				}
			}
		}
		_, _ = s.Verify(ctx, "msg-ed", token)
		_, _ = s.Decrypt(ctx, "box-dir", token)
		_, _ = s.Decrypt(ctx, "box-rsa", token)
	})
}

func FuzzStrictJSON(f *testing.F) {
	for _, seed := range []string{
		`{}`, `{"a":1}`, `{"a":1,"a":2}`, `{"a":{"b":1,"b":2}}`, `[[[[[[]]]]]]`, "{\"a\":\"\xff\"}",
		`{"a":[1,{"x":1,"x":1}]}`, `{} {}`, `{"a":1}x`, `"str"`, `{"a":1,"a":2}`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		err := strictjson.Check(data, 8)
		if err == nil {
			// anything accepted must be valid for encoding/json too
			if _, err := strictjson.Object(data, 8); err != nil && !errors.Is(err, strictjson.ErrNotObject) {
				t.Fatalf("inconsistent: %v", err)
			}
		}
	})
}

func FuzzParseCompact(f *testing.F) {
	fuzzSetup(f)
	for _, tok := range fuzzTokens {
		f.Add(tok)
	}
	f.Add("..")
	f.Add("....")
	f.Add("e30=.e30.e30")
	limits := LimitsConfig{MaxTokenBytes: 16384, MaxHeaderBytes: 2048, MaxPayloadBytes: 8192, MaxJSONDepth: 16}
	f.Fuzz(func(t *testing.T, token string) {
		for _, rules := range []headerRules{{segments: jwsSegments}, {segments: jweSegments, needEnc: true, allowCty: true}} {
			c, err := parseCompact(token, rules, limits)
			if err != nil {
				continue
			}
			if len(c.segments) != rules.segments || c.header.KeyID == "" || c.header.Algorithm == "" {
				t.Fatalf("accepted an incomplete header: %+v", c.header)
			}
			for i := 0; i < len(token); i++ {
				if !isCompactChar(token[i]) {
					t.Fatal("accepted a non-base64url character")
				}
			}
		}
	})
}

func FuzzKeyImport(f *testing.F) {
	m := testMaterial(f)
	f.Add([]byte(`{"kty":"OKP","crv":"Ed25519","x":"` + b64(m.ed.Public().(ed25519.PublicKey)) + `"}`))
	f.Add([]byte(`{"kty":"oct","k":"` + base64.RawURLEncoding.EncodeToString(m.hs) + `"}`))
	f.Add(pkcs8PEM(f, m.ed))
	f.Add(pkcs8PEM(f, m.rsaPS))
	f.Fuzz(func(t *testing.T, data []byte) {
		for _, alg := range []string{EdDSA, ES256, PS256, HS256} {
			k := &key{kid: "k", alg: alg, ops: []string{OpVerify}}
			if k.parseJWK(data, KeyConfig{KID: "k", Algorithm: alg, Operations: k.ops}, 16) == nil {
				_ = k.check()
			}
			k = &key{kid: "k", alg: alg, ops: []string{OpVerify}}
			if k.parsePEM(data) == nil {
				_ = k.check()
			}
			k = &key{kid: "k", alg: alg, ops: []string{OpVerify}}
			_ = k.parseOct(data)
		}
	})
}
