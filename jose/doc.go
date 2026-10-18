// Package jose signs, verifies, encrypts and decrypts JOSE objects (compact
// JWS and JWE) and issues and validates JWTs, through explicitly configured
// profiles.
//
// A profile has one kind, chosen by configuration and never by the token:
//
//   - jws: arbitrary bytes in a compact JWS (integrity, readable payload);
//   - jwe: arbitrary bytes in a compact JWE (confidentiality);
//   - jwt_signed: JSON claims in a compact JWS (API access tokens);
//   - jwt_nested: JSON claims in a JWS inside a JWE (confidential tokens).
//
// JWS and JWE profiles are optional: an application that only needs access
// tokens configures a single jwt_signed profile.
//
// The algorithm suite is restricted on purpose (EdDSA/Ed25519, ES256, PS256,
// RS256, HS256 for signatures; RSA-OAEP-256 and dir with A256GCM for
// encryption). Keys default to HS256 and may be given as an inline string
// secret (usually ${JWT_SECRET}) for the simplest setup; profiles inherit
// their algorithms from their keys. Every key is bound to one algorithm;
// incoming tokens must carry a protected kid and the profile's typ, and are
// parsed defensively (bounded sizes, canonical base64url, strict JSON with no
// duplicate members) before any cryptography runs.
//
// The package has no dependency on the rest of nForge, installs no global
// state and can run several independent services in one process. The
// cryptography itself is delegated to github.com/go-jose/go-jose/v4, whose
// types never appear in this API.
package jose
