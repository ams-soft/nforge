package nhttp

import (
	"bytes"
	"context"
	"log/slog"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func capturedServer(t *testing.T, buf *bytes.Buffer, mutate func(*testConfig)) *Server {
	t.Helper()
	var cfg testConfig
	cfg.App.Name, cfg.App.Version = "t", "1.0.0"
	if mutate != nil {
		mutate(&cfg)
	}
	return buildServer(&testDeps{Config: cfg, Logger: slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))})
}

// Regression (B5): a deadline is a 504 at WARN, never a 500 at ERROR.
func TestHandlerDeadlineIsA504NotA500(t *testing.T) {
	var logs bytes.Buffer
	s := capturedServer(t, &logs, func(c *testConfig) { c.HTTP.Server.HandlerTimeout = 40 * time.Millisecond })
	Get(s.Typed(), "/wait", func(r *stdhttp.Request, _ Empty) (map[string]string, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	rec := serveReq(s, httptest.NewRequest("GET", "/wait", nil))
	if rec.Code != stdhttp.StatusGatewayTimeout {
		t.Fatalf("status %d, want 504: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(logs.String(), "level=ERROR") {
		t.Errorf("a timeout must not log at ERROR:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "level=WARN") {
		t.Errorf("a timeout should log a warning:\n%s", logs.String())
	}
}

// Regression (B5): a client that went away is a 499 and no ERROR.
func TestClientAbortIsA499NotAnError(t *testing.T) {
	var logs bytes.Buffer
	s := capturedServer(t, &logs, nil)
	Get(s.Typed(), "/wait", func(r *stdhttp.Request, _ Empty) (map[string]string, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("GET", "/wait", nil).WithContext(ctx)
	time.AfterFunc(30*time.Millisecond, cancel)
	rec := serveReq(s, req)
	if rec.Code != StatusClientClosedRequest {
		t.Fatalf("status %d body %q, want 499", rec.Code, rec.Body.String())
	}
	if strings.Contains(logs.String(), "level=ERROR") || strings.Contains(logs.String(), "level=WARN") {
		t.Errorf("a client abort must not log above INFO:\n%s", logs.String())
	}
}

// A canceled error with a live request context is a real fault and stays a 500.
func TestUnrelatedCancellationStaysAnError(t *testing.T) {
	var logs bytes.Buffer
	s := capturedServer(t, &logs, nil)
	Get(s.Typed(), "/x", func(r *stdhttp.Request, _ Empty) (map[string]string, error) { return nil, context.Canceled })
	rec := serveReq(s, httptest.NewRequest("GET", "/x", nil))
	if rec.Code != stdhttp.StatusInternalServerError || !strings.Contains(logs.String(), "level=ERROR") {
		t.Fatalf("status %d logs %s", rec.Code, logs.String())
	}
}
