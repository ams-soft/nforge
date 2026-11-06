package deps

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/ams-soft/nforge/cache"
	"github.com/ams-soft/nforge/config"
	"github.com/ams-soft/nforge/jose"
	"github.com/ams-soft/nforge/module"
)

// recordLifecycle wraps the real modules, so the order is observed on what runs
// and not read from the plan.
func recordLifecycle(mods []Module, started, stopped *[]module.ID) []Module {
	out := slices.Clone(mods)
	for i, m := range out {
		init := m.Init
		if init == nil {
			continue
		}
		out[i].Init = func(ctx context.Context, cfg config.Config, s *module.Scope) (module.Hooks, error) {
			hooks, err := init(ctx, cfg, s)
			if err != nil {
				return hooks, err
			}
			*started = append(*started, m.ID)
			if stop := hooks.Shutdown; stop != nil {
				hooks.Shutdown = func(ctx context.Context) error {
					*stopped = append(*stopped, m.ID)
					return stop(ctx)
				}
			}
			return hooks, nil
		}
	}
	return out
}

func orderConfig() config.Config {
	cfg := minimalConfig()
	cfg.Logging.Output.Terminal.Enabled = false
	cfg.Logging.Output.Otel.Enabled = true
	cfg.Logging.Output.Otel.Endpoint, cfg.Logging.Output.Otel.Insecure = "127.0.0.1:1", true
	cfg.Tracing.Otel.Endpoint, cfg.Tracing.Otel.Insecure = "127.0.0.1:1", true
	cfg.JOSE = jose.Config{Enabled: true, Keys: []jose.KeyConfig{{KID: "k", Secret: "0123456789abcdef0123456789abcdef"}},
		Profiles: []jose.ProfileConfig{{Name: "p", Kind: jose.KindJWS, Typ: "msg+jws", SigningKey: "k"}}}
	jose.ApplyDefaults(&cfg.JOSE)
	cfg.Cache = cache.Config{Enabled: true, Driver: cache.DriverMemory}
	cache.ApplyDefaults(&cfg.Cache)
	return cfg
}

// runOrder starts the real modules of cfg, stops them and returns both sequences.
func runOrder(t *testing.T, cfg config.Config) (started, stopped []module.ID) {
	t.Helper()
	plan, err := module.Resolve(cfg, recordLifecycle(builtinModules(false), &started, &stopped))
	if err != nil {
		t.Fatal(err)
	}
	rt, err := plan.Start(context.Background(), module.StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// the collector does not exist: the exporters fail, the order must hold anyway
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_ = rt.Shutdown(ctx)
	return started, stopped
}

func TestRealModulesStartAndStopInOrder(t *testing.T) {
	started, stopped := runOrder(t, orderConfig())
	if want := []module.ID{"logger", "tracing", "startup-report", "codec", "jose", "cache"}; !slices.Equal(started, want) {
		t.Errorf("started = %v, want %v", started, want)
	}
	// logger and tracing stop last, so they still see the others shut down
	if want := []module.ID{"cache", "jose", "tracing", "logger"}; !slices.Equal(stopped, want) {
		t.Errorf("stopped = %v, want %v", stopped, want)
	}
}

// A function added after the bootstrap runs before the modules stop.
func TestAddShutdownRunsBeforeTheModules(t *testing.T) {
	cfg := minimalConfig()
	cfg.Cache = cache.Config{Enabled: true, Driver: cache.DriverMemory}
	cache.ApplyDefaults(&cfg.Cache)
	d, err := NewDepsE(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var pingErr error
	d.AddShutdown(func(ctx context.Context) error {
		pingErr = d.PingCache(ctx)
		return nil
	})
	if err := d.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if pingErr != nil {
		t.Fatalf("the cache was already closed: %v", pingErr)
	}
}
