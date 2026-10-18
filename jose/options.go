package jose

import (
	"context"
	"time"
)

// Option customizes a Service at construction time.
type Option func(*options) error

type options struct {
	clock      func() time.Time
	validators map[string]ClaimsValidator
}

// ClaimsValidator runs application checks (disabled accounts, tenant
// membership, revocation lists...) after every framework validation of a
// JWT profile succeeded, both when issuing and when validating.
//
// The custom argument differs by path: on IssueJWT it is the normalized
// custom object as map[string]any (numbers as json.Number); on ValidateJWT it
// is a pointer to the temporary decoded target when the caller passed one, or
// that same normalized map otherwise. Validators used on both paths should
// not assume one concrete application type.
//
// Return an error matching ErrClaimsRejected to reject the token, or
// ErrValidationUnavailable when a dependency is down; any other error is
// treated as an internal validation failure (never as success).
type ClaimsValidator func(ctx context.Context, registered RegisteredClaims, custom any) error

// WithClock replaces time.Now for issuance and validation (tests). The
// function must be safe for concurrent use and is never used for entropy.
func WithClock(now func() time.Time) Option {
	return func(o *options) error {
		if now == nil {
			return newError(ErrInvalidConfig, "new", "", "clock", "nil clock")
		}
		o.clock = now
		return nil
	}
}

// WithClaimsValidator registers the application validator of a JWT profile.
// One validator per profile; unknown and non-JWT profiles are rejected by New.
func WithClaimsValidator(profile string, fn ClaimsValidator) Option {
	return func(o *options) error {
		if fn == nil {
			return newError(ErrInvalidConfig, "new", profile, "validator", "nil validator")
		}
		if _, dup := o.validators[profile]; dup {
			return newError(ErrInvalidConfig, "new", profile, "validator", "duplicate validator")
		}
		o.validators[profile] = fn
		return nil
	}
}
