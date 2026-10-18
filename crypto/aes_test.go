package ncrypto

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"testing"
)

func TestAES(t *testing.T) {
	key := bytes.Repeat([]byte{1}, 32)
	c, err := NewAES(key)
	if err != nil {
		t.Fatal(err)
	}
	for _, plain := range [][]byte{nil, []byte("segredo")} {
		aad := []byte("tenant:123")
		enc, err := c.Encrypt(plain, aad)
		if err != nil {
			t.Fatal(err)
		}
		if len(enc) != len(plain)+28 {
			t.Fatal("invalid wire length")
		}
		enc2, err := c.Encrypt(plain, aad)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(enc, enc2) {
			t.Fatal("nonce reused")
		}
		got, err := c.Decrypt(enc, aad)
		if err != nil || !bytes.Equal(got, plain) {
			t.Fatal("roundtrip failed", err)
		}
		if _, err := c.Decrypt(enc, []byte("wrong")); err == nil {
			t.Fatal("wrong AAD accepted")
		}
		other, err := NewAES(bytes.Repeat([]byte{2}, 32))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := other.Decrypt(enc, aad); err == nil {
			t.Fatal("wrong key accepted")
		}
		// Verify compatibility with standard AES-GCM's explicit nonce API.
		block, err := aes.NewCipher(key)
		if err != nil {
			t.Fatal(err)
		}
		gcm, err := cipher.NewGCM(block)
		if err != nil {
			t.Fatal(err)
		}
		got, err = gcm.Open(nil, enc[:12], enc[12:], aad)
		if err != nil || !bytes.Equal(got, plain) {
			t.Fatal("interoperability failed", err)
		}
		enc[len(enc)-1] ^= 1
		if _, err := c.Decrypt(enc, aad); err == nil {
			t.Fatal("tampering accepted")
		}
	}
	if _, err := c.Decrypt(make([]byte, 27), nil); err == nil {
		t.Fatal("truncated input accepted")
	}
	for _, n := range []int{0, 16, 24, 31, 33} {
		if _, err := NewAES(make([]byte, n)); err == nil {
			t.Fatal("invalid key accepted", n)
		}
	}
}
