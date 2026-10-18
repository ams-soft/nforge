// Command keygen creates the key files of the jose example's asymmetric
// alternative (the commented block in config.yaml): Ed25519 signing keys, an
// RSA-OAEP encryption key and a 32-byte secret for a dir JWE profile. The
// default setup uses string secrets from the environment instead. The
// framework never generates keys by itself; in production, provision them
// through your secret manager.
//
//	go run ./_examples/jose/cmd/keygen [-out _examples/jose/keys] [-force]
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

func main() {
	out := flag.String("out", filepath.Join("_examples", "jose", "keys"), "directory for the generated key files")
	force := flag.Bool("force", false, "overwrite existing key files")
	flag.Parse()

	if err := generate(*out, *force); err != nil {
		fmt.Fprintln(os.Stderr, "keygen:", err)
		os.Exit(1)
	}
	abs, err := filepath.Abs(*out)
	if err != nil {
		abs = *out
	}
	fmt.Printf("keys written to %s\n\nexport NFORGE_KEYS_DIR=%s\n", abs, abs)
}

// generate writes every key file with owner-only permissions.
func generate(dir string, force bool) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	files := map[string]func() ([]byte, error){
		"access-sign.pem":      ed25519PEM,
		"message-sign.pem":     ed25519PEM,
		"confidential-enc.pem": func() ([]byte, error) { return rsaPEM(3072) },
		"message-enc.key":      func() ([]byte, error) { return octSecret(32) },
	}
	for name, gen := range files {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err == nil && !force {
			return fmt.Errorf("%s already exists (use -force to replace it)", path)
		} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		data, err := gen()
		if err != nil {
			return fmt.Errorf("generate %s: %w", name, err)
		}
		if err := writeOwnerOnly(path, data); err != nil {
			return err
		}
	}
	return nil
}

func writeOwnerOnly(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil { // WriteFile keeps the mode of an existing file
		return err
	}
	return os.Rename(tmp, path)
}

func ed25519PEM() ([]byte, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return pkcs8(priv)
}

func rsaPEM(bits int) ([]byte, error) {
	priv, err := rsa.GenerateKey(rand.Reader, bits)
	if err != nil {
		return nil, err
	}
	return pkcs8(priv)
}

func pkcs8(priv any) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

func octSecret(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return []byte(base64.RawURLEncoding.EncodeToString(b) + "\n"), nil
}
