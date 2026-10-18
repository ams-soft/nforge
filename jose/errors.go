package jose

import (
	"context"
	"errors"
	"strings"
)

// Sentinel errors. Every error returned by the package matches exactly one of
// them through errors.Is (context cancellation is preserved in addition), and
// carries sanitized details through errors.As(*Error).
var (
	ErrDisabled              = errors.New("jose: disabled")
	ErrClosed                = errors.New("jose: service closed")
	ErrInvalidConfig         = errors.New("jose: invalid config")
	ErrUnknownProfile        = errors.New("jose: unknown profile")
	ErrProfileKind           = errors.New("jose: wrong profile kind")
	ErrOperationUnavailable  = errors.New("jose: operation unavailable")
	ErrInvalidKey            = errors.New("jose: invalid key")
	ErrUnknownKey            = errors.New("jose: unknown key")
	ErrUnsupportedAlgorithm  = errors.New("jose: unsupported algorithm")
	ErrMalformedToken        = errors.New("jose: malformed token")
	ErrPayloadTooLarge       = errors.New("jose: payload too large")
	ErrInvalidSignature      = errors.New("jose: invalid signature")
	ErrDecryption            = errors.New("jose: decryption failed")
	ErrInvalidClaims         = errors.New("jose: invalid claims")
	ErrExpired               = errors.New("jose: token expired")
	ErrNotYetValid           = errors.New("jose: token not yet valid")
	ErrInvalidIssuer         = errors.New("jose: invalid issuer")
	ErrInvalidAudience       = errors.New("jose: invalid audience")
	ErrInvalidType           = errors.New("jose: invalid type")
	ErrInvalidTarget         = errors.New("jose: invalid target")
	ErrClaimsRejected        = errors.New("jose: claims rejected")
	ErrValidationUnavailable = errors.New("jose: validation unavailable")
	ErrInternal              = errors.New("jose: internal error")
)

// codes are the stable, machine-readable identifiers of each sentinel.
var codes = map[error]string{
	ErrDisabled:              "disabled",
	ErrClosed:                "closed",
	ErrInvalidConfig:         "invalid_config",
	ErrUnknownProfile:        "unknown_profile",
	ErrProfileKind:           "profile_kind",
	ErrOperationUnavailable:  "operation_unavailable",
	ErrInvalidKey:            "invalid_key",
	ErrUnknownKey:            "unknown_key",
	ErrUnsupportedAlgorithm:  "unsupported_algorithm",
	ErrMalformedToken:        "malformed_token",
	ErrPayloadTooLarge:       "payload_too_large",
	ErrInvalidSignature:      "invalid_signature",
	ErrDecryption:            "decryption_failed",
	ErrInvalidClaims:         "invalid_claims",
	ErrExpired:               "expired",
	ErrNotYetValid:           "not_yet_valid",
	ErrInvalidIssuer:         "invalid_issuer",
	ErrInvalidAudience:       "invalid_audience",
	ErrInvalidType:           "invalid_type",
	ErrInvalidTarget:         "invalid_target",
	ErrClaimsRejected:        "claims_rejected",
	ErrValidationUnavailable: "validation_unavailable",
	ErrInternal:              "internal",
}

// Error is the concrete error of the package. Its message contains only the
// operation, the configured profile name, a bounded field name and a fixed
// reason: never token bytes, claim values, key material, secret paths or
// upstream library text.
type Error struct {
	// Code is the stable identifier of the category, e.g. "invalid_signature".
	Code string
	// Operation is the public operation that failed, e.g. "verify".
	Operation string
	// Profile is the configured profile name, when the operation had one.
	Profile string
	// Field names the offending config path, header or claim, when known.
	Field string
	// Reason is a short fixed description chosen by this package.
	Reason string

	kind  error
	cause error // only context errors, never third-party causes
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("jose: ")
	if e.Operation != "" {
		b.WriteString(e.Operation)
		b.WriteString(": ")
	}
	if e.Profile != "" {
		b.WriteString("profile ")
		b.WriteString(e.Profile)
		b.WriteString(": ")
	}
	b.WriteString(strings.TrimPrefix(e.kind.Error(), "jose: "))
	if e.Field != "" {
		b.WriteString(" (")
		b.WriteString(e.Field)
		b.WriteString(")")
	}
	if e.Reason != "" {
		b.WriteString(": ")
		b.WriteString(e.Reason)
	}
	return b.String()
}

// Is matches the sentinel of the error's category.
func (e *Error) Is(target error) bool { return target == e.kind }

// Unwrap exposes only context.Canceled / context.DeadlineExceeded, so callers
// can tell cancellation apart; third-party causes are never exposed.
func (e *Error) Unwrap() error { return e.cause }

func newError(kind error, op, profile, field, reason string) *Error {
	return &Error{Code: codes[kind], Operation: op, Profile: profile, Field: field, Reason: reason, kind: kind}
}

// ctxError converts a context error into a package error that still matches
// context.Canceled / context.DeadlineExceeded.
func ctxError(err error, op, profile string) error {
	e := newError(ErrInternal, op, profile, "", "context done")
	if errors.Is(err, context.DeadlineExceeded) {
		e.cause = context.DeadlineExceeded
	} else {
		e.cause = context.Canceled
	}
	return e
}

// withOp fills the operation and profile of a package error created by a
// lower layer that did not know them.
func withOp(err error, op, profile string) error {
	var e *Error
	if errors.As(err, &e) {
		c := *e
		if c.Operation == "" {
			c.Operation = op
		}
		if c.Profile == "" {
			c.Profile = profile
		}
		return &c
	}
	return newError(ErrInternal, op, profile, "", "unexpected failure")
}

func configError(field, reason string) error {
	return newError(ErrInvalidConfig, "config", "", field, reason)
}
