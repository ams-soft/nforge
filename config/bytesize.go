package config

import (
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// ByteSize is a size in bytes that accepts units in the config: "5mb",
// "512kb", "1.5gb", "100b" or just the number of bytes. Units are base 1024
// and case-insensitive ("5MB", "5 MiB" also work).
type ByteSize int64

const (
	B  ByteSize = 1
	KB          = 1024 * B
	MB          = 1024 * KB
	GB          = 1024 * MB
)

var byteUnits = map[string]ByteSize{
	"": B, "b": B,
	"k": KB, "kb": KB, "kib": KB,
	"m": MB, "mb": MB, "mib": MB,
	"g": GB, "gb": GB, "gib": GB,
}

func ParseByteSize(s string) (ByteSize, error) {
	str := strings.ToLower(strings.TrimSpace(s))
	i := strings.IndexFunc(str, func(r rune) bool { return (r < '0' || r > '9') && r != '.' })
	if i < 0 {
		i = len(str)
	}
	num, unit := str[:i], strings.TrimSpace(str[i:])
	mult, ok := byteUnits[unit]
	if num == "" || !ok {
		return 0, fmt.Errorf("invalid size %q (use e.g. 5mb, 512kb, 1048576)", s)
	}
	n, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q: %w", s, err)
	}
	return ByteSize(n * float64(mult)), nil
}

func (b *ByteSize) UnmarshalText(text []byte) error {
	v, err := ParseByteSize(string(text))
	if err != nil {
		return err
	}
	*b = v
	return nil
}

func (b *ByteSize) UnmarshalYAML(node *yaml.Node) error {
	return b.UnmarshalText([]byte(node.Value))
}
