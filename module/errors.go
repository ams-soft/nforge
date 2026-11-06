package module

import "errors"

var (
	// ErrInvalidModule reports a malformed descriptor or too many modules.
	ErrInvalidModule = errors.New("module: invalid module")
	// ErrDuplicateModule reports two descriptors with the same ID.
	ErrDuplicateModule = errors.New("module: duplicate module")
	// ErrAmbiguousProvider reports two enabled modules providing one capability.
	ErrAmbiguousProvider = errors.New("module: ambiguous provider")
	// ErrMissingDependency reports a required capability that nothing provides.
	ErrMissingDependency = errors.New("module: missing dependency")
	// ErrDisabledDependency reports a required capability whose provider is
	// disabled. The provider is never enabled on the module's behalf.
	ErrDisabledDependency = errors.New("module: disabled dependency")
	// ErrCycle reports a dependency cycle.
	ErrCycle = errors.New("module: dependency cycle")
	// ErrNotProvided reports a module whose Init returned without publishing a
	// capability it declared.
	ErrNotProvided = errors.New("module: capability not provided")
)

// InitError is the failure of one module during Plan.Start. Its text is the
// cause's, so a module keeps control of its (already sanitized) message.
type InitError struct {
	Module ID
	Err    error
}

func (e *InitError) Error() string { return e.Err.Error() }

// Unwrap returns the cause.
func (e *InitError) Unwrap() error { return e.Err }
