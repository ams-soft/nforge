package main

import (
	"testing"

	"github.com/ams-soft/nforge/logs"
)

// TestRunCrypto keeps the crypto demo compiling and working while main.go
// leaves it disabled; any failure inside it exits the test binary.
func TestRunCrypto(t *testing.T) {
	runCrypto(logs.NewNoop())
}
