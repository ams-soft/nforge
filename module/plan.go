package module

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// MaxModules bounds an application, and with it the size of the graph output.
const MaxModules = 256

// Plan is a validated, frozen graph: the definitions of every registered
// module, the enabled ones in start order, and the resolved providers. Nothing
// after Build changes it.
type Plan struct {
	units     []*unit // registration order
	order     []*unit // enabled, in start order
	providers map[CapabilityID]*unit
	snapshot  Snapshot
}

type unit struct {
	id      ID
	section string
	src     Source
	def     Definition
	index   int
	enabled bool
	// deps and dependents hold the enabled modules this one uses and that use
	// it; position is the 0-based index in Plan.order.
	deps, dependents []*unit
	position         int
	// declared holds every capability the module may read, required or
	// optional, with the type it declared.
	declared map[CapabilityID]Handle
}

// Snapshot is the structural description of a plan and the single source of
// every graph representation. It holds no configuration values.
type Snapshot struct {
	// Modules holds every registered module, sorted by ID.
	Modules []ModuleInfo
	// Edges holds the dependencies between enabled modules, sorted.
	Edges []Edge
	// Order is the initialization order; shutdown runs it backwards.
	Order []ID
}

// ModuleInfo describes one module of a Snapshot.
type ModuleInfo struct {
	ID      ID
	Enabled bool
	Ingress bool
	// Provides lists the declared capabilities, sorted.
	Provides []CapabilityID
	// Dependencies and OptionalDependencies list the modules this one resolved
	// to, sorted. Both are empty for a disabled module.
	Dependencies         []ID
	OptionalDependencies []ID
	// StartupOrder is the 1-based initialization position; 0 when disabled.
	StartupOrder int
}

// Edge says that From uses Capability, published by To.
type Edge struct {
	From, To   ID
	Capability CapabilityID
	Required   bool
}

// Build configures every descriptor, resolves the graph and runs every Check.
// sources returns the Source of a module's section. It performs no I/O of its
// own: the Configure and Check functions must not either.
//
// Configuration errors of independent modules are aggregated, so one run
// reports every invalid section. Modules that are ready at the same time are
// ordered by registration, so a given configuration always yields one plan.
func Build(descs []Descriptor, sources func(d Descriptor) Source) (*Plan, error) {
	if len(descs) > MaxModules {
		return nil, fmt.Errorf("%w: %d modules, at most %d", ErrInvalidModule, len(descs), MaxModules)
	}
	ids := make(map[ID]bool, len(descs))
	sections := map[string]ID{}
	for _, d := range descs {
		if !validID(string(d.ID)) {
			return nil, fmt.Errorf("%w: id %q must be %s", ErrInvalidModule, d.ID, idRule)
		}
		if ids[d.ID] {
			return nil, fmt.Errorf("%w: %q", ErrDuplicateModule, d.ID)
		}
		ids[d.ID] = true
		if d.Configure == nil {
			return nil, fmt.Errorf("%w: module %q has no Configure", ErrInvalidModule, d.ID)
		}
		if d.Section != "" {
			if owner, taken := sections[d.Section]; taken {
				return nil, fmt.Errorf("%w: configuration section %q is owned by both %q and %q", ErrDuplicateModule, d.Section, owner, d.ID)
			}
			sections[d.Section] = d.ID
		}
	}

	p := &Plan{providers: map[CapabilityID]*unit{}}
	var errs []error
	for i, d := range descs {
		src := sources(d)
		def, err := configure(d, src)
		if err == nil {
			err = def.validate(d.ID)
		}
		if err != nil {
			errs = append(errs, &ConfigError{Module: d.ID, Err: err})
			continue
		}
		p.units = append(p.units, &unit{id: d.ID, section: d.Section, src: src, def: def, index: i, enabled: !def.Disabled})
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	if err := p.resolve(); err != nil {
		return nil, err
	}
	for _, u := range p.order {
		if u.def.Check == nil {
			continue
		}
		if err := check(u, &View{p: p, u: u}); err != nil {
			errs = append(errs, &ConfigError{Module: u.id, Err: err})
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return p, nil
}

// configure calls Configure, turning a panic into an error: a malformed section
// must fail the build, never the process.
func configure(d Descriptor, src Source) (def Definition, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("configure panicked: %v", r)
		}
	}()
	return d.Configure(src)
}

func check(u *unit, v *View) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("check panicked: %v", r)
		}
	}()
	return u.def.Check(v)
}

func (p *Plan) resolve() error {
	registered := map[CapabilityID][]*unit{}
	for _, u := range p.units {
		for _, h := range u.def.Provides {
			registered[h.ID()] = append(registered[h.ID()], u)
			if !u.enabled {
				continue
			}
			if other, taken := p.providers[h.ID()]; taken {
				return fmt.Errorf("%w: capability %q is provided by both %q and %q", ErrAmbiguousProvider, h.ID(), other.id, u.id)
			}
			p.providers[h.ID()] = u
		}
	}

	var edges []Edge
	for _, u := range p.units {
		if !u.enabled {
			continue
		}
		u.declared = map[CapabilityID]Handle{}
		link := func(h Handle, required bool) error {
			if prev, dup := u.declared[h.ID()]; dup {
				if prev.valueType() != h.valueType() {
					return mismatch(u.id, h, prev)
				}
				return nil
			}
			u.declared[h.ID()] = h
			provider, ok := p.providers[h.ID()]
			if !ok {
				if required {
					return missing(u.id, h.ID(), registered[h.ID()])
				}
				return checkRegisteredTypes(u.id, h, registered[h.ID()])
			}
			if err := checkRegisteredTypes(u.id, h, []*unit{provider}); err != nil {
				return err
			}
			edges = append(edges, Edge{From: u.id, To: provider.id, Capability: h.ID(), Required: required})
			if !slices.Contains(u.deps, provider) {
				u.deps = append(u.deps, provider)
				provider.dependents = append(provider.dependents, u)
			}
			return nil
		}
		for _, h := range u.def.Requires {
			if err := link(h, true); err != nil {
				return err
			}
		}
		for _, h := range u.def.Optional {
			if err := link(h, false); err != nil {
				return err
			}
		}
	}

	order, err := sortUnits(p.units)
	if err != nil {
		return err
	}
	p.order = order
	position := make(map[ID]int, len(order))
	for n, u := range order {
		u.position = n
		p.snapshot.Order = append(p.snapshot.Order, u.id)
		position[u.id] = n + 1
	}
	p.snapshot.Edges = sortEdges(edges)
	p.snapshot.Modules = describe(p.units, position, p.snapshot.Edges)
	return nil
}

// checkRegisteredTypes compares a consumer's declaration with what the
// registered providers declare for the same ID.
func checkRegisteredTypes(id ID, h Handle, providers []*unit) error {
	for _, pr := range providers {
		for _, ph := range pr.def.Provides {
			if ph.ID() == h.ID() && ph.valueType() != h.valueType() {
				return mismatch(id, h, ph)
			}
		}
	}
	return nil
}

func mismatch(id ID, h, other Handle) error {
	return fmt.Errorf("%w: module %q declares capability %q as %v, but it is declared as %v",
		ErrTypeMismatch, id, h.ID(), h.valueType(), other.valueType())
}

func missing(id ID, c CapabilityID, registered []*unit) error {
	if len(registered) == 0 {
		return fmt.Errorf("%w: module %q requires capability %q, which no registered module provides", ErrMissingDependency, id, c)
	}
	names := make([]string, len(registered))
	for i, u := range registered {
		names[i] = string(u.id)
	}
	return fmt.Errorf("%w: module %q requires capability %q, but its provider %s is disabled; enable it in the configuration",
		ErrDisabledDependency, id, c, strings.Join(names, ", "))
}

// sortUnits is Kahn's algorithm with the lowest registration index first.
func sortUnits(units []*unit) ([]*unit, error) {
	done := map[*unit]bool{}
	var order []*unit
	total := 0
	for _, u := range units {
		if u.enabled {
			total++
		}
	}
	for len(order) < total {
		var next *unit
		for _, u := range units {
			if !u.enabled || done[u] {
				continue
			}
			if !slices.ContainsFunc(u.deps, func(d *unit) bool { return !done[d] }) {
				next = u
				break
			}
		}
		if next == nil {
			return nil, cycleError(units, done)
		}
		done[next] = true
		order = append(order, next)
	}
	return order, nil
}

// cycleError walks unresolved dependencies from the first unresolved module.
// Every unresolved module waits for another one, so the walk must repeat a node.
func cycleError(units []*unit, done map[*unit]bool) error {
	var at *unit
	for _, u := range units {
		if u.enabled && !done[u] {
			at = u
			break
		}
	}
	var path []*unit
	for !slices.Contains(path, at) {
		path = append(path, at)
		waiting := slices.Clone(at.deps)
		slices.SortFunc(waiting, func(a, b *unit) int { return cmp.Compare(a.index, b.index) })
		at = waiting[slices.IndexFunc(waiting, func(d *unit) bool { return !done[d] })]
	}
	path = path[slices.Index(path, at):]
	names := make([]string, 0, len(path)+1)
	for _, u := range path {
		names = append(names, string(u.id))
	}
	names = append(names, string(at.id))
	return fmt.Errorf("%w: %s", ErrCycle, strings.Join(names, " -> "))
}

func sortEdges(edges []Edge) []Edge {
	slices.SortFunc(edges, func(a, b Edge) int {
		return cmp.Or(cmp.Compare(a.From, b.From), cmp.Compare(a.To, b.To), cmp.Compare(a.Capability, b.Capability))
	})
	return edges
}

func describe(units []*unit, position map[ID]int, edges []Edge) []ModuleInfo {
	infos := make([]ModuleInfo, len(units))
	for i, u := range units {
		provides := make([]CapabilityID, len(u.def.Provides))
		for j, h := range u.def.Provides {
			provides[j] = h.ID()
		}
		slices.Sort(provides)
		info := ModuleInfo{ID: u.id, Enabled: u.enabled, Ingress: u.def.Ingress, Provides: provides, StartupOrder: position[u.id]}
		for _, e := range edges {
			if e.From != u.id {
				continue
			}
			if e.Required {
				info.Dependencies = append(info.Dependencies, e.To)
			} else {
				info.OptionalDependencies = append(info.OptionalDependencies, e.To)
			}
		}
		slices.Sort(info.Dependencies)
		info.Dependencies = slices.Compact(info.Dependencies)
		slices.Sort(info.OptionalDependencies)
		info.OptionalDependencies = slices.Compact(info.OptionalDependencies)
		infos[i] = info
	}
	slices.SortFunc(infos, func(a, b ModuleInfo) int { return cmp.Compare(a.ID, b.ID) })
	return infos
}

// Snapshot returns a copy of the structure of the plan.
func (p *Plan) Snapshot() Snapshot { return p.snapshot.clone() }

func (s Snapshot) clone() Snapshot {
	out := Snapshot{Edges: slices.Clone(s.Edges), Order: slices.Clone(s.Order), Modules: slices.Clone(s.Modules)}
	for i, m := range out.Modules {
		out.Modules[i].Provides = slices.Clone(m.Provides)
		out.Modules[i].Dependencies = slices.Clone(m.Dependencies)
		out.Modules[i].OptionalDependencies = slices.Clone(m.OptionalDependencies)
	}
	return out
}

// View is what Check sees: the module's own source and the validated
// configuration of the providers it declared.
type View struct {
	p *Plan
	u *unit
}

// App returns the application metadata.
func (v *View) App() App { return v.u.src.App() }

// Warn records a weakening setting; see Source.Warn.
func (v *View) Warn(exception, msg string) { v.u.src.Warn(exception, msg) }

// Enabled reports whether the provider of a declared capability is enabled.
func (v *View) Enabled(h Handle) bool {
	_, ok := v.p.providers[h.ID()]
	return ok && v.u.declared[h.ID()] != nil
}

// ConfigOf returns the Definition.Config of the module providing h, which the
// checking module must have declared in Requires or Optional. enabled is false
// when that provider is registered but disabled; found is false when no
// registered module provides h. A config of another type than C is an error.
func ConfigOf[C any](v *View, h Handle) (cfg C, enabled, found bool, err error) {
	if _, ok := v.u.declared[h.ID()]; !ok {
		return cfg, false, false, fmt.Errorf("%w: %s read the configuration of %q without declaring it", ErrUndeclared, v.u.id, h.ID())
	}
	var provider *unit
	for _, u := range v.p.units {
		if slices.ContainsFunc(u.def.Provides, func(ph Handle) bool { return ph.ID() == h.ID() }) && (provider == nil || u.enabled) {
			provider = u
		}
	}
	if provider == nil {
		return cfg, false, false, nil
	}
	c, ok := provider.def.Config.(C)
	if !ok {
		return cfg, false, false, fmt.Errorf("%w: the configuration of %q is %T, not %T", ErrTypeMismatch, provider.id, provider.def.Config, cfg)
	}
	return c, provider.enabled, true, nil
}
