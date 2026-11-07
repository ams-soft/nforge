// Package apptest builds applications from inline YAML for the tests of the
// nForge modules. It is test support only.
package apptest

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/ams-soft/nforge/app"
	"github.com/ams-soft/nforge/config"
	"github.com/ams-soft/nforge/module"
)

// Header is the minimal valid configuration.
const Header = "app: {name: test-app, version: 1.0.0}\n"

// Load writes yaml (after Header, unless it sets app itself) to a temporary
// config.yaml and loads it, with envFile as the env file when not empty.
func Load(t testing.TB, yaml, envFile string) *config.Document {
	t.Helper()
	d, err := LoadE(t, yaml, envFile)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// LoadE is Load returning the error.
func LoadE(t testing.TB, yaml, envFile string) (*config.Document, error) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(Header+yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	envPath := ""
	if envFile != "" {
		envPath = filepath.Join(dir, "test.env")
		if err := os.WriteFile(envPath, []byte(envFile), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return config.LoadFlags(path, envPath)
}

// Quiet is the bootstrap logger of test applications.
func Quiet() app.Option { return app.WithLogger(slog.New(slog.DiscardHandler)) }

// New builds an application of mods on yaml.
func New(t testing.TB, yaml string, mods ...module.Descriptor) (*app.Application, error) {
	t.Helper()
	d, err := LoadE(t, yaml, "")
	if err != nil {
		return nil, err
	}
	return app.New(d, app.Modules(mods...), Quiet())
}

// Start builds and starts an application of mods on yaml, and shuts it down
// when the test ends.
func Start(t testing.TB, yaml string, mods ...module.Descriptor) *app.Application {
	t.Helper()
	a, err := New(t, yaml, mods...)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Shutdown(context.Background()) })
	return a
}
