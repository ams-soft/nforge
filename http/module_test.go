package nhttp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	stdhttp "net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ams-soft/nforge/app"
	"github.com/ams-soft/nforge/codec"
	"github.com/ams-soft/nforge/config"
	"github.com/ams-soft/nforge/internal/apptest"
	"github.com/ams-soft/nforge/jose"
	"github.com/ams-soft/nforge/logs"
	"github.com/ams-soft/nforge/module"
)

// store is a synthetic resource that fails loudly when used after Stop.
type store struct {
	closed atomic.Bool
	misuse atomic.Int32
}

func (s *store) use() {
	if s.closed.Load() {
		s.misuse.Add(1)
	}
}

var storeCap = module.NewCapability[*store]("store")

func storeModule(s *store) module.Descriptor {
	return module.New("store", module.Definition{Provides: []module.Handle{storeCap}, Init: func(_ context.Context, sc *module.Scope) (module.Hooks, error) {
		module.Provide(sc, storeCap, s)
		return module.Hooks{Stop: func(context.Context) error { s.closed.Store(true); return nil }}, nil
	}})
}

type greeting struct {
	Hello string `json:"hello" codec:"required"`
}

func newHTTPApp(t *testing.T, yaml string, mods ...module.Descriptor) (*app.Application, int, error) {
	t.Helper()
	port := freePort(t)
	env := writeEnv(t, fmt.Sprintf("HTTP_SERVER_HOST=127.0.0.1\nHTTP_SERVER_PORT=%d\n", port))
	doc, err := config.LoadFlags(writeConfig(t, baseYAML+yaml), env)
	if err != nil {
		t.Fatal(err)
	}
	base := []module.Descriptor{logs.Module(), codec.Module(), Module()}
	a, err := app.New(doc, app.Modules(append(base, mods...)...), apptest.Quiet(), app.WithProbeInterval(time.Nanosecond, time.Second))
	return a, port, err
}

func appURL(port int, path string) string { return "http://127.0.0.1:" + strconv.Itoa(port) + path }

// An HTTP-only application: the routes an application module registers in Init
// are served once Start returns, readiness follows the application, and
// Shutdown releases the port.
func TestHTTPOnlyApplication(t *testing.T) {
	var boundDuringInit atomic.Bool
	var port int
	api := module.New("api", module.Definition{Requires: []module.Handle{Capability}, Init: func(_ context.Context, sc *module.Scope) (module.Hooks, error) {
		s := module.Require(sc, Capability)
		// nothing listens while the modules initialize
		if c, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 100*time.Millisecond); err == nil {
			_ = c.Close()
			boundDuringInit.Store(true)
		}
		Get(s.Typed(), "/hello", func(*stdhttp.Request, Empty) (greeting, error) { return greeting{Hello: "world"}, nil })
		return module.Hooks{}, nil
	}})
	a, p, err := newHTTPApp(t, "http: {routes: {readiness: {enabled: true}, health: {enabled: true}}}\n", api)
	if err != nil {
		t.Fatal(err)
	}
	port = p
	if err := a.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if boundDuringInit.Load() {
		t.Fatal("the listener was bound before every module registered its routes")
	}
	resp, err := stdhttp.Get(appURL(port, "/hello"))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"hello":"world"`) {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	resp, err = stdhttp.Get(appURL(port, "/ready"))
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"http":"ready"`) || !strings.Contains(string(body), `"api":"ready"`) {
		t.Fatalf("readiness: %d %s", resp.StatusCode, body)
	}
	if err := a.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !refused(port) {
		t.Fatal("the port is still bound after Shutdown")
	}
}

// A request in flight finishes before the resources its handler uses close:
// HTTP stops first and drains, then the rest stops in reverse order.
func TestShutdownDrainsRequestsBeforeResourcesClose(t *testing.T) {
	st := &store{}
	entered, release := make(chan struct{}), make(chan struct{})
	api := module.New("api", module.Definition{Requires: []module.Handle{Capability, storeCap}, Init: func(_ context.Context, sc *module.Scope) (module.Hooks, error) {
		s, res := module.Require(sc, Capability), module.Require(sc, storeCap)
		s.Router().Get("/slow", func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
			close(entered)
			<-release
			time.Sleep(20 * time.Millisecond)
			res.use()
			_, _ = io.WriteString(w, "done")
		})
		return module.Hooks{Stop: func(context.Context) error { res.use(); return nil }}, nil
	}})
	a, port, err := newHTTPApp(t, "", storeModule(st), api)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := make(chan string, 1)
	go func() {
		resp, err := stdhttp.Get(appURL(port, "/slow"))
		if err != nil {
			got <- err.Error()
			return
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		got <- string(b)
	}()
	<-entered
	done := make(chan error, 1)
	go func() { done <- a.Shutdown(context.Background()) }()
	time.Sleep(20 * time.Millisecond) // the shutdown has begun and waits for the request
	if st.closed.Load() {
		t.Fatal("the store closed while a request was in flight")
	}
	close(release)
	if body := <-got; body != "done" {
		t.Fatalf("the request in flight got %q", body)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if st.misuse.Load() != 0 {
		t.Fatal("the store was closed while a handler still used it")
	}
	if !st.closed.Load() {
		t.Fatal("the store was not closed")
	}
}

// queue is a synthetic job consumer: an ingress module beside HTTP.
type queue struct {
	jobs chan func()
}

var queueCap = module.NewCapability[*queue]("queue")

func queueModule() module.Descriptor {
	return module.New("worker", module.Definition{Provides: []module.Handle{queueCap}, Ingress: true, Init: func(_ context.Context, sc *module.Scope) (module.Hooks, error) {
		q := &queue{jobs: make(chan func(), 4)}
		module.Provide(sc, queueCap, q)
		stop := make(chan struct{})
		var wg sync.WaitGroup
		return module.Hooks{
			Start: func(context.Context) error {
				wg.Go(func() {
					for {
						select {
						case <-stop:
							return
						case job := <-q.jobs:
							job()
						}
					}
				})
				return nil
			},
			Stop: func(context.Context) error { close(stop); wg.Wait(); return nil },
		}, nil
	}})
}

// HTTP and a worker in one application: both drain before the shared domain
// resource closes.
func TestMixedHTTPAndWorkerApplication(t *testing.T) {
	st := &store{}
	jobDone := make(chan struct{})
	domain := module.New("orders", module.Definition{Requires: []module.Handle{Capability, queueCap, storeCap}, Init: func(_ context.Context, sc *module.Scope) (module.Hooks, error) {
		s, q, res := module.Require(sc, Capability), module.Require(sc, queueCap), module.Require(sc, storeCap)
		Post(s.Typed(), "/orders", func(*stdhttp.Request, greeting) (greeting, error) {
			q.jobs <- func() { time.Sleep(20 * time.Millisecond); res.use(); close(jobDone) }
			res.use()
			return greeting{Hello: "queued"}, nil
		}, Opts{Status: stdhttp.StatusAccepted})
		return module.Hooks{}, nil
	}})
	a, port, err := newHTTPApp(t, "", storeModule(st), queueModule(), domain)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	resp, err := stdhttp.Post(appURL(port, "/orders"), "application/json", strings.NewReader(`{"hello":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != stdhttp.StatusAccepted {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if err := a.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-jobDone:
	default:
		t.Fatal("the worker stopped before its job")
	}
	if st.misuse.Load() != 0 || !st.closed.Load() {
		t.Fatalf("misuse %d, closed %v", st.misuse.Load(), st.closed.Load())
	}
}

// An occupied port fails the start and rolls everything back.
func TestPortCollisionFailsTheStart(t *testing.T) {
	st := &store{}
	a, port, err := newHTTPApp(t, "", storeModule(st))
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	err = a.Start(context.Background())
	var le *module.LifecycleError
	if !errors.As(err, &le) || le.Module != "http" || le.Phase != module.PhaseStart {
		t.Fatalf("err = %v", err)
	}
	if !st.closed.Load() {
		t.Fatal("the rollback did not stop the store")
	}
}

// A listener that dies after the start is an application failure.
func TestListenerFailureIsReported(t *testing.T) {
	a, _, err := newHTTPApp(t, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Shutdown(context.Background()) }()
	s, _ := app.Value(a, Capability)
	s.failed(errors.New("nhttp: http: accept: too many open files"))
	select {
	case <-a.Failed():
	case <-time.After(time.Second):
		t.Fatal("the failure did not reach the application")
	}
	if r := a.Status(context.Background()); r.Ready {
		t.Fatal("a failed listener must not report ready")
	}
}

// http.auth.default_profile is checked against the jose module at app.New.
func TestDefaultProfileNeedsJOSE(t *testing.T) {
	if _, _, err := newHTTPApp(t, "http: {auth: {default_profile: access}}\n"); !errors.Is(err, module.ErrMissingDependency) {
		t.Fatalf("without the jose module: %v", err)
	}
	if _, _, err := newHTTPApp(t, "http: {auth: {default_profile: access}}\n", jose.Module()); !errors.Is(err, module.ErrDisabledDependency) {
		t.Fatalf("with jose disabled: %v", err)
	}
}
