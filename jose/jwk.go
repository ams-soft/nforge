package jose

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"slices"

	"github.com/ams-soft/nforge/jose/internal/strictjson"
)

// jwkMembers lists every accepted JWK member; anything else (x5c, jku, oth,
// a "keys" set...) is rejected.
var jwkMembers = map[string]bool{
	"kty": true, "kid": true, "alg": true, "use": true, "key_ops": true,
	"crv": true, "x": true, "y": true, "d": true,
	"n": true, "e": true, "p": true, "q": true, "dp": true, "dq": true, "qi": true,
	"k": true,
}

// jwkOps maps a configured operation to the key_ops values that grant it.
var jwkOps = map[string][]string{
	OpSign:    {"sign"},
	OpVerify:  {"verify"},
	OpEncrypt: {"encrypt", "wrapKey"},
	OpDecrypt: {"decrypt", "unwrapKey"},
}

// parseJWK imports exactly one JWK object. Metadata present in the JWK
// must agree with the trusted configuration; missing metadata comes from it.
func (k *key) parseJWK(data []byte, kc KeyConfig, maxDepth int) error {
	members, err := strictjson.Object(data, maxDepth)
	if err != nil {
		return errReason("jwk source must be one strict json object")
	}
	if _, ok := members["keys"]; ok {
		return errReason("jwk sets are not accepted as key sources")
	}
	str := map[string]string{}
	var keyOps []string
	for name, raw := range members {
		if !jwkMembers[name] {
			return errReason("unsupported jwk member " + name)
		}
		if name == "key_ops" {
			if err := json.Unmarshal(raw, &keyOps); err != nil || keyOps == nil {
				return errReason("jwk key_ops must be an array of strings")
			}
			continue
		}
		var v string
		if err := json.Unmarshal(raw, &v); err != nil || strictjson.IsNull(raw) {
			return errReason("jwk member " + name + " must be a string")
		}
		str[name] = v
	}

	if kid, ok := str["kid"]; ok && kid != kc.KID {
		return errReason("jwk kid does not match the configuration")
	}
	if alg, ok := str["alg"]; ok && alg != kc.Algorithm {
		return errReason("jwk alg does not match the configuration")
	}
	if use, ok := str["use"]; ok {
		want := "sig"
		if isKeyMgmtAlg(kc.Algorithm) {
			want = "enc"
		}
		if use != want {
			return errReason("jwk use does not match the algorithm")
		}
	}
	if keyOps != nil {
		if hasDuplicates(keyOps) {
			return errReason("jwk key_ops has duplicates")
		}
		for _, op := range kc.Operations {
			if !slices.ContainsFunc(jwkOps[op], func(v string) bool { return slices.Contains(keyOps, v) }) {
				return errReason("jwk key_ops does not grant the configured " + op + " operation")
			}
		}
	}

	field := func(name string, size int) ([]byte, error) {
		v, ok := str[name]
		if !ok {
			return nil, nil
		}
		b, err := decodeB64([]byte(v))
		if err != nil || len(b) == 0 || (size > 0 && len(b) != size) {
			return nil, errReason("invalid jwk member " + name)
		}
		return b, nil
	}
	allowed := func(names ...string) error {
		for name := range str {
			if !slices.Contains(append(names, "kty", "kid", "alg", "use"), name) {
				return errReason("jwk member " + name + " does not belong to this key type")
			}
		}
		return nil
	}

	switch str["kty"] {
	case "OKP":
		if err := allowed("crv", "x", "d"); err != nil {
			return err
		}
		if str["crv"] != "Ed25519" {
			return errReason("unsupported OKP curve")
		}
		x, err := field("x", ed25519.PublicKeySize)
		if err != nil || x == nil {
			return errReason("invalid Ed25519 public key")
		}
		pub := ed25519.PublicKey(x)
		d, err := field("d", ed25519.SeedSize)
		if err != nil {
			return err
		}
		if d == nil {
			return k.setAsymmetric(pub)
		}
		defer clear(d)
		priv := ed25519.NewKeyFromSeed(d)
		if !pub.Equal(priv.Public()) {
			return errReason("inconsistent Ed25519 private and public components")
		}
		return k.setAsymmetric(priv)

	case "EC":
		if err := allowed("crv", "x", "y", "d"); err != nil {
			return err
		}
		if str["crv"] != "P-256" {
			return errReason("unsupported EC curve")
		}
		x, errX := field("x", 32)
		y, errY := field("y", 32)
		if errX != nil || errY != nil || x == nil || y == nil {
			return errReason("invalid P-256 public key")
		}
		pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), append(append([]byte{4}, x...), y...))
		if err != nil {
			return errReason("invalid P-256 point")
		}
		d, err := field("d", 32)
		if err != nil {
			return err
		}
		if d == nil {
			return k.setAsymmetric(pub)
		}
		defer clear(d)
		priv, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), d)
		if err != nil {
			return errReason("invalid P-256 private key")
		}
		if !priv.PublicKey.Equal(pub) {
			return errReason("inconsistent P-256 private and public components")
		}
		return k.setAsymmetric(priv)

	case "RSA":
		if err := allowed("n", "e", "d", "p", "q", "dp", "dq", "qi"); err != nil {
			return err
		}
		n, errN := field("n", 0)
		e, errE := field("e", 0)
		if errN != nil || errE != nil || n == nil || e == nil || len(e) > 4 {
			return errReason("invalid RSA public key")
		}
		exp := new(big.Int).SetBytes(e)
		if !exp.IsInt64() || exp.Int64() < 3 || exp.Int64() > 1<<31-1 || exp.Bit(0) == 0 {
			return errReason("invalid RSA exponent")
		}
		pub := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(exp.Int64())}
		_, hasD := str["d"]
		_, hasP := str["p"]
		_, hasQ := str["q"]
		_, hasDP := str["dp"]
		_, hasDQ := str["dq"]
		_, hasQI := str["qi"]
		if !hasD {
			if hasP || hasQ || hasDP || hasDQ || hasQI {
				return errReason("RSA private components without d")
			}
			return k.setAsymmetric(pub)
		}
		if !hasP || !hasQ {
			return errReason("RSA private keys must include p and q")
		}
		crt := hasDP || hasDQ || hasQI
		if crt && (!hasDP || !hasDQ || !hasQI) {
			return errReason("RSA CRT components must be complete")
		}
		var parts [6][]byte
		for i, name := range []string{"d", "p", "q", "dp", "dq", "qi"} {
			b, err := field(name, 0)
			if err != nil {
				return err
			}
			parts[i] = b
		}
		defer func() {
			for _, b := range parts {
				clear(b)
			}
		}()
		priv := &rsa.PrivateKey{
			PublicKey: *pub,
			D:         new(big.Int).SetBytes(parts[0]),
			Primes:    []*big.Int{new(big.Int).SetBytes(parts[1]), new(big.Int).SetBytes(parts[2])},
		}
		if err := priv.Validate(); err != nil {
			return errReason("invalid RSA private key")
		}
		priv.Precompute()
		if crt {
			c := priv.Precomputed
			if c.Dp.Cmp(new(big.Int).SetBytes(parts[3])) != 0 ||
				c.Dq.Cmp(new(big.Int).SetBytes(parts[4])) != 0 ||
				c.Qinv.Cmp(new(big.Int).SetBytes(parts[5])) != 0 {
				return errReason("inconsistent RSA CRT components")
			}
		}
		return k.setAsymmetric(priv)

	case "oct":
		if err := allowed("k"); err != nil {
			return err
		}
		secret, err := field("k", 0)
		if err != nil || secret == nil {
			return errReason("invalid oct key")
		}
		k.secret = secret
		return nil
	}
	return errReason("unsupported jwk kty")
}

// publicJWK is the exported representation: public members only, in a
// fixed order so the JWKS bytes are stable.
type publicJWK struct {
	KID    string   `json:"kid"`
	KTY    string   `json:"kty"`
	Alg    string   `json:"alg"`
	Use    string   `json:"use"`
	KeyOps []string `json:"key_ops"`
	Crv    string   `json:"crv,omitempty"`
	X      string   `json:"x,omitempty"`
	Y      string   `json:"y,omitempty"`
	N      string   `json:"n,omitempty"`
	E      string   `json:"e,omitempty"`
}

// buildJWKS exports the publish=true asymmetric keys sorted by kid, in a
// stable byte representation.
func buildJWKS(keys map[string]*key) ([]byte, error) {
	b64 := base64.RawURLEncoding.EncodeToString
	out := struct {
		Keys []publicJWK `json:"keys"`
	}{Keys: []publicJWK{}}

	kids := make([]string, 0, len(keys))
	for kid, k := range keys {
		if k.publish {
			kids = append(kids, kid)
		}
	}
	slices.Sort(kids)
	for _, kid := range kids {
		k := keys[kid]
		if k.secret != nil {
			return nil, keyErr(kid, "symmetric keys are never published")
		}
		j := publicJWK{KID: kid, Alg: k.alg, Use: "sig"}
		if isKeyMgmtAlg(k.alg) {
			j.Use = "enc"
		}
		for _, op := range k.ops {
			switch op {
			case OpVerify, OpSign:
				if !slices.Contains(j.KeyOps, "verify") {
					j.KeyOps = append(j.KeyOps, "verify")
				}
			case OpEncrypt, OpDecrypt:
				if !slices.Contains(j.KeyOps, "wrapKey") {
					j.KeyOps = append(j.KeyOps, "wrapKey")
				}
			}
		}
		switch pub := k.public.(type) {
		case ed25519.PublicKey:
			j.KTY, j.Crv, j.X = "OKP", "Ed25519", b64(pub)
		case *ecdsa.PublicKey:
			raw, err := pub.Bytes()
			if err != nil {
				return nil, keyErr(kid, "cannot export P-256 point")
			}
			j.KTY, j.Crv, j.X, j.Y = "EC", "P-256", b64(raw[1:33]), b64(raw[33:])
		case *rsa.PublicKey:
			j.KTY, j.N, j.E = "RSA", b64(pub.N.Bytes()), b64(big.NewInt(int64(pub.E)).Bytes())
		default:
			return nil, keyErr(kid, "cannot export key type")
		}
		out.Keys = append(out.Keys, j)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(out); err != nil {
		return nil, newError(ErrInternal, "jwks", "", "", "cannot encode jwks")
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}
