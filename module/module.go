// Package module is the neutral core of an nForge application: typed
// capabilities, explicitly registered module descriptors, a deterministic
// dependency graph and the lifecycle that runs it. It knows nothing about any
// concrete module and imports only the standard library, so the resource
// packages (database, jose, cache, nhttp...) describe themselves here without
// depending on the orchestrator (package app).
//
// A module goes through three pure steps and then its lifecycle:
//
//  1. Configure decodes and validates the module's own configuration section
//     and returns its Definition: what it provides, requires and optionally
//     uses, and its hooks. No I/O.
//  2. Build resolves the graph of every Definition: duplicate IDs, ambiguous
//     providers, missing or disabled required providers, capability type
//     mismatches and cycles fail here, before anything is opened. A module is
//     never enabled implicitly.
//  3. Check runs on each enabled module with the resolved graph, so it can
//     validate its configuration against the validated configuration of the
//     providers it declared (a cache codec that the codec module must enable).
//
// Runtime.Start then initializes every module in dependency order (resources
// may be allocated, no external work is accepted), and only after every Init
// succeeded starts them: first the ordinary modules, then the ingress modules
// (HTTP listeners, queue consumers), so no request or job can arrive before
// every handler is registered. Runtime.Shutdown stops the ingress modules
// first and then the rest in reverse dependency order.
package module

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"time"
)

// ID identifies a module. It is stable, unique in an application and, because
// it reaches the /graph page and the readiness response, limited to lowercase
// letters, digits, '-' and '_'.
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

// Capability is a typed handle to a resource published by a module. Its Go type
// is part of its identity: a provider and a consumer that name the same ID with
// different types are a Build error (ErrTypeMismatch), never an unsafe cast.
// Capabilities are singletons per application, not process globals.
type Capability[T any] struct{ id CapabilityID }

// NewCapability returns the handle of the capability named id.
func NewCapability[T any](id CapabilityID) Capability[T] {
	return Capability[T]{id: id}
}

// ID returns the capability name.
func (c Capability[T]) ID() CapabilityID { return c.id }

// valueType is the Go type of the capability. It is used only to compare
// declarations, never to build or inject anything.
func (c Capability[T]) valueType() reflect.Type { return reflect.TypeFor[T]() }

// Logger is the capability under which a logging module publishes the
// application logger. It is defined here, with a standard library type, so
// every module (and the orchestrator) can declare it without importing the
// logging module: the logger never depends on its own consumers.
var Logger = NewCapability[*slog.Logger]("logger")

// Handle is any Capability, for the Provides, Requires and Optional lists.
type Handle interface {
	ID() CapabilityID
	valueType() reflect.Type
}

// Descriptor is the explicit registration of one module. Registration is
// authoritative: a configuration section cannot activate a module that was not
// registered, and a section that no registered module owns is an error.
type Descriptor struct {
	ID ID
	// Section is the top-level configuration section the module owns ("" for
	// none). Only its owner decodes it, strictly.
	Section string
	// Configure decodes and validates the module's section and returns its
	// definition. It must be pure: no I/O, no goroutines, no global state.
	Configure func(src Source) (Definition, error)
}

// New returns the descriptor of a module that has no configuration section,
// typically an application module that registers routes or consumers.
func New(id ID, def Definition) Descriptor {
	return Descriptor{ID: id, Configure: func(Source) (Definition, error) { return def, nil }}
}

// App is the application metadata every module may read.
type App struct {
	Name        string
	Version     string
	Env         string // local | dev | stg | prd
	InternalUse bool   // the service is not reachable from the internet
	OnlineSince time.Time
}

// Source is what Configure sees of the configuration: its own section and the
// application metadata. The orchestrator implements it.
type Source interface {
	// Decode decodes the module's section into dst, strictly (unknown keys are
	// errors), applying the environment overrides of the section. An absent
	// section leaves dst unchanged, so dst carries the defaults.
	Decode(dst any) error
	// Present reports whether the section appears in the configuration.
	Present() bool
	// App returns the application metadata.
	App() App
	// ProcessGlobals reports whether the application opted in to installing
	// process-wide defaults (slog.Default, codec.Default, the OTel globals).
	// The default is false, so several applications can share a process.
	ProcessGlobals() bool
	// Warn records a valid setting that weakens the deployment. exception is
	// the prd_exceptions key that accepts it in production ("" when it is only
	// ever a warning); with env prd and the exception not set the application
	// refuses to start.
	Warn(exception, msg string)
}

// Definition is what a configured module declares.
type Definition struct {
	// Disabled keeps a registered module out of the run (an "enabled: false"
	// section). A required capability whose provider is disabled is an error.
	Disabled bool
	// Provides lists the capabilities Init must publish. Two enabled modules
	// providing the same capability are an error; a disabled alternative is not.
	Provides []Handle
	// Requires lists the capabilities the module cannot run without.
	Requires []Handle
	// Optional lists capabilities the module uses when their provider is
	// enabled. They order the module after that provider and create no
	// obligation.
	Optional []Handle
	// Ingress marks a module that accepts external work (listeners, queue
	// consumers). It starts after every other module and stops before them.
	// A module that depends on an ingress module may use it only to register
	// (routes, handlers) during Init.
	Ingress bool
	// NonCritical keeps the module's readiness out of the application's: a
	// failing probe shows the module as not ready, the application stays ready.
	NonCritical bool
	// Config is the validated configuration, readable by the modules that
	// declare one of its capabilities (ConfigOf). It is never printed by the
	// framework.
	Config any
	// Check validates the definition against the resolved graph. Nil skips.
	Check func(v *View) error
	// Init allocates the module's resources and publishes its capabilities. It
	// must not accept external work, and it must release what it created before
	// returning an error. Nil marks a module with nothing to initialize.
	Init func(ctx context.Context, s *Scope) (Hooks, error)
}

// Hooks are the operations of an initialized module. All may be nil.
type Hooks struct {
	// Start makes the service active and returns once it is (listeners bound,
	// consumers subscribed); it must not block on a serve loop. Goroutines it
	// starts belong to the module, which reports their unexpected end with
	// Scope.ReportFailure.
	Start func(ctx context.Context) error
	// Ready is the readiness probe. It honors ctx, changes nothing and may run
	// concurrently with itself; a recoverable outage returns an error.
	Ready func(ctx context.Context) error
	// Stop stops the module and releases what it owns, never what its
	// dependencies own. It runs at most once, also for a module that was
	// initialized but never started. An ingress module stops accepting work,
	// drains what is in flight and closes its listeners.
	Stop func(ctx context.Context) error
	// Reload re-reads external credentials (certificates, key files) without a
	// restart. A failure keeps the previous state.
	Reload func(ctx context.Context) error
}

// validate checks what can be checked without looking at other modules.
func (d Definition) validate(id ID) error {
	seen := make(map[CapabilityID]bool, len(d.Provides))
	for _, h := range d.Provides {
		if err := validHandle(id, h); err != nil {
			return err
		}
		if seen[h.ID()] {
			return fmt.Errorf("%w: module %q provides %q twice", ErrInvalidModule, id, h.ID())
		}
		seen[h.ID()] = true
	}
	for _, list := range [][]Handle{d.Requires, d.Optional} {
		for _, h := range list {
			if err := validHandle(id, h); err != nil {
				return err
			}
		}
	}
	return nil
}

func validHandle(id ID, h Handle) error {
	if h == nil {
		return fmt.Errorf("%w: module %q declares a nil capability", ErrInvalidModule, id)
	}
	if !validID(string(h.ID())) {
		return fmt.Errorf("%w: module %q: capability %q must be %s", ErrInvalidModule, id, h.ID(), idRule)
	}
	return nil
}
