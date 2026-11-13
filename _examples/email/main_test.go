package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/ams-soft/nforge/config"
	"github.com/ams-soft/nforge/email"
	"github.com/ams-soft/nforge/module"
)

func exampleConfig(t *testing.T) *config.Document {
	t.Helper()
	cfg, err := config.LoadFlags("config.yaml", "")
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestExampleBuildsAnApplicationWithoutHTTP(t *testing.T) {
	a, _, err := newApp(exampleConfig(t), quietOption())
	if err != nil {
		t.Fatal(err)
	}
	have := map[module.ID]bool{}
	for _, m := range a.Graph().Modules {
		have[m.ID] = true
	}
	for _, id := range []module.ID{"logger", "email", "welcome"} {
		if !have[id] {
			t.Errorf("module %q missing", id)
		}
	}
	for _, id := range []module.ID{"http", "database", "message"} {
		if have[id] {
			t.Errorf("module %q must not be part of this example", id)
		}
	}
}

func TestMJMLFixtureRenders(t *testing.T) {
	html, err := email.RenderMJML(context.Background(), welcomeMJML)
	if err != nil || !strings.Contains(html, "Welcome to nForge") {
		t.Fatalf("err=%v", err)
	}
}

func TestMissingRecipientSendsNothing(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run(context.Background(), exampleConfig(t), "", &out, &errb); code != 2 || !strings.Contains(errb.String(), "no recipient") {
		t.Fatalf("code=%d stderr=%q", code, errb.String())
	}
}

func TestInvalidRecipientIsReportedWithoutDialing(t *testing.T) {
	var out, errb bytes.Buffer
	code := run(context.Background(), exampleConfig(t), "not an address", &out, &errb)
	if code != 1 || !strings.Contains(errb.String(), "not sent") || strings.Contains(out.String(), "accepted") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errb.String())
	}
}
