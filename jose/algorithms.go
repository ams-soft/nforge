package jose

// Supported algorithm identifiers. The suite is deliberately restricted; see
// the package documentation.
const (
	// EdDSA is Ed25519 signing (RFC 8037). Ed448 is not supported.
	EdDSA = "EdDSA"
	// ES256 is ECDSA P-256 with SHA-256.
	ES256 = "ES256"
	// PS256 is RSASSA-PSS with SHA-256.
	PS256 = "PS256"
	// RS256 is RSASSA-PKCS1-v1_5 with SHA-256, kept for compatibility.
	RS256 = "RS256"
	// HS256 is HMAC SHA-256 with a secret of at least 32 random bytes.
	HS256 = "HS256"
	// RSAOAEP256 is RSA-OAEP with SHA-256 key management.
	RSAOAEP256 = "RSA-OAEP-256"
	// Dir is direct encryption with a shared 32-byte key.
	Dir = "dir"
	// A256GCM is AES-256-GCM content encryption.
	A256GCM = "A256GCM"
)

// Profile kinds.
const (
	KindJWS       = "jws"
	KindJWE       = "jwe"
	KindJWTSigned = "jwt_signed"
	KindJWTNested = "jwt_nested"
)

// Key operations.
const (
	OpSign    = "sign"
	OpVerify  = "verify"
	OpEncrypt = "encrypt"
	OpDecrypt = "decrypt"
)

const (
	rsaMinBits = 2048
	rsaMaxBits = 8192
	hmacMinLen = 32
	dirKeyLen  = 32
	gcmIVLen   = 12
	gcmTagLen  = 16
)

func isSigningAlg(alg string) bool {
	switch alg {
	case EdDSA, ES256, PS256, RS256, HS256:
		return true
	}
	return false
}

func isKeyMgmtAlg(alg string) bool { return alg == RSAOAEP256 || alg == Dir }

func isSymmetricAlg(alg string) bool { return alg == HS256 || alg == Dir }

// opsFor lists the operations a key bound to alg may declare.
func opsFor(alg string) []string {
	if isSigningAlg(alg) {
		return []string{OpSign, OpVerify}
	}
	if isKeyMgmtAlg(alg) {
		return []string{OpEncrypt, OpDecrypt}
	}
	return nil
}

func isJWTKind(kind string) bool { return kind == KindJWTSigned || kind == KindJWTNested }

func signs(kind string) bool {
	return kind == KindJWS || kind == KindJWTSigned || kind == KindJWTNested
}

func encrypts(kind string) bool { return kind == KindJWE || kind == KindJWTNested }
