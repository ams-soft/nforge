package codec

import (
	"errors"
	"fmt"
	"math"
	"math/bits"
	"strconv"
	"strings"
)

// ByteSize is a size in bytes that accepts units in YAML and env files:
// "1mb", "512kb", "1.5gb", "100b" or a plain number of bytes. Units are
// base 1024 and case-insensitive ("5MB", "5 MiB"). It is the one parser of
// sizes in nForge: the cache and HTTP limits use it too.
type ByteSize int64

// Size units.
const (
	B  ByteSize = 1
	KB          = 1024 * B
	MB          = 1024 * KB
	GB          = 1024 * MB
)

var byteUnits = map[string]uint64{
	"": 1, "b": 1,
	"k": 1 << 10, "kb": 1 << 10, "kib": 1 << 10,
	"m": 1 << 20, "mb": 1 << 20, "mib": 1 << 20,
	"g": 1 << 30, "gb": 1 << 30, "gib": 1 << 30,
}

// ParseByteSize parses a size such as "1mb" or "1048576".
func ParseByteSize(s string) (ByteSize, error) {
	str := strings.ToLower(strings.TrimSpace(s))
	i := strings.IndexFunc(str, func(r rune) bool { return (r < '0' || r > '9') && r != '.' })
	if i < 0 {
		i = len(str)
	}
	num, unit := str[:i], strings.TrimSpace(str[i:])
	mult, ok := byteUnits[unit]
	if num == "" || !ok {
		return 0, fmt.Errorf("invalid size %q (use e.g. 1mb, 512kb, 1048576)", s)
	}
	if !strings.Contains(num, ".") {
		// Whole numbers stay exact (a float64 loses the last bytes above 2^53)
		// and overflow is an error.
		n, err := strconv.ParseUint(num, 10, 63)
		if err != nil {
			return 0, fmt.Errorf("invalid size %q: too large", s)
		}
		hi, lo := bits.Mul64(n, mult)
		if hi != 0 || lo > math.MaxInt64 {
			return 0, fmt.Errorf("invalid size %q: too large", s)
		}
		return ByteSize(lo), nil
	}
	n, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	v := n * float64(mult)
	if v >= math.MaxInt64 {
		return 0, fmt.Errorf("invalid size %q: too large", s)
	}
	return ByteSize(v), nil
}

// UnmarshalText implements encoding.TextUnmarshaler (env files).
func (b *ByteSize) UnmarshalText(text []byte) error {
	v, err := ParseByteSize(string(text))
	if err != nil {
		return err
	}
	*b = v
	return nil
}

// UnmarshalYAML accepts both "1mb" and 1048576 in YAML. It uses the
// unmarshal-function form so the package needs no YAML dependency.
func (b *ByteSize) UnmarshalYAML(unmarshal func(any) error) error {
	var s string
	if err := unmarshal(&s); err != nil {
		return errors.New("invalid size: expected a string or a number")
	}
	return b.UnmarshalText([]byte(s))
}
