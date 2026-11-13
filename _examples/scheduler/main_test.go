package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ams-soft/nforge/app"
	"github.com/ams-soft/nforge/config"
	"github.com/ams-soft/nforge/module"
	"github.com/ams-soft/nforge/scheduler"
)

func load(t *testing.T, yamlText string) *config.Document {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(yamlText), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFlags(path, "")
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestExampleConfigBuildsASchedulerOnlyApplication(t *testing.T) {
	cfg, err := config.LoadFlags("config.yaml", "")
	if err != nil {
		t.Fatal(err)
	}
	a, err := newApp(cfg, dailyAtSeven)
	if err != nil {
		t.Fatal(err)
	}
	have := map[module.ID]bool{}
	for _, m := range a.Graph().Modules {
		have[m.ID] = true
	}
	for _, id := range []module.ID{"logger", "database", "scheduler", "stock"} {
		if !have[id] {
			t.Errorf("module %q missing from the graph", id)
		}
	}
	for _, id := range []module.ID{"http", "message", "sse"} {
		if have[id] {
			t.Errorf("module %q must not be part of a scheduler-only application", id)
		}
	}
}

func TestWithoutDatabaseTheApplicationDoesNotBuild(t *testing.T) {
	cfg := load(t, "app: {name: x, version: 1.0.0}\ndatabase: {enabled: false}\n")
	if _, err := newApp(cfg, dailyAtSeven); !errors.Is(err, module.ErrDisabledDependency) {
		t.Fatalf("err = %v, want ErrDisabledDependency", err)
	}
}

func TestInvalidSchedulerConfigurationFailsBeforeAnyIO(t *testing.T) {
	for name, section := range map[string]string{
		"distributed": "mode: distributed",
		"timezone":    "timezone: Mars/Olympus",
		"negative":    "max_concurrency: -1",
	} {
		cfg := load(t, "app: {name: x, version: 1.0.0}\ndatabase: {enabled: true, host: 127.0.0.1, port: 1, name: x, user: x}\nscheduler: {"+section+"}\n")
		_, err := newApp(cfg, dailyAtSeven)
		if err == nil {
			t.Errorf("%s: app.New succeeded", name)
		}
		if name == "distributed" && !errors.Is(err, scheduler.ErrUnsupportedMode) {
			t.Errorf("distributed: %v", err)
		}
	}
}

// Without a reachable database the start fails with a clear error and nothing
// stays running.
func TestUnreachableDatabaseFailsTheStart(t *testing.T) {
	cfg := load(t, "app: {name: x, version: 1.0.0}\ndatabase: {enabled: true, host: 127.0.0.1, port: 1, name: x, user: x, ssl: {mode: disable}, connection: {connect_timeout: 1s}}\n")
	a, err := newApp(cfg, dailyAtSeven, app.WithLogger(quiet()))
	if err != nil {
		t.Fatal(err)
	}
	err = a.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "database") {
		t.Fatalf("Start = %v, want a database error", err)
	}
}
