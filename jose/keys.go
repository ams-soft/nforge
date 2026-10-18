package jose

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"io"
	"os"
	"runtime"
	"slices"
)

// key is one loaded key bound to its configured algorithm and operations.
// Exactly one of the asymmetric pair (public, private) or secret is set.
type key struct {
	kid     string
	alg     string
	ops     []string
	publish bool

	public  crypto.PublicKey // ed25519.PublicKey, *ecdsa.PublicKey or *rsa.PublicKey
	private crypto.Signer    // nil for public-only keys
	secret  []byte           // HS256 / dir

	// material identifies the key for duplicate detection (hash of the
	// canonical public key or of the secret).
	material [32]byte
}

func (k *key) has(op string) bool { return slices.Contains(k.ops, op) }

// rsaSize is the modulus size in bytes, used to precheck signature and
// encrypted-key lengths.
func (k *key) rsaSize() int {
	if pub, ok := k.public.(*rsa.PublicKey); ok {
		return pub.Size()
	}
	return 0
}

// LogValue never exposes key material.
func (k *key) LogValue() string { return "jose.key{kid: " + k.kid + ", alg: " + k.alg + "}" }

func (k *key) String() string { return k.LogValue() }

// wipe clears the owned secret bytes on a best-effort basis.
func (k *key) wipe() { clear(k.secret) }

// loadKey reads, parses and checks one configured key.
func loadKey(kc KeyConfig, limits LimitsConfig) (*key, error) {
	if kc.Secret != "" {
		k := &key{kid: kc.KID, alg: kc.Algorithm, ops: slices.Clone(kc.Operations), secret: []byte(kc.Secret)}
		if err := k.check(); err != nil {
			k.wipe()
			return nil, keyErr(kc.KID, err.Error())
		}
		k.material = k.fingerprint()
		return k, nil
	}
	data, mode, err := readKeyFile(kc.Source.File, limits.MaxKeyFileBytes)
	if err != nil {
		return nil, keyErr(kc.KID, err.Error())
	}
	defer clear(data)

	k := &key{kid: kc.KID, alg: kc.Algorithm, ops: slices.Clone(kc.Operations), publish: kc.Publish}
	switch kc.Source.Format {
	case "pem":
		err = k.parsePEM(data)
	case "jwk":
		err = k.parseJWK(data, kc, limits.MaxJSONDepth)
	case "oct":
		err = k.parseOct(data)
	default:
		err = errReason("unsupported source format")
	}
	if err != nil {
		return nil, keyErr(kc.KID, err.Error())
	}
	if err := k.check(); err != nil {
		return nil, keyErr(kc.KID, err.Error())
	}
	if (k.private != nil || k.secret != nil) && !ownerOnly(mode) {
		return nil, keyErr(kc.KID, "private or secret key file must not be accessible by group or others")
	}
	k.material = k.fingerprint()
	return k, nil
}

type errReason string

func (e errReason) Error() string { return string(e) }

func keyErr(kid, reason string) error {
	return newError(ErrInvalidKey, "load key", "", kid, reason)
}

// readKeyFile opens the path (following symlinks, as projected secrets
// use them), requires a regular file and bounds the read.
func readKeyFile(path string, maxBytes int64) ([]byte, os.FileMode, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, errReason("cannot open key source")
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, 0, errReason("cannot stat key source")
	}
	if !info.Mode().IsRegular() {
		return nil, 0, errReason("key source must be a regular file")
	}
	if info.Size() > maxBytes {
		return nil, 0, errReason("key source exceeds limits.max_key_file_bytes")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, 0, errReason("cannot read key source")
	}
	if int64(len(data)) > maxBytes {
		clear(data)
		return nil, 0, errReason("key source exceeds limits.max_key_file_bytes")
	}
	return data, info.Mode(), nil
}

func ownerOnly(mode os.FileMode) bool {
	if runtime.GOOS == "windows" {
		return true // POSIX permission bits are not meaningful there
	}
	return mode.Perm()&0o077 == 0
}

func (k *key) parsePEM(data []byte) error {
	block, rest := pem.Decode(data)
	if block == nil {
		return errReason("no pem block")
	}
	if len(bytes.TrimSpace(rest)) != 0 {
		return errReason("pem source must contain exactly one block and nothing else")
	}
	if len(block.Headers) != 0 {
		return errReason("encrypted or annotated pem blocks are not supported")
	}
	defer clear(block.Bytes)
	var parsed any
	var err error
	switch block.Type {
	case "PRIVATE KEY":
		parsed, err = x509.ParsePKCS8PrivateKey(block.Bytes)
	case "RSA PRIVATE KEY":
		parsed, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	case "EC PRIVATE KEY":
		parsed, err = x509.ParseECPrivateKey(block.Bytes)
	case "PUBLIC KEY":
		parsed, err = x509.ParsePKIXPublicKey(block.Bytes)
	case "RSA PUBLIC KEY":
		parsed, err = x509.ParsePKCS1PublicKey(block.Bytes)
	default:
		return errReason("unsupported pem block type")
	}
	if err != nil {
		return errReason("invalid key encoding")
	}
	return k.setAsymmetric(parsed)
}

func (k *key) setAsymmetric(parsed any) error {
	// Ed25519 keys are byte slices that may alias the parsed buffer, which is
	// cleared after loading: always keep an owned copy.
	switch v := parsed.(type) {
	case ed25519.PrivateKey:
		priv := slices.Clone(v)
		k.private, k.public = priv, priv.Public()
	case ed25519.PublicKey:
		k.public = slices.Clone(v)
	case *ecdsa.PrivateKey:
		k.private, k.public = v, &v.PublicKey
	case *ecdsa.PublicKey:
		k.public = v
	case *rsa.PrivateKey:
		k.private, k.public = v, &v.PublicKey
	case *rsa.PublicKey:
		k.public = v
	default:
		return errReason("unsupported key type")
	}
	return nil
}

// parseOct reads one canonical unpadded base64url secret, with at most one
// trailing LF or CRLF.
func (k *key) parseOct(data []byte) error {
	s := data
	if bytes.HasSuffix(s, []byte("\r\n")) {
		s = s[:len(s)-2]
	} else if bytes.HasSuffix(s, []byte("\n")) {
		s = s[:len(s)-1]
	}
	secret, err := decodeB64(s)
	if err != nil {
		return errReason("oct source must be one canonical unpadded base64url value")
	}
	k.secret = secret
	return nil
}

// check verifies that the key type fits the bound algorithm and operations.
func (k *key) check() error {
	if isSymmetricAlg(k.alg) {
		if k.secret == nil {
			return errReason("algorithm requires a symmetric key")
		}
		if k.alg == HS256 && len(k.secret) < hmacMinLen {
			return errReason("HS256 secrets must have at least 32 bytes")
		}
		if k.alg == Dir && len(k.secret) != dirKeyLen {
			return errReason("dir keys must have exactly 32 bytes")
		}
		return nil
	}
	if k.public == nil {
		return errReason("algorithm requires an asymmetric key")
	}
	switch k.alg {
	case EdDSA:
		if _, ok := k.public.(ed25519.PublicKey); !ok {
			return errReason("EdDSA requires an Ed25519 key")
		}
	case ES256:
		pub, ok := k.public.(*ecdsa.PublicKey)
		if !ok || pub.Curve != elliptic.P256() {
			return errReason("ES256 requires a P-256 key")
		}
		if _, err := pub.ECDH(); err != nil {
			return errReason("invalid P-256 point")
		}
	case PS256, RS256, RSAOAEP256:
		pub, ok := k.public.(*rsa.PublicKey)
		if !ok {
			return errReason("algorithm requires an RSA key")
		}
		if bits := pub.N.BitLen(); bits < rsaMinBits || bits > rsaMaxBits {
			return errReason("RSA keys must have 2048 to 8192 bits")
		}
		if priv, ok := k.private.(*rsa.PrivateKey); ok {
			if err := priv.Validate(); err != nil {
				return errReason("invalid RSA private key")
			}
		}
	default:
		return errReason("unsupported algorithm")
	}
	if k.private == nil && (k.has(OpSign) || k.has(OpDecrypt)) {
		return errReason("sign and decrypt operations require a private key")
	}
	return nil
}

func (k *key) fingerprint() [32]byte {
	h := sha256.New()
	switch {
	case k.secret != nil:
		h.Write([]byte("oct:"))
		h.Write(k.secret)
	default:
		der, err := x509.MarshalPKIXPublicKey(k.public)
		if err != nil {
			h.Write([]byte(k.kid)) // unreachable for checked keys
		}
		h.Write(der)
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// verificationKey is the key handed to the JOSE library to verify or encrypt.
func (k *key) verificationKey() any {
	if k.secret != nil {
		return k.secret
	}
	return k.public
}

// signingKey is the key handed to the JOSE library to sign or decrypt.
func (k *key) signingKey() any {
	if k.secret != nil {
		return k.secret
	}
	return k.private
}

// decodeB64 decodes canonical unpadded base64url (rejects padding,
// whitespace and non-zero trailing bits). The alphabet is checked explicitly
// because the stdlib decoder silently skips CR and LF.
func decodeB64(s []byte) ([]byte, error) {
	for _, c := range s {
		if !isB64URL(c) {
			return nil, errReason("invalid base64url")
		}
	}
	out := make([]byte, base64.RawURLEncoding.DecodedLen(len(s)))
	n, err := base64.RawURLEncoding.Strict().Decode(out, s)
	if err != nil {
		return nil, err
	}
	return out[:n], nil
}

func isB64URL(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_'
}
