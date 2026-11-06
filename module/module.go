// Package module is the neutral core of the nForge bootstrap: it resolves a
// set of explicitly registered modules into one deterministic, acyclic start
// plan and runs their lifecycle. It knows nothing about any concrete module
// and imports only the standard library, so the resource packages (database,
// jose, cache...) never depend on the orchestrator; package deps holds their
// descriptors.
//
// The flow is two-phased on purpose. Resolve validates the whole graph and does
// no I/O, so a missing or disabled dependency fails before a connection,
// goroutine or listener exists. Plan.Start then initializes the enabled modules
// in order and rolls back on failure. A module is never enabled implicitly: a
// required capability whose provider is disabled is an error.
package module

import (
	"context"
	"fmt"
)

// ID identifies a module. It is stable, unique in a plan and, because it reaches
// the /graph page, limited to lowercase letters, digits, '-' and '_'.
type ID string

// CapabilityID names something a module publishes. It follows the rules of ID.
type CapabilityID string

// idRule describes validID, for error messages.
const idRule = "1 to 64 characters: a lowercase letter, then lowercase letters, digits, '-' or '_'"

// validID bounds what a module can put in the graph output, so identifiers never
// need escaping to stay inert in HTML, Mermaid or JSON. It is a loop and not a
// regexp because compiling that repetition is the main cost of a bootstrap.
func validID(id string) bool {
	if id == "" || len(id) > 64 || id[0] < 'a' || id[0] > 'z' {
		return false
	}
	for i := 1; i < len(id); i++ {
		c := id[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

// Capability is a typed handle to a resource published by a module. Consumers
// declare the ID in Module.Requires or Module.Optional and read the value through
// Require or Lookup, so the graph always shows who uses what.
type Capability[T any] struct{ id CapabilityID }

// NewCapability returns the handle of the capability named id.
func NewCapability[T any](id CapabilityID) Capability[T] {
	return Capability[T]{id: id}
}

// ID returns the capability name, for Module.Provides, Requires and Optional.
func (c Capability[T]) ID() CapabilityID { return c.id }

// Hooks are the operations of a started module. Both may be nil.
type Hooks struct {
	// Ready is an operational probe: it honors ctx, changes nothing and may run
	// concurrently with itself.
	Ready func(ctx context.Context) error
	// Shutdown releases what the module owns. The runtime calls it at most once;
	// a module closes only its own resources, never those of its dependencies.
	Shutdown func(ctx context.Context) error
}

// Module describes one module for the configuration type C. The functions
// receive the already normalized configuration and must be pure: they are called
// once, by Resolve.
type Module[C any] struct {
	ID ID
	// Provides lists the capabilities Init must publish. Two enabled modules
	// providing the same capability are an error; a disabled alternative is not.
	Provides []CapabilityID
	// Enabled decides whether the module runs. Nil means always.
	Enabled func(cfg C) bool
	// Requires lists the capabilities the module cannot run without, given cfg.
	// Only evaluated for enabled modules.
	Requires func(cfg C) []CapabilityID
	// Optional lists capabilities the module uses when their provider is enabled.
	// They order the module after that provider and create no obligation.
	Optional func(cfg C) []CapabilityID
	// Init starts the module and publishes its capabilities in s. It must release
	// what it created before returning an error. Nil marks a passive module: it
	// appears in the graph but its lifecycle belongs to something else.
	Init func(ctx context.Context, cfg C, s *Scope) (Hooks, error)
}

// validate checks what can be checked without looking at other modules.
func (m Module[C]) validate() error {
	if !validID(string(m.ID)) {
		return fmt.Errorf("%w: id %q must be %s", ErrInvalidModule, m.ID, idRule)
	}
	seen := make(map[CapabilityID]bool, len(m.Provides))
	for _, c := range m.Provides {
		if !validID(string(c)) {
			return fmt.Errorf("%w: module %q: capability %q must be %s", ErrInvalidModule, m.ID, c, idRule)
		}
		if seen[c] {
			return fmt.Errorf("%w: module %q provides %q twice", ErrInvalidModule, m.ID, c)
		}
		seen[c] = true
	}
	return nil
}
