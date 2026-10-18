package ncrypto

import (
	"crypto/aes"
	"crypto/cipher"
	"errors"
)

var (
	// ErrAESKey reports a key that is not exactly 32 bytes.
	ErrAESKey = errors.New("ncrypto: AES-256 requires a 32-byte key")
	// ErrMessageTooLarge reports a plaintext over the GCM per-message limit.
	ErrMessageTooLarge = errors.New("ncrypto: message exceeds AES-GCM size limit")
)

// AES implements AES-256-GCM with automatic random 96-bit nonces.
// Construct with NewAES; its zero value is not usable.
// Rotate the key before exceeding 2^32 encryptions across ALL instances
// sharing that key. This type does not track the global encryption count.
type AES struct {
	aead cipher.AEAD
}

var _ Crypto = (*AES)(nil)

// NewAES builds the cipher from a 32-byte key (see Key); other sizes, which
// would select AES-128/192, return ErrAESKey.
func NewAES(key []byte) (*AES, error) {
	if len(key) != 32 {
		return nil, ErrAESKey
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, err
	}
	return &AES{aead: aead}, nil
}

// Encrypt returns nonce (12 bytes) || ciphertext || tag (16 bytes).
// aad is optional authenticated data; use nil when not needed.
func (a *AES) Encrypt(plaintext, aad []byte) ([]byte, error) {
	// GCM allows at most (2^32 - 2) blocks per message.
	if uint64(len(plaintext)) > ((uint64(1)<<32)-2)*aes.BlockSize {
		return nil, ErrMessageTooLarge
	}
	// Random-nonce AEAD requires a nil nonce and prepends it automatically.
	return a.aead.Seal(nil, nil, plaintext, aad), nil
}

// Decrypt opens the output of Encrypt; aad must match the one used to encrypt.
func (a *AES) Decrypt(encrypted, aad []byte) ([]byte, error) {
	if len(encrypted) < a.aead.Overhead() {
		return nil, ErrCiphertext
	}
	return a.aead.Open(nil, nil, encrypted, aad)
}
