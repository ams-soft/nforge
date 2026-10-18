package ncrypto

import (
	"bytes"
	"math"
	"strings"
	"testing"

	"github.com/oklog/ulid/v2"
)

func TestCrypto(t *testing.T) {
	key, err := Key()
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewChaCha20(key)
	if err != nil {
		t.Fatal(err)
	}
	plain, aad := []byte("segredo"), []byte("tenant:123")
	enc, err := c.Encrypt(plain, aad)
	if err != nil {
		t.Fatal(err)
	}
	other, err := c.Encrypt(plain, aad)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(enc, other) {
		t.Fatal("nonce reused")
	}
	got, err := c.Decrypt(enc, aad)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("roundtrip: %q %v", got, err)
	}
	if _, err := c.Decrypt(enc, []byte("tenant:456")); err == nil {
		t.Fatal("wrong AAD accepted")
	}
	enc[len(enc)-1] ^= 1
	if _, err := c.Decrypt(enc, aad); err == nil {
		t.Fatal("tampering accepted")
	}
	if _, err := c.Decrypt(nil, aad); err == nil {
		t.Fatal("truncated ciphertext accepted")
	}
	if _, err := NewChaCha20(key[:31]); err == nil {
		t.Fatal("invalid key accepted")
	}
}

func TestMAC(t *testing.T) {
	key := bytes.Repeat([]byte{1}, 32)
	m, err := NewHMAC256(key)
	if err != nil {
		t.Fatal(err)
	}
	sig := m.Sign([]byte("hello"))
	key[0] ^= 1 // Constructor must own its key copy.
	if !m.Verify([]byte("hello"), sig) {
		t.Fatal("valid MAC rejected")
	}
	if m.Verify([]byte("changed"), sig) {
		t.Fatal("changed data accepted")
	}
	sig[0] ^= 1
	if m.Verify([]byte("hello"), sig) {
		t.Fatal("changed MAC accepted")
	}
}

func TestPasswordHashes(t *testing.T) {
	h, err := NewHash(HashConfig{BcryptCost: 10, ArgonMemory: 19 * 1024, ArgonTime: 2, ArgonThreads: 1})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		generate func(string) (string, error)
		verify   func(string, string) (bool, error)
	}{
		{"bcrypt", h.Bcrypt, h.VerifyBcrypt},
		{"argon2id", h.Argon2id, h.VerifyArgon2id},
	} {
		t.Run(tc.name, func(t *testing.T) {
			encoded, err := tc.generate("senha de teste")
			if err != nil {
				t.Fatal(err)
			}
			ok, err := tc.verify("senha de teste", encoded)
			if err != nil || !ok {
				t.Fatalf("valid password: %v %v", ok, err)
			}
			ok, err = tc.verify("errada", encoded)
			if err != nil || ok {
				t.Fatalf("wrong password: %v %v", ok, err)
			}
		})
	}
	if _, err := h.Bcrypt(string(bytes.Repeat([]byte{'a'}, 73))); err == nil {
		t.Fatal("long bcrypt password accepted")
	}
	if _, err := h.VerifyArgon2id("x", "$argon2id$v=19$m=4294967295,t=3,p=4$abc$abc"); err == nil {
		t.Fatal("unbounded memory accepted")
	}
	if got := h.Blake3(nil); got != "af1349b9f5f9a1a6a0404dea36dcc9499bcb25c9adc112b7cc9a93cae41f3262" {
		t.Fatalf("BLAKE3 vector: %s", got)
	}
}

func TestRandomBoundaries(t *testing.T) {
	if _, err := Int64(-1, 1); err == nil {
		t.Fatal("reversed bounds accepted")
	}
	if got, err := Int64(-42, -42); err != nil || got != -42 {
		t.Fatal(got, err)
	}
	for i := 0; i < 100; i++ {
		v, err := Int64(9, -9)
		if err != nil || v < -9 || v > 9 {
			t.Fatal(v, err)
		}
		if _, err := Int64(math.MaxInt64, math.MinInt64); err != nil {
			t.Fatal(err)
		}
		if _, err := Uint64(math.MaxUint64, 0); err != nil {
			t.Fatal(err)
		}
		f, err := Float64(math.MaxFloat64, -math.MaxFloat64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f >= math.MaxFloat64 || f < -math.MaxFloat64 {
			t.Fatal(f, err)
		}
		f32, err := Float32(1, -1)
		if err != nil || f32 < -1 || f32 >= 1 {
			t.Fatal(f32, err)
		}
	}
	if _, err := Float64(math.Inf(1), 0); err == nil {
		t.Fatal("infinite bound accepted")
	}
	s, err := String(1000)
	if err != nil || len(s) != 1000 {
		t.Fatal(len(s), err)
	}
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	for _, c := range s {
		if !strings.ContainsRune(alphabet, c) {
			t.Fatal("invalid character")
		}
	}
	id, err := ULID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ulid.ParseStrict(id); err != nil {
		t.Fatal(err)
	}
}

func TestSHA256(t *testing.T) {
	h, err := NewHash(HashConfig{})
	if err != nil {
		t.Fatal(err)
	}
	const want = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if got := h.SHA256([]byte("abc")); got != want {
		t.Fatalf("SHA256 vector: %s", got)
	}
	ok, err := h.VerifySHA256([]byte("abc"), want)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	ok, err = h.VerifySHA256([]byte("changed"), want)
	if err != nil || ok {
		t.Fatal(ok, err)
	}
	if _, err := h.VerifySHA256(nil, "invalid"); err == nil {
		t.Fatal("malformed digest accepted")
	}
}

func TestErrorPrefix(t *testing.T) {
	for _, err := range []error{ErrCiphertext, ErrHash, ErrKey, ErrConfig, ErrAESKey, ErrMessageTooLarge, ErrBounds, ErrSize} {
		if !strings.HasPrefix(err.Error(), "ncrypto: ") {
			t.Errorf("%q lacks the package prefix", err)
		}
	}
}
