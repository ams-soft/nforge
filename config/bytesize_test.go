package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestParseByteSize(t *testing.T) {
	cases := map[string]ByteSize{
		"5mb":     5 * MB,
		"5MB":     5 * MB,
		"5 MiB":   5 * MB,
		"512kb":   512 * KB,
		"1.5gb":   GB + 512*MB,
		"100b":    100,
		"1048576": MB,
	}
	for in, want := range cases {
		got, err := ParseByteSize(in)
		if err != nil || got != want {
			t.Errorf("ParseByteSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "mb", "5xb", "-1mb", "5.5.5mb"} {
		if _, err := ParseByteSize(bad); err == nil {
			t.Errorf("ParseByteSize(%q) should fail", bad)
		}
	}
}

func TestByteSizeYAML(t *testing.T) {
	var c CodecConfig
	if err := yaml.Unmarshal([]byte("max_payload_bytes: 5mb"), &c); err != nil || c.MaxPayloadBytes != 5*MB {
		t.Fatalf("yaml: %d %v", c.MaxPayloadBytes, err)
	}
	if err := yaml.Unmarshal([]byte("max_payload_bytes: 2048"), &c); err != nil || c.MaxPayloadBytes != 2*KB {
		t.Fatalf("yaml number: %d %v", c.MaxPayloadBytes, err)
	}
}

// Out-of-range sizes used to convert a float64 to int64 and silently become
// math.MinInt64; whole numbers lost bytes above 2^53.
func TestParseByteSizeOverflowAndPrecision(t *testing.T) {
	for _, in := range []string{
		"9999999999gb", "8589934592gb", "9223372036854775808", "9223372036854775807kb",
		"18446744073709551616", "99999999999999999999b", "9e18gb", "1e3mb",
	} {
		if got, err := ParseByteSize(in); err == nil {
			t.Errorf("ParseByteSize(%q) = %d, want an error", in, got)
		}
	}
	exact := map[string]ByteSize{
		"9007199254740993":    9007199254740993, // 2^53+1: a float64 would round it
		"9223372036854775807": 1<<63 - 1,
		"8191gb":              8191 * GB,
		"0":                   0,
		" 5 mb ":              5 * MB,
	}
	for in, want := range exact {
		if got, err := ParseByteSize(in); err != nil || got != want {
			t.Errorf("ParseByteSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
}
