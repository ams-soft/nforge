package jose

import (
	"strings"
	"testing"

	gojose "github.com/go-jose/go-jose/v4"
)

func TestEncryptDecrypt(t *testing.T) {
	s := newService(t, testConfig(t))
	for _, profile := range []string{"box-rsa", "box-dir"} {
		t.Run(profile, func(t *testing.T) {
			a, err := s.Encrypt(ctx, profile, []byte("secret"))
			if err != nil {
				t.Fatal(err)
			}
			b, _ := s.Encrypt(ctx, profile, []byte("secret"))
			pa, pb := strings.Split(a, "."), strings.Split(b, ".")
			if pa[2] == pb[2] || pa[3] == pb[3] {
				t.Fatal("repeated encryption must use fresh IVs and produce different ciphertexts")
			}
			msg, err := s.Decrypt(ctx, profile, a)
			if err != nil {
				t.Fatal(err)
			}
			if string(msg.Plaintext) != "secret" || msg.Header.Encryption != A256GCM || msg.Header.Type != "box+jwe" || msg.Header.ContentType != "" {
				t.Fatalf("msg = %+v", msg)
			}
			for i := range 5 {
				if profile == "box-dir" && i == 1 {
					continue // empty encrypted key: covered below
				}
				_, err := s.Decrypt(ctx, profile, splice(a, i, flip(pa[i])))
				if err == nil {
					t.Fatalf("segment %d tampering accepted", i)
				}
			}
		})
	}
}

func TestDecryptRejects(t *testing.T) {
	cfg := testConfig(t)
	m := testMaterial(t)
	// a second dir key under its own profile, to prove keys of other profiles are not tried
	cfg.Keys = append(cfg.Keys, KeyConfig{KID: "dir-2", Algorithm: Dir, Operations: []string{OpEncrypt, OpDecrypt},
		Source: KeySourceConfig{Format: "oct", File: writeKey(t, t.TempDir(), "dir2", octFile(m.dir2))}})
	cfg.Profiles = append(cfg.Profiles, ProfileConfig{Name: "box-dir2", Kind: KindJWE, Typ: "box+jwe",
		EncryptionAlgorithm: Dir, EncryptionKey: "dir-2", DecryptionAlgorithms: []string{Dir}, DecryptionKeys: []string{"dir-2"}})
	s := newService(t, cfg)

	dirTok, _ := s.Encrypt(ctx, "box-dir", []byte("secret"))
	rsaTok, _ := s.Encrypt(ctx, "box-rsa", []byte("secret"))
	other, _ := s.Encrypt(ctx, "box-dir2", []byte("secret"))
	p := strings.Split(dirTok, ".")
	hdr := func(members map[string]any) string { return splice(dirTok, 0, header(t, members)) }

	cases := []struct {
		name, profile, token string
		want                 error
	}{
		{"other profile key", "box-dir", other, ErrUnknownKey},
		{"rsa token on dir profile", "box-dir", rsaTok, ErrUnsupportedAlgorithm},
		{"wrong enc", "box-dir", hdr(map[string]any{"alg": "dir", "enc": "A128GCM", "kid": "dir-enc", "typ": "box+jwe"}), ErrUnsupportedAlgorithm},
		{"cbc enc", "box-dir", hdr(map[string]any{"alg": "dir", "enc": "A128CBC-HS256", "kid": "dir-enc", "typ": "box+jwe"}), ErrUnsupportedAlgorithm},
		{"pbes2", "box-dir", hdr(map[string]any{"alg": "PBES2-HS256+A128KW", "enc": "A256GCM", "kid": "dir-enc", "typ": "box+jwe", "p2c": 1}), ErrMalformedToken},
		{"pbes2 alg only", "box-dir", hdr(map[string]any{"alg": "PBES2-HS256+A128KW", "enc": "A256GCM", "kid": "dir-enc", "typ": "box+jwe"}), ErrUnsupportedAlgorithm},
		{"rsa1_5", "box-rsa", splice(rsaTok, 0, header(t, map[string]any{"alg": "RSA1_5", "enc": "A256GCM", "kid": "rsa-enc", "typ": "box+jwe"})), ErrUnsupportedAlgorithm},
		{"zip", "box-dir", hdr(map[string]any{"alg": "dir", "enc": "A256GCM", "kid": "dir-enc", "typ": "box+jwe", "zip": "DEF"}), ErrMalformedToken},
		{"cty on generic jwe", "box-dir", hdr(map[string]any{"alg": "dir", "enc": "A256GCM", "kid": "dir-enc", "typ": "box+jwe", "cty": "JWT"}), ErrMalformedToken},
		{"missing enc", "box-dir", hdr(map[string]any{"alg": "dir", "kid": "dir-enc", "typ": "box+jwe"}), ErrMalformedToken},
		{"modified header", "box-dir", splice(dirTok, 0, b64([]byte(`{"typ":"box+jwe","alg":"dir","enc":"A256GCM","kid":"dir-enc"}`))), ErrDecryption},
		{"encrypted key with dir", "box-dir", splice(dirTok, 1, b64(make([]byte, 32))), ErrMalformedToken},
		{"wrong encrypted key length", "box-rsa", splice(rsaTok, 1, b64(make([]byte, 100))), ErrMalformedToken},
		{"short iv", "box-dir", splice(dirTok, 2, b64(make([]byte, 8))), ErrMalformedToken},
		{"short tag", "box-dir", splice(dirTok, 4, b64(make([]byte, 8))), ErrMalformedToken},
		{"modified tag", "box-dir", splice(dirTok, 4, flip(p[4])), ErrDecryption},
		{"modified iv", "box-dir", splice(dirTok, 2, flip(p[2])), ErrDecryption},
		{"modified ciphertext", "box-dir", splice(dirTok, 3, flip(p[3])), ErrDecryption},
		{"jws given", "box-dir", "a.b.c", ErrMalformedToken},
		{"six segments", "box-dir", dirTok + ".x", ErrMalformedToken},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg, err := s.Decrypt(ctx, tc.profile, tc.token)
			if msg != nil {
				t.Fatal("message returned on failure")
			}
			mustIs(t, err, tc.want)
		})
	}
}

func TestDecryptExternallyProducedJWE(t *testing.T) {
	m := testMaterial(t)
	s := newService(t, testConfig(t))
	enc, err := gojose.NewEncrypter(gojose.A256GCM,
		gojose.Recipient{Algorithm: gojose.RSA_OAEP_256, Key: &m.rsaEnc.PublicKey, KeyID: "rsa-enc"},
		(&gojose.EncrypterOptions{}).WithType("box+jwe"))
	if err != nil {
		t.Fatal(err)
	}
	obj, _ := enc.Encrypt([]byte("from go-jose"))
	compact, _ := obj.CompactSerialize()
	msg, err := s.Decrypt(ctx, "box-rsa", compact)
	if err != nil || string(msg.Plaintext) != "from go-jose" {
		t.Fatalf("decrypt external: %v", err)
	}

	// compression is never accepted, even when authentic
	zipped, _ := gojose.NewEncrypter(gojose.A256GCM,
		gojose.Recipient{Algorithm: gojose.RSA_OAEP_256, Key: &m.rsaEnc.PublicKey, KeyID: "rsa-enc"},
		&gojose.EncrypterOptions{Compression: gojose.DEFLATE, ExtraHeaders: map[gojose.HeaderKey]any{"typ": "box+jwe"}})
	zobj, _ := zipped.Encrypt([]byte("compressed"))
	zcompact, _ := zobj.CompactSerialize()
	_, err = s.Decrypt(ctx, "box-rsa", zcompact)
	mustIs(t, err, ErrMalformedToken)
}
