package codec_test

import (
	"strings"
	"testing"

	"github.com/ams-soft/nforge/app"
	"github.com/ams-soft/nforge/codec"
	"github.com/ams-soft/nforge/internal/apptest"
)

func TestModuleDefaultsAndRegistry(t *testing.T) {
	a := apptest.Start(t, "", codec.Module())
	r, ok := app.Value(a, codec.Capability)
	if !ok || r.DefaultFormat() != codec.JSON || len(r.Formats()) != 1 || r.MaxPayloadBytes() != codec.DefaultMaxPayloadBytes || r.StrictUnknownFields() {
		t.Fatalf("registry = %+v", r)
	}
	if codec.Default() == r {
		t.Fatal("without app.WithProcessGlobals the process default must stay untouched")
	}
}

func TestModuleSettings(t *testing.T) {
	a := apptest.Start(t, "codec: {formats: {enabled: [json, xml, cbor]}, default: xml, strict_unknown_fields: true, max_payload_bytes: 1mb}\n", codec.Module())
	r, _ := app.Value(a, codec.Capability)
	if r.DefaultFormat() != codec.XML || len(r.Formats()) != 3 || r.MaxPayloadBytes() != int64(codec.MB) || !r.StrictUnknownFields() {
		t.Fatalf("registry = %+v", r)
	}
}

// Invalid values fail at app.New, before anything starts.
func TestModuleRejectsInvalidSettings(t *testing.T) {
	for yaml, want := range map[string]string{
		"codec: {max_payload_bytes: 300mb}\n":                  "at most",
		"codec: {max_payload_bytes: 2gb}\n":                    "between 0 and",
		"codec: {default: cbor}\n":                             "not enabled",
		"codec: {formats: {enabled: [yaml]}}\n":                "no Codec registered",
		"codec: {strict_unknown: true}\n":                      "not found",
		"codec: {formats: {enabled: [json]}, xml: {dtd: 1}}\n": "not found",
	} {
		_, err := apptest.New(t, yaml, codec.Module())
		if err == nil || (!strings.Contains(err.Error(), want) && !strings.Contains(err.Error(), "between 0 and")) {
			t.Errorf("%s: err = %v", yaml, err)
		}
	}
}

func TestModuleEnvFile(t *testing.T) {
	d := apptest.Load(t, "", "CODEC_MAX_PAYLOAD_BYTES=2mb\nCODEC_FORMATS_ENABLED=json,cbor\n")
	a, err := app.New(d, app.Modules(codec.Module()), apptest.Quiet(), app.WithProcessGlobals())
	if err != nil {
		t.Fatal(err)
	}
	previous := codec.Default()
	t.Cleanup(func() { codec.SetDefault(previous) })
	if err := a.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Shutdown(t.Context()) }()
	r, _ := app.Value(a, codec.Capability)
	if r.MaxPayloadBytes() != int64(2*codec.MB) || len(r.Formats()) != 2 {
		t.Fatalf("registry = %+v", r)
	}
	if codec.Default() != r {
		t.Fatal("app.WithProcessGlobals installs the registry as the process default")
	}
}
