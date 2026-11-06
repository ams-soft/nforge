package module

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
)

// MaxModules bounds a plan, and with it the size of the graph output.
const MaxModules = 256

// Plan is a validated start order. It is immutable: nothing after Resolve
// changes the structure, which stays valid until shutdown.
type Plan[C any] struct {
	cfg      C
	modules  []Module[C] // enabled, in start order
	reads    map[ID]map[CapabilityID]bool
	snapshot Snapshot
}

// Snapshot is the structural description of a plan and the single source of
// every graph representation. It holds no configuration values.
type Snapshot struct {
	// Modules holds every registered module, sorted by ID.
	Modules []ModuleInfo
	// Edges holds the dependencies between enabled modules, sorted.
	Edges []Edge
	// Order is the start order; shutdown runs it backwards.
	Order []ID
}

// ModuleInfo describes one module of a Snapshot.
type ModuleInfo struct {
	ID      ID
	Enabled bool
	// Provides lists the declared capabilities, sorted.
	Provides []CapabilityID
	// Dependencies and OptionalDependencies list the modules this one resolved
	// to, sorted. Both are empty for a disabled module.
	Dependencies         []ID
	OptionalDependencies []ID
	// StartupOrder is the 1-based start position; 0 for a disabled module.
	StartupOrder int
}

// Edge says that From uses Capability, published by To.
type Edge struct {
	From, To   ID
	Capability CapabilityID
	Required   bool
}

// Resolve validates mods against cfg and computes the start order. It calls
// only Enabled, Requires and Optional and performs no I/O. Modules that are
// ready at the same time start in registration order, so a given configuration
// always yields the same plan.
func Resolve[C any](cfg C, mods []Module[C]) (*Plan[C], error) {
	if len(mods) > MaxModules {
		return nil, fmt.Errorf("%w: %d modules, at most %d", ErrInvalidModule, len(mods), MaxModules)
	}
	index := make(map[ID]int, len(mods))
	enabled := make([]bool, len(mods))
	providers := map[CapabilityID][]int{} // every registered provider
	active := map[CapabilityID]int{}      // the enabled one
	for i, m := range mods {
		if err := m.validate(); err != nil {
			return nil, err
		}
		if _, dup := index[m.ID]; dup {
			return nil, fmt.Errorf("%w: %q", ErrDuplicateModule, m.ID)
		}
		index[m.ID] = i
		enabled[i] = m.Enabled == nil || m.Enabled(cfg)
		for _, c := range m.Provides {
			providers[c] = append(providers[c], i)
			if !enabled[i] {
				continue
			}
			if other, taken := active[c]; taken {
				return nil, fmt.Errorf("%w: capability %q is provided by both %q and %q", ErrAmbiguousProvider, c, mods[other].ID, m.ID)
			}
			active[c] = i
		}
	}

	var edges []Edge
	reads := make(map[ID]map[CapabilityID]bool, len(mods))
	deps := make([][]int, len(mods)) // deps[i]: providers module i waits for
	link := func(i int, c CapabilityID, required bool) {
		p := active[c]
		edges = append(edges, Edge{From: mods[i].ID, To: mods[p].ID, Capability: c, Required: required})
		reads[mods[i].ID][c] = true
		if !slices.Contains(deps[i], p) {
			deps[i] = append(deps[i], p)
		}
	}
	for i, m := range mods {
		if !enabled[i] {
			continue
		}
		reads[m.ID] = map[CapabilityID]bool{}
		if m.Requires != nil {
			for _, c := range m.Requires(cfg) {
				if _, ok := active[c]; !ok {
					return nil, missing(m.ID, c, providers[c], mods)
				}
				if !reads[m.ID][c] {
					link(i, c, true)
				}
			}
		}
		if m.Optional != nil {
			for _, c := range m.Optional(cfg) {
				if _, ok := active[c]; ok && !reads[m.ID][c] {
					link(i, c, false)
				}
			}
		}
	}

	order, err := sortModules(mods, enabled, deps)
	if err != nil {
		return nil, err
	}
	plan := &Plan[C]{cfg: cfg, reads: reads}
	position := make(map[ID]int, len(order))
	for n, i := range order {
		plan.modules = append(plan.modules, mods[i])
		plan.snapshot.Order = append(plan.snapshot.Order, mods[i].ID)
		position[mods[i].ID] = n + 1
	}
	plan.snapshot.Edges = sortEdges(edges)
	plan.snapshot.Modules = describe(mods, enabled, position, plan.snapshot.Edges)
	return plan, nil
}

func missing[C any](id ID, c CapabilityID, registered []int, mods []Module[C]) error {
	if len(registered) == 0 {
		return fmt.Errorf("%w: module %q requires capability %q, which no module provides", ErrMissingDependency, id, c)
	}
	names := make([]string, len(registered))
	for i, p := range registered {
		names[i] = string(mods[p].ID)
	}
	return fmt.Errorf("%w: module %q requires capability %q, but its provider %s is disabled; enable it in the configuration",
		ErrDisabledDependency, id, c, strings.Join(names, ", "))
}

// sortModules is Kahn's algorithm with the lowest registration index first.
func sortModules[C any](mods []Module[C], enabled []bool, deps [][]int) ([]int, error) {
	done := make([]bool, len(mods))
	var order []int
	total := 0
	for i := range mods {
		if enabled[i] {
			total++
		}
	}
	for len(order) < total {
		next := -1
		for i := range mods {
			if !enabled[i] || done[i] {
				continue
			}
			if !slices.ContainsFunc(deps[i], func(p int) bool { return !done[p] }) {
				next = i
				break
			}
		}
		if next < 0 {
			return nil, cycleError(mods, enabled, done, deps)
		}
		done[next] = true
		order = append(order, next)
	}
	return order, nil
}

// cycleError walks unresolved dependencies from the first unresolved module.
// Every unresolved module waits for another one, so the walk must repeat a node.
func cycleError[C any](mods []Module[C], enabled, done []bool, deps [][]int) error {
	at := 0
	for !enabled[at] || done[at] {
		at++
	}
	var path []int
	for !slices.Contains(path, at) {
		path = append(path, at)
		waiting := slices.Clone(deps[at])
		slices.Sort(waiting)
		at = waiting[slices.IndexFunc(waiting, func(p int) bool { return !done[p] })]
	}
	path = path[slices.Index(path, at):]
	names := make([]string, 0, len(path)+1)
	for _, i := range path {
		names = append(names, string(mods[i].ID))
	}
	names = append(names, string(mods[at].ID))
	return fmt.Errorf("%w: %s", ErrCycle, strings.Join(names, " -> "))
}

func sortEdges(edges []Edge) []Edge {
	slices.SortFunc(edges, func(a, b Edge) int {
		return cmp.Or(cmp.Compare(a.From, b.From), cmp.Compare(a.To, b.To), cmp.Compare(a.Capability, b.Capability))
	})
	return edges
}

func describe[C any](mods []Module[C], enabled []bool, position map[ID]int, edges []Edge) []ModuleInfo {
	infos := make([]ModuleInfo, len(mods))
	for i, m := range mods {
		provides := slices.Clone(m.Provides)
		slices.Sort(provides)
		info := ModuleInfo{ID: m.ID, Enabled: enabled[i], Provides: provides, StartupOrder: position[m.ID]}
		for _, e := range edges {
			if e.From != m.ID {
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
func (p *Plan[C]) Snapshot() Snapshot { return p.snapshot.clone() }

func (s Snapshot) clone() Snapshot {
	out := Snapshot{Edges: slices.Clone(s.Edges), Order: slices.Clone(s.Order), Modules: slices.Clone(s.Modules)}
	for i, m := range out.Modules {
		out.Modules[i].Provides = slices.Clone(m.Provides)
		out.Modules[i].Dependencies = slices.Clone(m.Dependencies)
		out.Modules[i].OptionalDependencies = slices.Clone(m.OptionalDependencies)
	}
	return out
}
