package jose

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var b64 = base64.RawURLEncoding.EncodeToString

func loadOne(t *testing.T, alg, format string, data []byte, ops ...string) (*key, error) {
	t.Helper()
	if len(ops) == 0 {
		ops = opsFor(alg)
	}
	path := writeKey(t, t.TempDir(), "key", data)
	return loadKey(KeyConfig{KID: "k1", Algorithm: alg, Operations: ops, Source: KeySourceConfig{Format: format, File: path}},
		LimitsConfig{MaxKeyFileBytes: 65536, MaxJSONDepth: 16})
}

func TestLoadPEMFormats(t *testing.T) {
	m := testMaterial(t)
	ecDER, _ := x509.MarshalECPrivateKey(m.ec)
	cases := []struct {
		name, alg string
		data      []byte
		private   bool
	}{
		{"pkcs8 ed25519", EdDSA, pkcs8PEM(t, m.ed), true},
		{"pkcs8 ec", ES256, pkcs8PEM(t, m.ec), true},
		{"sec1 ec", ES256, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: ecDER}), true},
		{"pkcs1 rsa", PS256, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(m.rsaPS)}), true},
		{"pkix ed25519", EdDSA, pkixPEM(t, m.ed.Public()), false},
		{"pkcs1 rsa public", RSAOAEP256, pem.EncodeToMemory(&pem.Block{Type: "RSA PUBLIC KEY", Bytes: x509.MarshalPKCS1PublicKey(&m.rsaEnc.PublicKey)}), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ops := []string{OpVerify}
			if isKeyMgmtAlg(tc.alg) {
				ops = []string{OpEncrypt}
			}
			if tc.private {
				ops = opsFor(tc.alg)
			}
			k, err := loadOne(t, tc.alg, "pem", tc.data, ops...)
			if err != nil {
				t.Fatal(err)
			}
			if (k.private != nil) != tc.private {
				t.Fatalf("private = %v", k.private != nil)
			}
		})
	}
}

func TestLoadPEMRejects(t *testing.T) {
	m := testMaterial(t)
	small, _ := rsa.GenerateKey(rand.Reader, 1024)
	p384, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	good := pkcs8PEM(t, m.ed)
	encrypted := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Headers: map[string]string{"Proc-Type": "4,ENCRYPTED"}, Bytes: []byte{1}})
	cases := []struct {
		name, alg string
		data      []byte
		ops       []string
	}{
		{"two blocks", EdDSA, append(append([]byte{}, good...), good...), nil},
		{"trailing data", EdDSA, append(append([]byte{}, good...), []byte("junk")...), nil},
		{"no pem", EdDSA, []byte("not a key"), nil},
		{"encrypted pem", PS256, encrypted, nil},
		{"encrypted pkcs8", EdDSA, pem.EncodeToMemory(&pem.Block{Type: "ENCRYPTED PRIVATE KEY", Bytes: []byte{1}}), nil},
		{"certificate", EdDSA, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte{1}}), nil},
		{"rsa 1024", PS256, pkcs8PEM(t, small), nil},
		{"p-384", ES256, pkcs8PEM(t, p384), nil},
		{"ed key as ES256", ES256, good, nil},
		{"rsa key as EdDSA", EdDSA, pkcs8PEM(t, m.rsaPS), nil},
		{"public key cannot sign", EdDSA, pkixPEM(t, m.ed.Public()), []string{OpSign}},
		{"public key cannot decrypt", RSAOAEP256, pkixPEM(t, &m.rsaEnc.PublicKey), []string{OpDecrypt}},
		{"pem as HS256", HS256, good, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadOne(t, tc.alg, "pem", tc.data, tc.ops...)
			mustIs(t, err, ErrInvalidKey)
		})
	}
}

func TestLoadOct(t *testing.T) {
	secret := randomBytes(32)
	enc := b64(secret)
	for _, ok := range []string{enc, enc + "\n", enc + "\r\n"} {
		if _, err := loadOne(t, HS256, "oct", []byte(ok)); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for name, bad := range map[string]string{
		"two newlines": enc + "\n\n",
		"padding":      base64.URLEncoding.EncodeToString(randomBytes(31)),
		"std alphabet": strings.ReplaceAll(strings.ReplaceAll(enc, "-", "+"), "_", "/") + "+/",
		"spaces":       " " + enc,
		"short hs256":  b64(randomBytes(16)),
		"empty":        "",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := loadOne(t, HS256, "oct", []byte(bad))
			mustIs(t, err, ErrInvalidKey)
		})
	}
	if _, err := loadOne(t, Dir, "oct", []byte(b64(randomBytes(31)))); !errors.Is(err, ErrInvalidKey) {
		t.Errorf("dir needs exactly 32 bytes: %v", err)
	}
}

func TestKeyFilePermissionsAndType(t *testing.T) {
	m := testMaterial(t)
	dir := t.TempDir()
	limits := LimitsConfig{MaxKeyFileBytes: 65536, MaxJSONDepth: 16}
	cfg := func(path, alg string, ops ...string) KeyConfig {
		return KeyConfig{KID: "k", Algorithm: alg, Operations: ops, Source: KeySourceConfig{Format: "pem", File: path}}
	}

	priv := filepath.Join(dir, "priv.pem")
	if err := os.WriteFile(priv, pkcs8PEM(t, m.ed), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := loadKey(cfg(priv, EdDSA, OpSign), limits)
	mustIs(t, err, ErrInvalidKey)
	if strings.Contains(err.Error(), priv) {
		t.Fatalf("error leaks the path: %v", err)
	}

	pub := filepath.Join(dir, "pub.pem")
	if err := os.WriteFile(pub, pkixPEM(t, m.ed.Public()), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadKey(cfg(pub, EdDSA, OpVerify), limits); err != nil {
		t.Fatalf("public keys may be readable: %v", err)
	}

	// projected secrets are symlinks to owner-only files
	target := writeKey(t, dir, "target.pem", pkcs8PEM(t, m.ed))
	link := filepath.Join(dir, "link.pem")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := loadKey(cfg(link, EdDSA, OpSign), limits); err != nil {
		t.Fatalf("symlink to owner-only file: %v", err)
	}

	_, err = loadKey(cfg(dir, EdDSA, OpSign), limits)
	mustIs(t, err, ErrInvalidKey)

	big := writeKey(t, dir, "big.pem", bytes.Repeat([]byte("a"), 100))
	_, err = loadKey(cfg(big, EdDSA, OpSign), LimitsConfig{MaxKeyFileBytes: 50})
	mustIs(t, err, ErrInvalidKey)

	_, err = loadKey(cfg(filepath.Join(dir, "missing"), EdDSA, OpSign), limits)
	mustIs(t, err, ErrInvalidKey)
}

func jwkJSON(t *testing.T, members map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(members)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestLoadJWK(t *testing.T) {
	m := testMaterial(t)
	ecPub, _ := m.ec.PublicKey.Bytes()
	ecD, _ := m.ec.Bytes()
	r := m.rsaPS
	r.Precompute()
	okp := map[string]any{"kty": "OKP", "crv": "Ed25519", "x": b64(m.ed.Public().(ed25519.PublicKey)), "d": b64(m.ed.Seed())}
	ec := map[string]any{"kty": "EC", "crv": "P-256", "x": b64(ecPub[1:33]), "y": b64(ecPub[33:]), "d": b64(ecD)}
	rsaJWK := map[string]any{
		"kty": "RSA", "n": b64(r.N.Bytes()), "e": b64(big.NewInt(int64(r.E)).Bytes()), "d": b64(r.D.Bytes()),
		"p": b64(r.Primes[0].Bytes()), "q": b64(r.Primes[1].Bytes()),
		"dp": b64(r.Precomputed.Dp.Bytes()), "dq": b64(r.Precomputed.Dq.Bytes()), "qi": b64(r.Precomputed.Qinv.Bytes()),
	}
	with := func(base map[string]any, kv ...any) map[string]any {
		out := map[string]any{}
		for k, v := range base {
			out[k] = v
		}
		for i := 0; i < len(kv); i += 2 {
			if kv[i+1] == nil {
				delete(out, kv[i].(string))
			} else {
				out[kv[i].(string)] = kv[i+1]
			}
		}
		return out
	}

	valid := []struct {
		name, alg string
		jwk       map[string]any
		ops       []string
	}{
		{"okp private", EdDSA, okp, nil},
		{"okp public", EdDSA, with(okp, "d", nil), []string{OpVerify}},
		{"okp with metadata", EdDSA, with(okp, "kid", "k1", "alg", EdDSA, "use", "sig", "key_ops", []string{"sign", "verify"}), nil},
		{"ec private", ES256, ec, nil},
		{"ec public", ES256, with(ec, "d", nil), []string{OpVerify}},
		{"rsa private", PS256, rsaJWK, nil},
		{"rsa without crt", PS256, with(rsaJWK, "dp", nil, "dq", nil, "qi", nil), nil},
		{"rsa public", PS256, with(rsaJWK, "d", nil, "p", nil, "q", nil, "dp", nil, "dq", nil, "qi", nil), []string{OpVerify}},
		{"oct", HS256, map[string]any{"kty": "oct", "k": b64(randomBytes(32))}, nil},
		{"rsa enc with wrapKey", RSAOAEP256, with(rsaJWK, "use", "enc", "key_ops", []string{"wrapKey", "unwrapKey"}), nil},
	}
	for _, tc := range valid {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := loadOne(t, tc.alg, "jwk", jwkJSON(t, tc.jwk), tc.ops...); err != nil {
				t.Fatal(err)
			}
		})
	}

	otherEd, _, _ := ed25519.GenerateKey(rand.Reader)
	invalid := []struct {
		name, alg string
		data      []byte
		ops       []string
	}{
		{"kid mismatch", EdDSA, jwkJSON(t, with(okp, "kid", "other")), nil},
		{"alg mismatch", EdDSA, jwkJSON(t, with(okp, "alg", ES256)), nil},
		{"use mismatch", EdDSA, jwkJSON(t, with(okp, "use", "enc")), nil},
		{"key_ops missing sign", EdDSA, jwkJSON(t, with(okp, "key_ops", []string{"verify"})), nil},
		{"inconsistent okp", EdDSA, jwkJSON(t, with(okp, "x", b64(otherEd))), nil},
		{"ed448", EdDSA, jwkJSON(t, with(okp, "crv", "Ed448")), nil},
		{"x25519", EdDSA, jwkJSON(t, with(okp, "crv", "X25519")), nil},
		{"p-384", ES256, jwkJSON(t, with(ec, "crv", "P-384")), nil},
		{"point off curve", ES256, jwkJSON(t, with(ec, "y", b64(bytes.Repeat([]byte{1}, 32)), "d", nil)), []string{OpVerify}},
		{"inconsistent ec", ES256, jwkJSON(t, with(ec, "d", b64(bytes.Repeat([]byte{7}, 32)))), nil},
		{"rsa bad crt", PS256, jwkJSON(t, with(rsaJWK, "qi", b64([]byte{1, 2, 3}))), nil},
		{"rsa partial crt", PS256, jwkJSON(t, with(rsaJWK, "qi", nil)), nil},
		{"rsa d without primes", PS256, jwkJSON(t, with(rsaJWK, "p", nil, "q", nil)), nil},
		{"rsa wrong d", PS256, jwkJSON(t, with(rsaJWK, "d", b64([]byte{3}), "dp", nil, "dq", nil, "qi", nil)), nil},
		{"unknown kty", EdDSA, jwkJSON(t, with(okp, "kty", "XYZ")), nil},
		{"jwk set", EdDSA, jwkJSON(t, map[string]any{"keys": []any{okp}}), nil},
		{"x5c member", EdDSA, jwkJSON(t, with(okp, "x5c", []string{"a"})), nil},
		{"wrong member for type", EdDSA, jwkJSON(t, with(okp, "n", "AQAB")), nil},
		{"padded base64", EdDSA, jwkJSON(t, with(okp, "x", base64.URLEncoding.EncodeToString(otherEd))), nil},
		{"duplicate member", EdDSA, []byte(`{"kty":"OKP","kty":"OKP","crv":"Ed25519","x":"` + b64(m.ed.Public().(ed25519.PublicKey)) + `"}`), []string{OpVerify}},
		{"not an object", EdDSA, []byte(`[1]`), nil},
		{"trailing data", EdDSA, append(jwkJSON(t, okp), []byte(" {}")...), nil},
		{"oct as EdDSA", EdDSA, jwkJSON(t, map[string]any{"kty": "oct", "k": b64(randomBytes(32))}), nil},
		{"okp as HS256", HS256, jwkJSON(t, okp), nil},
		{"short secret", HS256, jwkJSON(t, map[string]any{"kty": "oct", "k": b64(randomBytes(8))}), nil},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadOne(t, tc.alg, "jwk", tc.data, tc.ops...)
			mustIs(t, err, ErrInvalidKey)
		})
	}
}

func TestDuplicateKeyMaterialRejected(t *testing.T) {
	m := testMaterial(t)
	cfg := testConfig(t)
	// same Ed25519 key under another kid, once as private and once as public
	cfg.Keys = append(cfg.Keys, KeyConfig{KID: "ed-copy", Algorithm: EdDSA, Operations: []string{OpVerify},
		Source: KeySourceConfig{Format: "pem", File: writeKey(t, t.TempDir(), "copy", pkixPEM(t, m.ed.Public()))}})
	_, err := New(context.Background(), cfg)
	mustIs(t, err, ErrInvalidKey)

	// the same secret bytes for signing and encryption
	cfg = testConfig(t)
	cfg.Keys[6].Source.File = writeKey(t, t.TempDir(), "dir", octFile(m.hs))
	_, err = New(context.Background(), cfg)
	mustIs(t, err, ErrInvalidKey)
}

func TestPublicJWKS(t *testing.T) {
	s := newService(t, testConfig(t))
	data, err := s.PublicJWKS(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var set struct {
		Keys []map[string]any `json:"keys"`
	}
	if err := json.Unmarshal(data, &set); err != nil {
		t.Fatal(err)
	}
	var kids []string
	for _, k := range set.Keys {
		kids = append(kids, k["kid"].(string))
		for _, private := range []string{"d", "p", "q", "dp", "dq", "qi", "k", "oth"} {
			if _, ok := k[private]; ok {
				t.Fatalf("private member %q exported: %v", private, k)
			}
		}
	}
	if strings.Join(kids, ",") != "ed-sign,es-sign,rsa-enc" {
		t.Fatalf("kids = %v (only publish=true, sorted)", kids)
	}
	if set.Keys[2]["use"] != "enc" || set.Keys[0]["use"] != "sig" || set.Keys[0]["crv"] != "Ed25519" {
		t.Fatalf("metadata = %v", set.Keys)
	}
	again, _ := s.PublicJWKS(context.Background())
	if !bytes.Equal(data, again) {
		t.Fatal("JWKS bytes must be stable")
	}
	again[0] = 'X'
	if third, _ := s.PublicJWKS(context.Background()); third[0] != '{' {
		t.Fatal("PublicJWKS must return a copy")
	}

	// the exported keys round-trip as public-only key sources
	dir := t.TempDir()
	for i, k := range set.Keys {
		raw, _ := json.Marshal(k)
		ops := []string{OpVerify}
		if k["use"] == "enc" {
			ops = []string{OpEncrypt}
		}
		path := filepath.Join(dir, k["kid"].(string))
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := loadKey(KeyConfig{KID: k["kid"].(string), Algorithm: k["alg"].(string), Operations: ops,
			Source: KeySourceConfig{Format: "jwk", File: path}}, LimitsConfig{MaxKeyFileBytes: 65536, MaxJSONDepth: 16})
		if err != nil {
			t.Fatalf("key %d does not reimport: %v", i, err)
		}
	}
}

func TestEmptyJWKS(t *testing.T) {
	cfg := testConfig(t)
	for i := range cfg.Keys {
		cfg.Keys[i].Publish = false
	}
	s := newService(t, cfg)
	data, _ := s.PublicJWKS(context.Background())
	if string(data) != `{"keys":[]}` {
		t.Fatalf("jwks = %s", data)
	}
}
