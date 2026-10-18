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
