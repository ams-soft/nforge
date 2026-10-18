package jose

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Key generation (RSA especially) is slow, so the material is generated once
// per test binary and written to a fresh directory per test.
type keyMaterial struct {
	ed, ed2         ed25519.PrivateKey
	ec              *ecdsa.PrivateKey
	rsaPS, rsaRS    *rsa.PrivateKey
	rsaEnc, rsaEnc2 *rsa.PrivateKey
	hs, dir, dir2   []byte
}

var (
	materialOnce sync.Once
	material     keyMaterial
)

func testMaterial(t testing.TB) keyMaterial {
	t.Helper()
	materialOnce.Do(func() {
		var err error
		_, material.ed, _ = ed25519.GenerateKey(rand.Reader)
		_, material.ed2, _ = ed25519.GenerateKey(rand.Reader)
		if material.ec, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader); err != nil {
			panic(err)
		}
		for _, k := range []**rsa.PrivateKey{&material.rsaPS, &material.rsaRS, &material.rsaEnc, &material.rsaEnc2} {
			if *k, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
				panic(err)
			}
		}
		material.hs = randomBytes(32)
		material.dir = randomBytes(32)
		material.dir2 = randomBytes(32)
	})
	return material
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

func pkcs8PEM(t testing.TB, priv any) []byte {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

func pkixPEM(t testing.TB, pub any) []byte {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
}

func octFile(secret []byte) []byte {
	return []byte(base64.RawURLEncoding.EncodeToString(secret) + "\n")
}

func writeKey(t testing.TB, dir, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const (
	testIssuer = "https://issuer.test"
	testAud    = "api"
)

// testConfig builds a config exercising every algorithm and profile kind.
func testConfig(t testing.TB) Config {
	t.Helper()
	m := testMaterial(t)
	dir := t.TempDir()
	key := func(kid, alg, format string, data []byte, publish bool, ops ...string) KeyConfig {
		return KeyConfig{KID: kid, Algorithm: alg, Operations: ops, Publish: publish,
			Source: KeySourceConfig{Format: format, File: writeKey(t, dir, kid, data)}}
	}
	jws := func(name, typ, alg, kid string) ProfileConfig {
		return ProfileConfig{Name: name, Kind: KindJWS, Typ: typ, SigningAlgorithm: alg, SigningKey: kid,
			VerificationAlgorithms: []string{alg}, VerificationKeys: []string{kid}}
	}
	jwe := func(name, alg, kid string) ProfileConfig {
		return ProfileConfig{Name: name, Kind: KindJWE, Typ: "box+jwe", EncryptionAlgorithm: alg, EncryptionKey: kid,
			DecryptionAlgorithms: []string{alg}, DecryptionKeys: []string{kid}}
	}
	return Config{
		Enabled: true,
		Keys: []KeyConfig{
			key("ed-sign", EdDSA, "pem", pkcs8PEM(t, m.ed), true, OpSign, OpVerify),
			key("es-sign", ES256, "pem", pkcs8PEM(t, m.ec), true, OpSign, OpVerify),
			key("ps-sign", PS256, "pem", pkcs8PEM(t, m.rsaPS), false, OpSign, OpVerify),
			key("rs-sign", RS256, "pem", pkcs8PEM(t, m.rsaRS), false, OpSign, OpVerify),
			key("hs-sign", HS256, "oct", octFile(m.hs), false, OpSign, OpVerify),
			key("rsa-enc", RSAOAEP256, "pem", pkcs8PEM(t, m.rsaEnc), true, OpEncrypt, OpDecrypt),
			key("dir-enc", Dir, "oct", octFile(m.dir), false, OpEncrypt, OpDecrypt),
		},
		Profiles: []ProfileConfig{
			jws("msg-ed", "msg+jws", EdDSA, "ed-sign"),
			jws("msg-es", "msg+jws", ES256, "es-sign"),
			jws("msg-ps", "msg+jws", PS256, "ps-sign"),
			jws("msg-rs", "msg+jws", RS256, "rs-sign"),
			jws("msg-hs", "msg+jws", HS256, "hs-sign"),
			jwe("box-rsa", RSAOAEP256, "rsa-enc"),
			jwe("box-dir", Dir, "dir-enc"),
			{
				Name: "access", Kind: KindJWTSigned, Typ: "at+jwt",
				SigningAlgorithm: EdDSA, SigningKey: "ed-sign",
				VerificationAlgorithms: []string{EdDSA}, VerificationKeys: []string{"ed-sign"},
				JWT: JWTConfig{Issuer: testIssuer, Audiences: []string{testAud, "other"}},
			},
			{
				Name: "refresh", Kind: KindJWTSigned, Typ: "rt+jwt",
				SigningAlgorithm: EdDSA, SigningKey: "ed-sign",
				VerificationAlgorithms: []string{EdDSA}, VerificationKeys: []string{"ed-sign"},
				JWT: JWTConfig{Issuer: testIssuer, Audiences: []string{"refresh"}, TTL: time.Hour},
			},
			{
				Name: "nested", Kind: KindJWTNested, Typ: "secret+jwt",
				SigningAlgorithm: EdDSA, SigningKey: "ed-sign",
				VerificationAlgorithms: []string{EdDSA}, VerificationKeys: []string{"ed-sign"},
				EncryptionAlgorithm: RSAOAEP256, EncryptionKey: "rsa-enc",
				DecryptionAlgorithms: []string{RSAOAEP256}, DecryptionKeys: []string{"rsa-enc"},
				JWT: JWTConfig{Issuer: testIssuer, Audiences: []string{"secret-api"}, TTL: 5 * time.Minute},
			},
		},
	}
}

// testClock is a settable clock shared by a service under test.
type testClock struct{ now atomic.Int64 }

var baseTime = time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)

func newTestClock() *testClock {
	c := &testClock{}
	c.now.Store(baseTime.Unix())
	return c
}

func (c *testClock) Now() time.Time          { return time.Unix(c.now.Load(), 0) }
func (c *testClock) Set(t time.Time)         { c.now.Store(t.Unix()) }
func (c *testClock) Add(d time.Duration)     { c.now.Add(int64(d / time.Second)) }
func (c *testClock) Option() Option          { return WithClock(c.Now) }
func (c *testClock) At(secs int64) time.Time { return baseTime.Add(time.Duration(secs) * time.Second) }

func newService(t testing.TB, cfg Config, opts ...Option) *Service {
	t.Helper()
	s, err := New(context.Background(), cfg, opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })
	return s
}

func mustIs(t testing.TB, err, target error) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %v, got nil", target)
	}
	if !errors.Is(err, target) {
		t.Fatalf("expected %v, got %v", target, err)
	}
}
