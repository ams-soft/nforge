package ncrypto

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"math"
	"time"

	"github.com/oklog/ulid/v2"
)

var (
	// ErrBounds reports min > max, or a NaN or infinite float bound.
	ErrBounds = errors.New("ncrypto: invalid bounds")
	// ErrSize reports a negative length.
	ErrSize = errors.New("ncrypto: invalid size")
)

// Bytes returns n random bytes. Zero is allowed.
func Bytes(n int) ([]byte, error) {
	if n < 0 {
		return nil, ErrSize
	}
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
}

// Key returns a 256-bit key suitable for (X)ChaCha20-Poly1305 or AES-256.
// Keep the key as raw bytes; Base64/hex are storage encodings only.
func Key() ([]byte, error) { return Bytes(32) }

func rawUint64() (uint64, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint64(b[:]), nil
}

// below samples [0, n) without modulo bias. n == 0 means all 2^64 values.
func below(n uint64) (uint64, error) {
	if n == 0 {
		return rawUint64()
	}
	// Unsigned arithmetic deliberately wraps modulo 2^64.
	threshold := -n % n
	for {
		x, err := rawUint64()
		if err != nil {
			return 0, err
		}
		if x >= threshold {
			return x % n, nil
		}
	}
}

// Uint64 returns an unbiased value in [min, max]. Note the (max, min) order.
func Uint64(max, min uint64) (uint64, error) {
	if min > max {
		return 0, ErrBounds
	}
	if min == max {
		return min, nil
	}
	x, err := below(max - min + 1)
	if err != nil {
		return 0, err
	}
	return min + x, nil
}

// Int64 returns an unbiased value in [min, max], over the full int64 range.
// Note the (max, min) order.
func Int64(max, min int64) (int64, error) {
	if min > max {
		return 0, ErrBounds
	}
	if min == max {
		return min, nil
	}
	// XOR maps signed ordering to unsigned ordering, including MinInt64.
	const sign uint64 = 1 << 63
	lo, hi := uint64(min)^sign, uint64(max)^sign
	x, err := Uint64(hi, lo)
	if err != nil {
		return 0, err
	}
	return int64(x ^ sign), nil
}

// Int returns an unbiased value in [min, max]. Note the (max, min) order.
func Int(max, min int) (int, error) {
	x, err := Int64(int64(max), int64(min))
	return int(x), err
}

// Float64 scales 53 random bits into a finite [min, max) interval.
// This samples a real interval approximately, not all floats uniformly.
func Float64(max, min float64) (float64, error) {
	if math.IsNaN(min) || math.IsNaN(max) ||
		math.IsInf(min, 0) || math.IsInf(max, 0) || min > max {
		return 0, ErrBounds
	}
	if min == max {
		return min, nil
	}
	x, err := rawUint64()
	if err != nil {
		return 0, err
	}
	u := float64(x>>11) * 0x1p-53
	var value float64
	if span := max - min; math.IsInf(span, 0) {
		// Avoid overflow when bounds have opposite signs and huge magnitudes.
		value = min*(1-u) + max*u
	} else {
		value = min + span*u
	}
	// Floating-point rounding must not include max or fall below min.
	if value >= max {
		value = math.Nextafter(max, min)
	}
	if value < min {
		value = min
	}
	return value, nil
}

// Float32 is Float64 narrowed to float32, still excluding max.
func Float32(max, min float32) (float32, error) {
	x, err := Float64(float64(max), float64(min))
	if err != nil {
		return 0, err
	}
	value := float32(x)
	if min != max && value >= max {
		value = math.Nextafter32(max, min)
	}
	return value, nil
}

// String returns exactly n ASCII characters (also n bytes).
// With 64 symbols, each character carries 6 random bits without bias.
// The alphabet is URL-safe: letters, digits, '-' and '_'.
func String(n int) (string, error) {
	if n < 0 {
		return "", ErrSize
	}
	if n == 0 {
		return "", nil
	}
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	output := make([]byte, n)
	var buffer [256]byte
	for offset := 0; offset < n; {
		count := n - offset
		if count > len(buffer) {
			count = len(buffer)
		}
		if _, err := rand.Read(buffer[:count]); err != nil {
			return "", err
		}
		for i, b := range buffer[:count] {
			output[offset+i] = alphabet[b&63]
		}
		offset += count
	}
	return string(output), nil
}

// ULID returns a 26-character identifier with 80 cryptographically random bits.
// Its timestamp is public. Ordering within the same millisecond is random,
// not monotonic. Do not use ULIDs as authentication secrets.
func ULID() (string, error) {
	id, err := ulid.New(ulid.Timestamp(time.Now()), rand.Reader)
	if err != nil {
		return "", err
	}
	return id.String(), nil
}
