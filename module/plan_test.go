package module

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
)

// source is a Source without configuration.
type source struct {
	mu       sync.Mutex
	warnings []string
}

func (*source) Decode(any) error     { return nil }
func (*source) Present() bool        { return false }
func (*source) App() App             { return App{Name: "test", Env: "local"} }
func (*source) ProcessGlobals() bool { return false }
func (s *source) Warn(exception, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.warnings = append(s.warnings, exception+": "+msg)
}

func build(descs ...Descriptor) (*Plan, error) {
	src := &source{}
	return Build(descs, func(Descriptor) Source { return src })
}

func capOf(id ID) Capability[string] { return NewCapability[string](CapabilityID(id)) }

func handles(ids ...ID) []Handle {
	out := make([]Handle, len(ids))
	for i, id := range ids {
		out[i] = capOf(id)
	}
	return out
}

// mod is a module providing a capability named after itself.
func mod(id ID, requires ...ID) Descriptor {
	return New(id, def(id, requires...))
}

func def(id ID, requires ...ID) Definition {
	c := capOf(id)
	return Definition{
		Provides: []Handle{c}, Requires: handles(requires...),
		Init: func(_ context.Context, s *Scope) (Hooks, error) {
			Provide(s, c, string(id))
			return Hooks{}, nil
		},
	}
}

func with(d Descriptor, edit func(*Definition)) Descriptor {
	inner := d.Configure
	d.Configure = func(src Source) (Definition, error) {
		def, err := inner(src)
		edit(&def)
		return def, err
	}
	return d
}

func off(d Descriptor) Descriptor { return with(d, func(def *Definition) { def.Disabled = true }) }

func TestBuildOrdersDependenciesFirst(t *testing.T) {
	plan, err := build(mod("c", "b"), mod("b", "a"), mod("a"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := plan.Snapshot().Order, []ID{"a", "b", "c"}; !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

// Independent modules keep the registration order, so one configuration always
// gives one plan.
func TestBuildIsDeterministic(t *testing.T) {
	mods := []Descriptor{mod("z"), mod("m"), mod("a"), mod("k", "a"), mod("b", "z")}
	want := []ID{"z", "m", "a", "k", "b"}
	for range 100 {
		plan, err := build(mods...)
		if err != nil {
			t.Fatal(err)
		}
		if got := plan.Snapshot().Order; !slices.Equal(got, want) {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

func TestBuildErrors(t *testing.T) {
	selfOptional := with(mod("x"), func(d *Definition) { d.Optional = handles("x") })
	wrongType := New("user", Definition{Requires: []Handle{NewCapability[int]("a")}})
	twiceDifferently := New("user", Definition{Requires: []Handle{capOf("a")}, Optional: []Handle{NewCapability[int]("a")}})
	optionalWrongType := New("user", Definition{Optional: []Handle{NewCapability[int]("a")}})
	tests := []struct {
		name string
		mods []Descriptor
		want error
		text string
	}{
		{name: "duplicate id", mods: []Descriptor{mod("a"), mod("a")}, want: ErrDuplicateModule, text: `"a"`},
		{name: "duplicate section", mods: []Descriptor{{ID: "a", Section: "s", Configure: mod("a").Configure}, {ID: "b", Section: "s", Configure: mod("b").Configure}}, want: ErrDuplicateModule, text: `section "s"`},
		{name: "invalid id", mods: []Descriptor{mod("<script>")}, want: ErrInvalidModule},
		{name: "empty id", mods: []Descriptor{mod("")}, want: ErrInvalidModule},
		{name: "no configure", mods: []Descriptor{{ID: "a"}}, want: ErrInvalidModule},
		{name: "invalid capability", mods: []Descriptor{New("a", Definition{Provides: handles("A B")})}, want: ErrInvalidModule},
		{name: "nil capability", mods: []Descriptor{New("a", Definition{Requires: []Handle{nil}})}, want: ErrInvalidModule},
		{name: "capability twice", mods: []Descriptor{New("a", Definition{Provides: handles("c", "c")})}, want: ErrInvalidModule},
		{
			name: "ambiguous provider",
			mods: []Descriptor{New("a", Definition{Provides: handles("c")}), New("b", Definition{Provides: handles("c")})},
			want: ErrAmbiguousProvider, text: `"a" and "b"`,
		},
		{name: "missing", mods: []Descriptor{mod("a", "nope")}, want: ErrMissingDependency, text: `"nope"`},
		{
			name: "disabled", mods: []Descriptor{mod("a", "b"), off(mod("b"))},
			want: ErrDisabledDependency, text: `requires capability "b", but its provider b is disabled`,
		},
		{name: "wrong type", mods: []Descriptor{mod("a"), wrongType}, want: ErrTypeMismatch, text: `"a" as int`},
		{name: "declared twice with two types", mods: []Descriptor{mod("a"), twiceDifferently}, want: ErrTypeMismatch},
		{name: "optional of a disabled provider with another type", mods: []Descriptor{off(mod("a")), optionalWrongType}, want: ErrTypeMismatch},
		{name: "self", mods: []Descriptor{mod("a", "a")}, want: ErrCycle, text: "a -> a"},
		{name: "cycle", mods: []Descriptor{mod("a", "b"), mod("b", "c"), mod("c", "a")}, want: ErrCycle, text: "a -> b -> c -> a"},
		{name: "cycle behind a module", mods: []Descriptor{mod("z", "a"), mod("a", "b"), mod("b", "a")}, want: ErrCycle, text: "a -> b -> a"},
		{name: "optional cycle", mods: []Descriptor{selfOptional}, want: ErrCycle},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := build(tt.mods...)
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			if !strings.Contains(err.Error(), tt.text) {
				t.Fatalf("err = %q, want it to contain %q", err, tt.text)
			}
		})
	}
}

// Every invalid section is reported in one run, and nothing is initialized.
func TestBuildAggregatesConfigurationErrors(t *testing.T) {
	inits := 0
	bad := func(id ID, err error) Descriptor {
		return Descriptor{ID: id, Section: string(id), Configure: func(Source) (Definition, error) {
			return Definition{Init: func(context.Context, *Scope) (Hooks, error) { inits++; return Hooks{}, nil }}, err
		}}
	}
	errA, errB := errors.New("a: port must be positive"), errors.New("b: unknown key")
	panics := Descriptor{ID: "p", Configure: func(Source) (Definition, error) { panic("bad section") }}
	_, err := build(bad("a", errA), mod("fine"), bad("b", errB), panics)
	if !errors.Is(err, errA) || !errors.Is(err, errB) || !strings.Contains(err.Error(), "configure panicked: bad section") {
		t.Fatalf("err = %v", err)
	}
	var ce *ConfigError
	if !errors.As(err, &ce) || ce.Module != "a" || !strings.HasPrefix(err.Error(), "module a: ") {
		t.Fatalf("err = %v", err)
	}
	if inits != 0 {
		t.Fatal("Build must not initialize anything")
	}
}

// Check runs after the graph is resolved and sees its providers' configuration.
func TestCheckReadsDeclaredProviderConfig(t *testing.T) {
	type codecCfg struct{ formats []string }
	codec := NewCapability[string]("codec")
	provider := New("codec", Definition{Provides: []Handle{codec}, Config: codecCfg{formats: []string{"json"}}})
	var seen codecCfg
	var enabled, found bool
	consumer := New("cache", Definition{Requires: []Handle{codec}, Check: func(v *View) error {
		var err error
		seen, enabled, found, err = ConfigOf[codecCfg](v, codec)
		if err != nil {
			return err
		}
		if !slices.Contains(seen.formats, "cbor") {
			return errors.New("cache.codec cbor must be enabled in the codec module")
		}
		return nil
	}})
	_, err := build(provider, consumer)
	if err == nil || !strings.Contains(err.Error(), "module cache: cache.codec cbor") {
		t.Fatalf("err = %v", err)
	}
	if !enabled || !found || seen.formats[0] != "json" {
		t.Fatalf("ConfigOf = %+v %v %v", seen, enabled, found)
	}

	undeclared := New("x", Definition{Check: func(v *View) error {
		_, _, _, err := ConfigOf[codecCfg](v, codec)
		return err
	}})
	if _, err := build(provider, undeclared); !errors.Is(err, ErrUndeclared) {
		t.Fatalf("undeclared: err = %v", err)
	}
	wrong := New("y", Definition{Requires: []Handle{codec}, Check: func(v *View) error {
		_, _, _, err := ConfigOf[int](v, codec)
		return err
	}})
	if _, err := build(provider, wrong); !errors.Is(err, ErrTypeMismatch) {
		t.Fatalf("wrong type: err = %v", err)
	}
	// a disabled provider still shares its configuration with optional users
	optional := New("z", Definition{Optional: []Handle{codec}, Check: func(v *View) error {
		_, enabled, found, err = ConfigOf[codecCfg](v, codec)
		if v.Enabled(codec) {
			return errors.New("the provider is disabled")
		}
		return err
	}})
	if _, err := build(off(provider), optional); err != nil || enabled || !found {
		t.Fatalf("disabled provider: err = %v, enabled %v, found %v", err, enabled, found)
	}
}

func TestCheckWarnings(t *testing.T) {
	src := &source{}
	m := New("a", Definition{Check: func(v *View) error { v.Warn("redis_plaintext", "redis without TLS"); return nil }})
	if _, err := Build([]Descriptor{m}, func(Descriptor) Source { return src }); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(src.warnings, []string{"redis_plaintext: redis without TLS"}) {
		t.Fatalf("warnings = %v", src.warnings)
	}
}

func TestValidID(t *testing.T) {
	for id, want := range map[string]bool{
		"a": true, "a-b_c9": true, strings.Repeat("a", 64): true, strings.Repeat("a", 65): false,
		"": false, "1a": false, "-a": false, "A": false, "a b": false, "a.b": false, `a"`: false, "a\n": false, "é": false,
	} {
		if got := validID(id); got != want {
			t.Errorf("%q = %v", id, got)
		}
	}
}

func TestBuildLimitsModules(t *testing.T) {
	mods := make([]Descriptor, MaxModules+1)
	for i := range mods {
		mods[i] = New(ID(fmt.Sprintf("m%d", i)), Definition{})
	}
	if _, err := build(mods...); !errors.Is(err, ErrInvalidModule) {
		t.Fatalf("err = %v", err)
	}
	if _, err := build(mods[:MaxModules]...); err != nil {
		t.Fatal(err)
	}
}

// A disabled module's requirements do not count at all.
func TestDisabledModuleRequiresNothing(t *testing.T) {
	if _, err := build(off(mod("db")), off(mod("consumer", "db"))); err != nil {
		t.Fatal(err)
	}
}

// Two alternatives may be registered as long as one is enabled.
func TestBuildDisabledAlternativeIsNotAmbiguous(t *testing.T) {
	a := off(New("a", Definition{Provides: handles("c")}))
	b := New("b", Definition{Provides: handles("c")})
	plan, err := build(a, b, mod("user", "c"))
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.Snapshot().Edges; len(got) != 1 || got[0].To != "b" {
		t.Fatalf("edges = %+v", got)
	}
}

func TestOptionalDependency(t *testing.T) {
	user := with(mod("user"), func(d *Definition) { d.Optional = handles("extra", "unregistered") })
	// the optional provider is registered after its consumer: it still starts first
	plan, err := build(user, mod("extra"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := plan.Snapshot().Order, []ID{"extra", "user"}; !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	plan, err = build(user, off(mod("extra")))
	if err != nil {
		t.Fatal("a disabled optional provider is not an error:", err)
	}
	if got := plan.Snapshot().Edges; len(got) != 0 {
		t.Fatalf("edges = %+v", got)
	}
}

func TestSnapshot(t *testing.T) {
	user := with(mod("user", "b", "b", "a"), func(d *Definition) { d.Optional = handles("b", "c"); d.Ingress = true })
	plan, err := build(off(mod("off")), mod("c"), mod("b"), mod("a"), user)
	if err != nil {
		t.Fatal(err)
	}
	snap := plan.Snapshot()
	ids := make([]ID, len(snap.Modules))
	for i, m := range snap.Modules {
		ids[i] = m.ID
	}
	if want := []ID{"a", "b", "c", "off", "user"}; !slices.Equal(ids, want) {
		t.Fatalf("modules = %v", ids)
	}
	if off := snap.Modules[3]; off.Enabled || off.StartupOrder != 0 {
		t.Fatalf("off = %+v", off)
	}
	u := snap.Modules[4]
	if !slices.Equal(u.Dependencies, []ID{"a", "b"}) || !slices.Equal(u.OptionalDependencies, []ID{"c"}) || u.StartupOrder != 4 || !u.Ingress {
		t.Fatalf("user = %+v", u)
	}
	want := []Edge{{"user", "a", "a", true}, {"user", "b", "b", true}, {"user", "c", "c", false}}
	if !slices.Equal(snap.Edges, want) {
		t.Fatalf("edges = %+v", snap.Edges)
	}
	// a caller cannot reach into the plan
	snap.Modules[4].Dependencies[0] = "changed"
	snap.Order[0] = "changed"
	if again := plan.Snapshot(); again.Modules[4].Dependencies[0] != "a" || again.Order[0] == "changed" {
		t.Fatal("the snapshot must be a copy")
	}
}

func BenchmarkBuild(b *testing.B) {
	mods := []Descriptor{mod("logger"), mod("tracing", "logger"), mod("codec", "logger"),
		mod("database", "logger"), mod("jose", "logger"), mod("cache", "logger", "codec"), mod("http", "logger", "codec", "cache")}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := build(mods...); err != nil {
			b.Fatal(err)
		}
	}
}
