package nhttp

import (
	"encoding/json"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Regression (G11): unmatched paths and methods answer the standard envelope.
func TestNotFoundAndMethodNotAllowedUseTheEnvelope(t *testing.T) {
	s := limitsServer(t, nil)
	s.Router().Get("/only-get", func(w stdhttp.ResponseWriter, r *stdhttp.Request) { w.WriteHeader(204) })

	rec := serveReq(s, httptest.NewRequest("GET", "/nope", nil))
	var env struct {
		Success bool `json:"success"`
		Status  int  `json:"status"`
		Error   struct {
			Title string `json:"title"`
		} `json:"error"`
	}
	if rec.Code != 404 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") || json.Unmarshal(rec.Body.Bytes(), &env) != nil || env.Success || env.Status != 404 || env.Error.Title != "404 ERROR NOT FOUND" {
		t.Fatalf("404: %d %q %q", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}

	rec = serveReq(s, httptest.NewRequest("POST", "/only-get", nil))
	if rec.Code != 405 || rec.Header().Get("Allow") == "" || json.Unmarshal(rec.Body.Bytes(), &env) != nil || env.Status != 405 {
		t.Fatalf("405: %d Allow=%q body=%q", rec.Code, rec.Header().Get("Allow"), rec.Body.String())
	}
}

// Regression (G12): CORS never opens to every origin by accident.
func TestCORSExplicitAnyOriginStillWorks(t *testing.T) {
	_, err := loadTenantConfig(t, "http:\n  server: {port: 8080}\n  cors: {enabled: true, allow_any_origin: true}\n", "")
	if err != nil {
		t.Fatalf("explicit allow_any_origin must load: %v", err)
	}
	_, err = loadTenantConfig(t, "http:\n  server: {port: 8080}\n  cors: {enabled: true, origins: ['*']}\n", "")
	if err != nil {
		t.Fatalf("an explicit '*' must still load: %v", err)
	}
}

// Regression (G10): with shutdown_delay the listener stays open (and answers
// Connection: close) before it closes.
func TestShutdownDelayKeepsServingBeforeClosing(t *testing.T) {
	h := newHarness(t, func(c *ServerConfig) { c.ShutdownDelay = 600 * time.Millisecond })
	h.run(t)
	get(t, &stdhttp.Client{Timeout: 3 * time.Second}, addr("http", h.http, "/ping")).Body.Close()

	stopped := make(chan struct{})
	go func() { _ = h.stop(t); close(stopped) }()
	time.Sleep(150 * time.Millisecond)

	c := &stdhttp.Client{Timeout: 3 * time.Second, Transport: &stdhttp.Transport{DisableKeepAlives: true}}
	resp, err := c.Get(addr("http", h.http, "/ping"))
	if err != nil {
		t.Fatalf("a request during the delay must be served: %v", err)
	}
	if resp.Header.Get("Connection") != "close" && !resp.Close {
		t.Error("answers during the drain should ask the client to close the connection")
	}
	resp.Body.Close()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the server never stopped")
	}
	if !refused(h.http) {
		t.Error("the listener must be closed after the delay")
	}
}

func TestShutdownDelayValidation(t *testing.T) {
	for in, want := range map[string]string{
		"shutdown_delay: -1s": "must be positive",
		"shutdown_delay: 6m":  "at most 5m",
	} {
		if _, err := loadTenantConfig(t, "http:\n  server: {port: 8080, "+in+"}\n", ""); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", in, err, want)
		}
	}
}
