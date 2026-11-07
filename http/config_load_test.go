package nhttp

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ams-soft/nforge/app"
	"github.com/ams-soft/nforge/cache"
	"github.com/ams-soft/nforge/codec"
	"github.com/ams-soft/nforge/config"
	"github.com/ams-soft/nforge/database"
	"github.com/ams-soft/nforge/internal/apptest"
	"github.com/ams-soft/nforge/jose"
	"github.com/ams-soft/nforge/logs"
	"github.com/ams-soft/nforge/module"
	"github.com/ams-soft/nforge/telemetry"
)

const baseYAML = "app: {name: a, version: '1'}\n"

func writeConfig(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeEnv(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.env")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// loadedConfig is what an application of every built-in module validated.
type loadedConfig struct {
	testConfig
	Database database.Config
	Cache    cache.Config
	Warnings []string
}

// allModules registers every built-in module.
func allModules() []module.Descriptor {
	return []module.Descriptor{logs.Module(), telemetry.Module(), codec.Module(), database.Module(), jose.Module(), cache.Module(), Module()}
}

// LoadFlags loads a configuration and validates it with every built-in module,
// as an application would at app.New, returning the effective sections.
func LoadFlags(configPath, envPath string) (*loadedConfig, error) {
	doc, err := config.LoadFlags(configPath, envPath)
	if err != nil {
		return nil, err
	}
	out := &loadedConfig{}
	out.Env, out.App.Name, out.App.Version, out.App.InternalUse = doc.Env(), doc.App().Name, doc.App().Version, doc.App().InternalUse
	reader := module.New("reader", module.Definition{
		Optional: []module.Handle{Capability, jose.Capability, codec.Capability, database.Capability, cache.Capability, telemetry.Capability, module.Logger},
		Check: func(v *module.View) error {
			out.HTTP, _, _, _ = module.ConfigOf[Config](v, Capability)
			out.JOSE, _, _, _ = module.ConfigOf[jose.Config](v, jose.Capability)
			out.Codec, _, _, _ = module.ConfigOf[codec.Settings](v, codec.Capability)
			out.Database, _, _, _ = module.ConfigOf[database.Config](v, database.Capability)
			out.Cache, _, _, _ = module.ConfigOf[cache.Config](v, cache.Capability)
			out.Tracing, _, _, _ = module.ConfigOf[telemetry.Settings](v, telemetry.Capability)
			out.Logging, _, _, _ = module.ConfigOf[logs.Settings](v, module.Logger)
			return nil
		},
	})
	a, err := app.New(doc, app.Modules(append(allModules(), reader)...), apptest.Quiet())
	if err != nil {
		return nil, err
	}
	out.Warnings = a.Warnings()
	return out, nil
}
