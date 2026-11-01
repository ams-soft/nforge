package config

import (
	"github.com/ams-soft/nforge/cache"
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

// ParseByteSize parses a size with the syntax of cache.ParseByteSize (the one
// implementation of it, so the two packages cannot drift apart): a number with
// an optional unit, exact for whole numbers, an error on overflow.
func ParseByteSize(s string) (ByteSize, error) {
	v, err := cache.ParseByteSize(s)
	if err != nil {
		return 0, err
	}
	return ByteSize(v), nil
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
