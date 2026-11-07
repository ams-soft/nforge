package module

import "errors"

var (
	// ErrInvalidModule reports a malformed descriptor or definition, or too many
	// modules.
	ErrInvalidModule = errors.New("module: invalid module")
	// ErrDuplicateModule reports two descriptors with the same ID, or two
	// owners of one configuration section.
	ErrDuplicateModule = errors.New("module: duplicate module")
	// ErrAmbiguousProvider reports two enabled modules providing one capability.
	ErrAmbiguousProvider = errors.New("module: ambiguous provider")
	// ErrMissingDependency reports a required capability that nothing provides.
	ErrMissingDependency = errors.New("module: missing dependency")
	// ErrDisabledDependency reports a required capability whose provider is
	// disabled. The provider is never enabled on the module's behalf.
	ErrDisabledDependency = errors.New("module: disabled dependency")
	// ErrTypeMismatch reports two declarations of one capability ID with
	// different Go types.
	ErrTypeMismatch = errors.New("module: capability type mismatch")
	// ErrCycle reports a dependency cycle.
	ErrCycle = errors.New("module: dependency cycle")
	// ErrNotProvided reports a module whose Init returned without publishing a
	// capability it declared.
	ErrNotProvided = errors.New("module: capability not provided")
	// ErrUndeclared reports access to a capability the module did not declare.
	ErrUndeclared = errors.New("module: undeclared capability")
	// ErrState reports a lifecycle call in the wrong state: Start twice, or
	// Start after Shutdown.
	ErrState = errors.New("module: invalid lifecycle state")
	// ErrStopTimeout reports a Stop hook that did not return before the
	// shutdown deadline (plus its bounded grace). The modules it depends on are
	// then left open rather than closed under it.
	ErrStopTimeout = errors.New("module: stop did not finish in time")
)

// ConfigError is the failure of one module's Configure or Check. Its text names
// the module; the cause is the module's own (already sanitized) message.
type ConfigError struct {
	Module ID
	Err    error
}

func (e *ConfigError) Error() string { return "module " + string(e.Module) + ": " + e.Err.Error() }

// Unwrap returns the cause.
func (e *ConfigError) Unwrap() error { return e.Err }

// Phase names a lifecycle hook, in errors.
type Phase string

// Lifecycle phases.
const (
	PhaseInit   Phase = "init"
	PhaseStart  Phase = "start"
	PhaseStop   Phase = "stop"
	PhaseReload Phase = "reload"
	PhaseRun    Phase = "run"
)

// LifecycleError is the failure of one module in one phase. Its text is the
// cause's, so a module keeps control of its (already sanitized) message.
type LifecycleError struct {
	Module ID
	Phase  Phase
	Err    error
}

func (e *LifecycleError) Error() string { return e.Err.Error() }

// Unwrap returns the cause.
func (e *LifecycleError) Unwrap() error { return e.Err }
