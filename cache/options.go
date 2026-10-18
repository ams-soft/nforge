package cache

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/ams-soft/nforge/codec"
)

// Option configures Open. Nil values and conflicting duplicates are
// rejected by Open with ErrInvalidConfig.
type Option func(*options) error

type options struct {
	registry *codec.Registry
	logger   *slog.Logger
	tenant   func(context.Context) (string, error)

	env, app     string
	hasNamespace bool

	now func() time.Time // private test clock of the memory driver
}

// WithCodecRegistry binds the registry used for object values. Without it,
// Open captures codec.Default() once; the registry never changes afterwards.
func WithCodecRegistry(registry *codec.Registry) Option {
	return func(o *options) error {
		if registry == nil {
			return invalid("WithCodecRegistry: nil registry")
		}
		if o.registry != nil && o.registry != registry {
			return invalid("WithCodecRegistry given twice with different registries")
		}
		o.registry = registry
		return nil
	}
}

// WithNamespace provides the trusted environment and application name used
// by the default prefix nforge:<env>:[<tenant>:]<app>:. It is required when
// cache.prefix is empty.
func WithNamespace(env, appName string) Option {
	return func(o *options) error {
		if env == "" || appName == "" {
			return invalid("WithNamespace: env and app name must not be empty")
		}
		if o.hasNamespace && (o.env != env || o.app != appName) {
			return invalid("WithNamespace given twice with different values")
		}
		o.env, o.app, o.hasNamespace = env, appName, true
		return nil
	}
}

// WithTenantResolver enables tenant isolation: every operation resolves the
// tenant from its context and inserts it into the key namespace; a missing
// tenant fails with ErrTenantRequired before any I/O. It is an adapter to
// the framework's existing tenant context (deps binds
// database.TenantFromContext), not a separate tenant system. The resolver
// must return a trusted, already validated ID.
func WithTenantResolver(resolve func(context.Context) (string, error)) Option {
	return func(o *options) error {
		if resolve == nil {
			return invalid("WithTenantResolver: nil resolver")
		}
		if o.tenant != nil {
			return invalid("WithTenantResolver given twice")
		}
		o.tenant = resolve
		return nil
	}
}

// WithLogger sets the logger for the few events the cache logs itself
// (best-effort GetOrLoad write failures). Default: a discarding logger.
func WithLogger(logger *slog.Logger) Option {
	return func(o *options) error {
		if logger == nil {
			return invalid("WithLogger: nil logger")
		}
		if o.logger != nil && o.logger != logger {
			return invalid("WithLogger given twice with different loggers")
		}
		o.logger = logger
		return nil
	}
}

// withClock replaces time.Now in the memory driver (tests only).
func withClock(now func() time.Time) Option {
	return func(o *options) error {
		o.now = now
		return nil
	}
}

func buildOptions(opts []Option) (options, error) {
	var o options
	var errs []error
	for i, opt := range opts {
		if opt == nil {
			errs = append(errs, invalid("option %d is nil", i))
			continue
		}
		if err := opt(&o); err != nil {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return o, fmt.Errorf("cache: options: %w", err)
	}
	if o.registry == nil {
		o.registry = codec.Default()
	}
	if o.logger == nil {
		o.logger = slog.New(slog.DiscardHandler)
	}
	if o.now == nil {
		o.now = time.Now
	}
	return o, nil
}
