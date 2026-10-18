// Package ncrypto provides standalone cryptographic primitives: authenticated
// encryption (XChaCha20-Poly1305, AES-256-GCM), HMAC-SHA256, password hashing
// (bcrypt, Argon2id), unkeyed data hashing (BLAKE3, SHA-256) and
// cryptographically secure random values.
//
// It has no configuration and is not part of deps.Deps: callers build each
// primitive from their own keys. The directory is crypto/, but the package is
// named ncrypto to avoid clashing with the standard library.
//
// Random ranges take their bounds as (max, min): integer ranges are inclusive
// [min, max] and float ranges are [min, max); equal bounds return that bound.
package ncrypto
