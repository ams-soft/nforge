package cache

import (
	"context"
	"errors"
	"strings"
)

// Sentinel categories. Every error returned by a Cache method (except the
// application errors returned by a GetOrLoad loader, which are passed
// through) matches one of them with errors.Is; details are in *Error.
var (
	ErrDisabled          = errors.New("cache: disabled")
	ErrClosed            = errors.New("cache: closed")
	ErrInvalidConfig     = errors.New("cache: invalid config")
	ErrUnsupportedDriver = errors.New("cache: unsupported driver")
	ErrUnsupportedCodec  = errors.New("cache: unsupported codec")
	ErrInvalidKey        = errors.New("cache: invalid key")
	ErrTenantRequired    = errors.New("cache: tenant required")
	ErrInvalidTTL        = errors.New("cache: invalid ttl")
	ErrInvalidTarget     = errors.New("cache: invalid target")
	ErrNotFound          = errors.New("cache: not found")
	ErrEncode            = errors.New("cache: encode failed")
	ErrDecode            = errors.New("cache: decode failed")
	ErrValueTooLarge     = errors.New("cache: value too large")
	ErrNotInteger        = errors.New("cache: value is not an integer")
	ErrCounterOverflow   = errors.New("cache: counter overflow")
	ErrUnavailable       = errors.New("cache: backend unavailable")
	// ErrOutcomeUnknown means a mutation may have reached Redis but no
	// authoritative reply arrived: it may or may not have been applied, and
	// it is never replayed automatically. It also matches ErrUnavailable.
	ErrOutcomeUnknown = errors.New("cache: outcome unknown")
	ErrLoadCapacity   = errors.New("cache: too many concurrent loads")
	ErrLoaderPanic    = errors.New("cache: loader panicked")
)

// Error is the typed error of the cache. Its message never contains keys,
// tenant IDs, cached contents, credentials, URLs or Redis error text: only
// the operation, the driver, the category and a bounded reason. Unwrap
// exposes only safe causes: context errors and framework codec errors.
type Error struct {
	Op     string // public method, e.g. "get", "set_if_absent"
	Driver string // "redis" | "memory" | "" (before a backend exists)
	Kind   error  // the sentinel category
	Reason string // bounded machine-readable detail, e.g. "timeout", "auth"

	also  []error // extra categories (ErrOutcomeUnknown also matches ErrUnavailable)
	cause error   // safe cause: context or codec error
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("cache")
	if e.Op != "" {
		b.WriteString(": ")
		b.WriteString(e.Op)
	}
	if e.Driver != "" {
		b.WriteString(" (")
		b.WriteString(e.Driver)
		b.WriteString(")")
	}
	b.WriteString(": ")
	b.WriteString(strings.TrimPrefix(e.Kind.Error(), "cache: ")) // context errors print as is
	if e.Reason != "" {
		b.WriteString(" (")
		b.WriteString(e.Reason)
		b.WriteString(")")
	}
	if e.cause != nil {
		b.WriteString(": ")
		b.WriteString(e.cause.Error())
	}
	return b.String()
}

// Is matches the category and the extra categories.
func (e *Error) Is(target error) bool {
	if target == e.Kind {
		return true
	}
	for _, a := range e.also {
		if target == a {
			return true
		}
	}
	return false
}

// Unwrap returns the safe cause (a context error or a codec error), if any.
func (e *Error) Unwrap() error { return e.cause }

func newError(kind error, reason string) *Error {
	return &Error{Kind: kind, Reason: reason}
}

func wrapError(kind error, reason string, cause error) *Error {
	return &Error{Kind: kind, Reason: reason, cause: cause}
}

// ctxError converts a context error into a cache error whose category is
// context.Canceled or context.DeadlineExceeded itself.
func ctxError(err error) *Error {
	if errors.Is(err, context.DeadlineExceeded) {
		return &Error{Kind: context.DeadlineExceeded}
	}
	return &Error{Kind: context.Canceled}
}

// withOp fills the operation and driver of err when it is a cache *Error,
// copying it so shared values (flight results) are never mutated.
func withOp(err error, op, driver string) error {
	var ce *Error
	if !errors.As(err, &ce) || err != error(ce) { //nolint:errorlint // identity on purpose: only an unwrapped *Error gets the op
		return err // loader errors and anything already wrapped pass through
	}
	cp := *ce
	if cp.Op == "" {
		cp.Op = op
	}
	if cp.Driver == "" {
		cp.Driver = driver
	}
	return &cp
}
