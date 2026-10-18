package ncrypto

import (
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/zeebo/blake3"
	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/crypto/chacha20poly1305"
)

// Sentinel errors, matched with errors.Is. AEAD authentication failures
// (wrong key, tampering, wrong aad) are the cipher's own opaque error.
var (
	// ErrCiphertext reports input shorter than a nonce plus a tag.
	ErrCiphertext = errors.New("ncrypto: invalid ciphertext")
	// ErrHash reports a malformed encoded hash or one whose parameters exceed
	// the verification ceilings.
	ErrHash = errors.New("ncrypto: invalid hash or parameters exceed limits")
	// ErrKey reports an HMAC key shorter than 32 bytes.
	ErrKey = errors.New("ncrypto: HMAC key must contain at least 32 bytes")
	// ErrConfig reports a HashConfig outside the accepted ranges.
	ErrConfig = errors.New("ncrypto: invalid hash configuration")
)

// Crypto encrypts/decrypts binary data. aad is authenticated but not encrypted;
// the same aad must be provided for decryption. Use nil when not needed.
type Crypto interface {
	Encrypt(plaintext, aad []byte) ([]byte, error)
	Decrypt(encrypted, aad []byte) ([]byte, error)
}

// ChaCha20 uses XChaCha20-Poly1305 (24-byte nonce, 32-byte key).
// It is NOT wire-compatible with the 12-byte-nonce standard variant.
type ChaCha20 struct{ aead cipher.AEAD }

var _ Crypto = (*ChaCha20)(nil)

// NewChaCha20 builds the cipher from a 32-byte key (see Key).
func NewChaCha20(key []byte) (*ChaCha20, error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	return &ChaCha20{aead: aead}, nil
}

// Encrypt returns nonce || ciphertext || authentication tag.
// Each call generates a fresh random nonce; the nonce is not secret.
func (c *ChaCha20) Encrypt(plaintext, aad []byte) ([]byte, error) {
	nonce := make([]byte, c.aead.NonceSize(), c.aead.NonceSize()+len(plaintext)+c.aead.Overhead())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return c.aead.Seal(nonce, nonce, plaintext, aad), nil
}

// Decrypt opens the output of Encrypt; aad must match the one used to encrypt.
func (c *ChaCha20) Decrypt(encrypted, aad []byte) ([]byte, error) {
	n := c.aead.NonceSize()
	if len(encrypted) < n+c.aead.Overhead() {
		return nil, ErrCiphertext
	}
	return c.aead.Open(nil, encrypted[:n], encrypted[n:], aad)
}

// MAC authenticates messages; it cannot encrypt or decrypt them.
type MAC interface {
	Sign(data []byte) []byte
	Verify(data, signature []byte) bool
}

// HMAC256 is HMAC-SHA256. Use a dedicated random key, distinct from encryption.
type HMAC256 struct{ key []byte }

var _ MAC = (*HMAC256)(nil)

// NewHMAC256 copies the key, so later changes to the caller's slice do not
// affect the MAC. Keys shorter than 32 bytes return ErrKey.
func NewHMAC256(key []byte) (*HMAC256, error) {
	if len(key) < 32 {
		return nil, ErrKey
	}
	return &HMAC256{key: append([]byte(nil), key...)}, nil
}

// Sign returns the 32-byte HMAC-SHA256 tag of data.
func (h *HMAC256) Sign(data []byte) []byte {
	mac := hmac.New(sha256.New, h.key)
	_, _ = mac.Write(data)
	return mac.Sum(nil)
}

// Verify compares the tag in constant time.
func (h *HMAC256) Verify(data, signature []byte) bool {
	return hmac.Equal(h.Sign(data), signature)
}

// HashConfig zero fields receive defaults. Verification has fixed ceilings:
// bcrypt cost <= 16; Argon2 memory <= 256 MiB, passes <= 10, threads <= 16.
// Stored hashes are expected to come from your database, not client input.
type HashConfig struct {
	BcryptCost   int
	ArgonMemory  uint32 // KiB, NOT bytes
	ArgonTime    uint32
	ArgonThreads uint8
}

// Hash must be constructed with NewHash. Configuration remains immutable.
// All methods can be used concurrently, but password hashing is expensive:
// bound concurrent password operations in the calling application.
type Hash struct{ config HashConfig }

// NewHash applies the defaults (bcrypt cost 12; Argon2id 64 MiB, 3 passes,
// 4 threads) and rejects values outside the verification ceilings, so every
// hash it produces can also be verified.
func NewHash(config HashConfig) (*Hash, error) {
	if config.BcryptCost == 0 {
		config.BcryptCost = 12
	}
	if config.ArgonMemory == 0 {
		config.ArgonMemory = 64 * 1024
	}
	if config.ArgonTime == 0 {
		config.ArgonTime = 3
	}
	if config.ArgonThreads == 0 {
		config.ArgonThreads = 4
	}
	if config.BcryptCost < 10 || config.BcryptCost > 16 ||
		!validArgon(config.ArgonMemory, config.ArgonTime, config.ArgonThreads) {
		return nil, ErrConfig
	}
	return &Hash{config: config}, nil
}

// Bcrypt returns the standard encoded bcrypt hash, including cost and salt.
// Passwords longer than 72 BYTES are rejected, never truncated.
func (h *Hash) Bcrypt(password string) (string, error) {
	encoded, err := bcrypt.GenerateFromPassword([]byte(password), h.config.BcryptCost)
	return string(encoded), err
}

// VerifyBcrypt returns false,nil for a wrong password; malformed/unsupported
// hashes and passwords over 72 bytes return an error.
func (h *Hash) VerifyBcrypt(password, encoded string) (bool, error) {
	if len(password) > 72 {
		return false, bcrypt.ErrPasswordTooLong
	}
	if len(encoded) != 60 {
		return false, ErrHash
	}
	cost, err := bcrypt.Cost([]byte(encoded))
	if err != nil {
		return false, err
	}
	if cost > 16 {
		return false, ErrHash
	}
	err = bcrypt.CompareHashAndPassword([]byte(encoded), []byte(password))
	if errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
		return false, nil
	}
	return err == nil, err
}

// Argon2id returns a PHC string containing version, parameters, salt and hash.
func (h *Hash) Argon2id(password string) (string, error) {
	var salt [16]byte
	if _, err := rand.Read(salt[:]); err != nil {
		return "", err
	}
	c := h.config
	key := argon2.IDKey([]byte(password), salt[:], c.ArgonTime, c.ArgonMemory, c.ArgonThreads, 32)
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, c.ArgonMemory, c.ArgonTime, c.ArgonThreads,
		b64.EncodeToString(salt[:]), b64.EncodeToString(key)), nil
}

func validArgon(memory, passes uint32, threads uint8) bool {
	return threads >= 1 && threads <= 16 &&
		passes >= 1 && passes <= 10 &&
		memory >= 8*uint32(threads) && memory <= 256*1024
}

// VerifyArgon2id validates the encoded parameters before allocating memory.
// A mismatching password returns false,nil.
func (h *Hash) VerifyArgon2id(password, encoded string) (bool, error) {
	if len(encoded) > 512 {
		return false, ErrHash
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" ||
		parts[2] != "v="+strconv.Itoa(argon2.Version) {
		return false, ErrHash
	}
	params := strings.Split(parts[3], ",")
	if len(params) != 3 {
		return false, ErrHash
	}
	parse := func(s, prefix string, bits int) (uint64, error) {
		if !strings.HasPrefix(s, prefix) {
			return 0, ErrHash
		}
		value := strings.TrimPrefix(s, prefix)
		if value == "" {
			return 0, ErrHash
		}
		for _, c := range value {
			if c < '0' || c > '9' {
				return 0, ErrHash
			}
		}
		return strconv.ParseUint(value, 10, bits)
	}
	memory, err := parse(params[0], "m=", 32)
	if err != nil {
		return false, ErrHash
	}
	passes, err := parse(params[1], "t=", 32)
	if err != nil {
		return false, ErrHash
	}
	threads, err := parse(params[2], "p=", 8)
	if err != nil || !validArgon(uint32(memory), uint32(passes), uint8(threads)) {
		return false, ErrHash
	}
	b64 := base64.RawStdEncoding.Strict()
	salt, err := b64.DecodeString(parts[4])
	if err != nil || len(salt) < 16 || len(salt) > 64 {
		return false, ErrHash
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil || len(want) < 16 || len(want) > 64 {
		return false, ErrHash
	}
	got := argon2.IDKey([]byte(password), salt, uint32(passes), uint32(memory), uint8(threads), uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// Blake3 returns a 256-bit data hash as 64 hexadecimal characters.
// This fast unkeyed hash is NOT suitable for storing passwords or signing data.
func (h *Hash) Blake3(data []byte) string {
	sum := blake3.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// VerifyBlake3 compares in constant time; a malformed encoding returns ErrHash.
func (h *Hash) VerifyBlake3(data []byte, encoded string) (bool, error) {
	if len(encoded) != 64 {
		return false, ErrHash
	}
	want, err := hex.DecodeString(encoded)
	if err != nil {
		return false, ErrHash
	}
	got := blake3.Sum256(data)
	return subtle.ConstantTimeCompare(got[:], want) == 1, nil
}

// SHA256 returns a 256-bit data hash as 64 hexadecimal characters.
// Like unkeyed BLAKE3, it is not password hashing or message authentication.
func (h *Hash) SHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// VerifySHA256 compares in constant time; a malformed encoding returns ErrHash.
func (h *Hash) VerifySHA256(data []byte, encoded string) (bool, error) {
	if len(encoded) != 64 {
		return false, ErrHash
	}
	want, err := hex.DecodeString(encoded)
	if err != nil {
		return false, ErrHash
	}
	got := sha256.Sum256(data)
	return subtle.ConstantTimeCompare(got[:], want) == 1, nil
}
