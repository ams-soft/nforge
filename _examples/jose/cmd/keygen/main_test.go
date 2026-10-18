package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGenerateOwnerOnlyAndNoOverwrite(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keys")
	if err := generate(dir, false); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"access-sign.pem", "message-sign.pem", "confidential-enc.pem", "message-enc.key"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s: mode %v, want 0600", name, info.Mode().Perm())
		}
	}
	if info, _ := os.Stat(dir); info.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %v, want 0700", info.Mode().Perm())
	}
	if err := generate(dir, false); err == nil {
		t.Fatal("existing keys must not be overwritten without -force")
	}
	if err := generate(dir, true); err != nil {
		t.Fatal(err)
	}
}
