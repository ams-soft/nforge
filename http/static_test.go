package nhttp

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"github.com/ams-soft/nforge/module"
)

const (
	indexHTML  = "<!doctype html><title>app</title><div id=root></div>"
	htmlAccept = "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"
)

// site writes a built frontend and a public directory in a temp dir.
func site(t *testing.T) (dist, public string) {
	t.Helper()
	base := t.TempDir()
	dist, public = filepath.Join(base, "dist"), filepath.Join(base, "public")
	files := map[string]string{
		"dist/index.html":            indexHTML,
		"dist/assets/app.abc123.js":  "console.log(1)",
		"dist/assets/app.abc123.css": "body{}",
		"dist/favicon.ico":           "ico",
		"dist/sw.js":                 "self",
		"dist/.env":                  "SECRET=1",
		"dist/.git/config":           "[core]",
		"dist/server.key":            "KEY",
		"dist/config.yaml":           "db: x",
		"dist/deep/dir/file.txt":     "deep",
		"public/logo.svg":            "<svg xmlns='http://www.w3.org/2000/svg'/>",
		"public/data.bin":            "0123456789",
		"public/.secret":             "no",
	}
	for name, body := range files {
		p := filepath.Join(base, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dist, public
}

func newFilesServer(t *testing.T) *Server {
	t.Helper()
	return New(Config{}, Dependencies{App: module.App{Name: "t", Version: "1"}})
}

func fileReq(s *Server, method, target string, hdr ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	s.Srv.Handler.ServeHTTP(rec, req)
	return rec
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestStaticServesFilesWithBoundaries(t *testing.T) {
	_, public := site(t)
	s := newFilesServer(t)
	must(t, s.Static("/assets", public))

	rec := fileReq(s, "GET", "/assets/logo.svg")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "<svg") {
		t.Fatalf("GET logo: %d %q", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "image/svg+xml") {
		t.Fatalf("content type %q", got)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" || rec.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("headers %v", rec.Header())
	}
	head := fileReq(s, "HEAD", "/assets/logo.svg")
	if head.Code != 200 || head.Body.Len() != 0 || head.Header().Get("Content-Length") == "" {
		t.Fatalf("HEAD: %d len=%d hdr=%v", head.Code, head.Body.Len(), head.Header())
	}
	for _, target := range []string{"/assetsx/logo.svg", "/assetsx", "/asset/logo.svg", "/assets", "/assets/", "/assets/missing.js", "/assets/logo.svg/"} {
		if rec := fileReq(s, "GET", target); rec.Code != 404 {
			t.Errorf("GET %s = %d, want 404", target, rec.Code)
		}
	}
	if rec := fileReq(s, "POST", "/assets/logo.svg"); rec.Code != 405 || !strings.Contains(rec.Header().Get("Allow"), "GET") {
		t.Errorf("POST = %d allow=%q", rec.Code, rec.Header().Get("Allow"))
	}
}

func TestStaticRefusesUnsafePaths(t *testing.T) {
	_, public := site(t)
	outside := filepath.Join(filepath.Dir(public), "outside.txt")
	must(t, os.WriteFile(outside, []byte("outside"), 0o600))
	s := newFilesServer(t)
	must(t, s.Static("/assets", public))
	for _, target := range []string{
		"/assets/../outside.txt", "/assets/%2e%2e/outside.txt", "/assets/%2E%2E%2Foutside.txt",
		"/assets/..%2foutside.txt", "/assets/.%2e/outside.txt", "/assets//logo.svg", "/assets/./logo.svg",
		"/assets/%00logo.svg", "/assets/logo.svg%00", "/assets/..%5coutside.txt", "/assets/%5clogo.svg",
		"/assets/.secret", "/assets/%2esecret", "/assets/logo.svg.", "/assets/logo.svg%20",
		"/assets/%ff", "/assets/" + strings.Repeat("a", 3000),
	} {
		rec := fileReq(s, "GET", target)
		if rec.Code == 200 || rec.Body.String() == "outside" {
			t.Errorf("GET %s = %d %q", target, rec.Code, rec.Body)
		}
	}
}

func TestStaticSymlinks(t *testing.T) {
	dist, public := site(t)
	// a link to a file outside, a link to a dotfile inside, a link to a directory
	must(t, os.WriteFile(filepath.Join(filepath.Dir(public), "outside.txt"), []byte("outside"), 0o600))
	for name, target := range map[string]string{
		"out.txt":   filepath.Join(filepath.Dir(public), "outside.txt"),
		"dot.txt":   filepath.Join(public, ".secret"),
		"linkdir":   dist,
		"logo2.svg": filepath.Join(public, "logo.svg"),
	} {
		if err := os.Symlink(target, filepath.Join(public, name)); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	}
	s := newFilesServer(t)
	must(t, s.Static("/assets", public))
	for _, target := range []string{"/assets/out.txt", "/assets/dot.txt", "/assets/linkdir/index.html", "/assets/logo2.svg"} {
		if rec := fileReq(s, "GET", target); rec.Code != 404 {
			t.Errorf("GET %s = %d %q, want 404", target, rec.Code, rec.Body)
		}
	}
	if rec := fileReq(s, "GET", "/assets/logo.svg"); rec.Code != 200 {
		t.Errorf("regular file = %d", rec.Code)
	}
}

func TestStaticDeniesSensitiveFiles(t *testing.T) {
	dist, _ := site(t)
	s := newFilesServer(t)
	must(t, s.Static("/s", dist))
	for _, target := range []string{"/s/.env", "/s/.git/config", "/s/server.key", "/s/config.yaml", "/s/assets"} {
		if rec := fileReq(s, "GET", target); rec.Code != 404 {
			t.Errorf("GET %s = %d", target, rec.Code)
		}
	}
	if rec := fileReq(s, "GET", "/s/deep/dir/file.txt"); rec.Code != 200 || rec.Body.String() != "deep" {
		t.Errorf("nested file = %d %q", rec.Code, rec.Body)
	}
	if rec := fileReq(s, "GET", "/s/deep/dir/"); rec.Code != 404 { // no listing, no index
		t.Errorf("directory = %d", rec.Code)
	}
}

func TestStaticConditionalAndRange(t *testing.T) {
	_, public := site(t)
	s := newFilesServer(t)
	must(t, s.Static("/assets", public))
	first := fileReq(s, "GET", "/assets/data.bin")
	etag, lm := first.Header().Get("ETag"), first.Header().Get("Last-Modified")
	if !strings.HasPrefix(etag, `W/"`) || lm == "" {
		t.Fatalf("validators missing: etag=%q last-modified=%q", etag, lm)
	}
	if rec := fileReq(s, "GET", "/assets/data.bin", "If-None-Match", etag); rec.Code != 304 || rec.Body.Len() != 0 {
		t.Errorf("If-None-Match = %d", rec.Code)
	}
	if rec := fileReq(s, "GET", "/assets/data.bin", "If-Modified-Since", lm); rec.Code != 304 {
		t.Errorf("If-Modified-Since = %d", rec.Code)
	}
	rec := fileReq(s, "GET", "/assets/data.bin", "Range", "bytes=2-4")
	if rec.Code != 206 || rec.Body.String() != "234" || rec.Header().Get("Content-Range") != "bytes 2-4/10" {
		t.Errorf("range = %d %q %v", rec.Code, rec.Body, rec.Header())
	}
	if rec := fileReq(s, "GET", "/assets/data.bin", "Range", "bytes=50-60"); rec.Code != 416 {
		t.Errorf("unsatisfiable range = %d", rec.Code)
	}
	if rec := fileReq(s, "GET", "/assets/data.bin"); rec.Header().Get("Content-Type") != "application/octet-stream" {
		t.Errorf("unknown type = %q", rec.Header().Get("Content-Type"))
	}
}

func TestStaticCacheControlOption(t *testing.T) {
	_, public := site(t)
	s := newFilesServer(t)
	must(t, s.Static("/assets", public, WithStaticCacheControl("public, max-age=60")))
	if got := fileReq(s, "GET", "/assets/logo.svg").Header().Get("Cache-Control"); got != "public, max-age=60" {
		t.Errorf("Cache-Control = %q", got)
	}
	if rec := fileReq(s, "GET", "/assets/missing.svg"); rec.Header().Get("Cache-Control") != "" {
		t.Errorf("a 404 must not carry the asset policy: %q", rec.Header().Get("Cache-Control"))
	}
}

func TestSPANavigation(t *testing.T) {
	dist, _ := site(t)
	s := newFilesServer(t)
	nh := func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "api") }
	s.Router().Get("/api/health", nh)
	s.Router().Post("/api/users", nh)
	must(t, s.SPA("/", dist, WithSPAReserved("/api")))

	check := func(method, target string, want int, contains string, hdr ...string) {
		t.Helper()
		rec := fileReq(s, method, target, hdr...)
		if rec.Code != want || !strings.Contains(rec.Body.String(), contains) {
			t.Errorf("%s %s = %d %q, want %d containing %q", method, target, rec.Code, rec.Body, want, contains)
		}
	}
	html := []string{"Accept", htmlAccept}
	check("GET", "/", 200, "id=root") // the root needs no Accept
	check("GET", "/", 200, "id=root", html...)
	check("GET", "/dashboard", 200, "id=root", html...) // client-side route
	check("GET", "/users/42/edit/", 200, "id=root", html...)
	check("HEAD", "/dashboard", 200, "", html...)
	check("GET", "/index.html", 200, "id=root")
	check("GET", "/assets/app.abc123.js", 200, "console.log")
	check("GET", "/deep/dir/file.txt", 200, "deep")
	check("GET", "/api/health", 200, "api") // explicit routes win
	check("GET", "/dashboard", 404, "")     // curl: Accept */*
	check("GET", "/dashboard", 404, "", "Accept", "*/*")
	check("GET", "/dashboard", 404, "", "Accept", "text/html;q=0")
	check("GET", "/dashboard", 200, "id=root", "Accept", "application/json, text/html;q=0.5")
	check("GET", "/missing.js", 404, "", html...) // a file extension never falls back
	check("GET", "/assets/missing.css", 404, "", html...)
	check("GET", "/api/missing", 404, "", html...) // reserved
	check("GET", "/api", 404, "", html...)
	check("GET", "/api/users", 405, "", html...) // another method serves it: the router's 405
	check("GET", "/.env", 404, "", html...)
	check("GET", "/.well-known/x", 404, "", html...)
	check("GET", "/server.key", 404, "", html...)
	check("GET", "/a/../../etc/passwd", 404, "", html...)
	check("POST", "/dashboard", 404, "", html...) // never HTML for other methods
	check("DELETE", "/", 404, "")
	check("GET", "/apix", 200, "id=root", html...) // the reservation has a segment boundary
	if rec := fileReq(s, "GET", "/index.html"); rec.Header().Get("Cache-Control") != "no-cache" || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
		t.Errorf("index headers %v", rec.Header())
	}
}

func TestSPAReservesBuiltins(t *testing.T) {
	dist, _ := site(t)
	cfg := Config{}
	cfg.Docs.Enabled = true
	cfg.Routes.Health.Enabled = true
	cfg.Routes.Readiness.Enabled = true
	s := New(cfg, Dependencies{App: module.App{Name: "t", Version: "1"}})
	must(t, s.SPA("/", dist))
	html := []string{"Accept", htmlAccept}
	if rec := fileReq(s, "GET", "/health", html...); rec.Code != 200 || strings.Contains(rec.Body.String(), "id=root") {
		t.Errorf("health = %d %q", rec.Code, rec.Body)
	}
	for _, target := range []string{"/docs/nothing", "/health/x", "/ready/x"} {
		if rec := fileReq(s, "GET", target, html...); rec.Code != 404 {
			t.Errorf("GET %s = %d, want 404", target, rec.Code)
		}
	}
}

func TestSPAPrefixMount(t *testing.T) {
	dist, _ := site(t)
	s := newFilesServer(t)
	must(t, s.SPA("/app", dist))
	html := []string{"Accept", htmlAccept}
	for target, want := range map[string]int{
		"/app": 200, "/app/": 200, "/app/dashboard": 200, "/app/assets/app.abc123.js": 200,
		"/app/missing.js": 404, "/apps": 404, "/appx/dashboard": 404, "/dashboard": 404,
	} {
		if rec := fileReq(s, "GET", target, html...); rec.Code != want {
			t.Errorf("GET %s = %d, want %d", target, rec.Code, want)
		}
	}
	if rec := fileReq(s, "POST", "/app/x", html...); rec.Code != 405 {
		t.Errorf("POST = %d", rec.Code)
	}
}

func TestSPACachePolicies(t *testing.T) {
	dist, _ := site(t)
	s := newFilesServer(t)
	must(t, s.SPA("/", dist, WithSPAImmutableDir("assets"), WithSPAHTMLCacheControl("no-store"), WithSPAAssetCacheControl("public, max-age=300")))
	for target, want := range map[string]string{
		"/":                     "no-store",
		"/index.html":           "no-store",
		"/assets/app.abc123.js": ImmutableCacheControl,
		"/sw.js":                "public, max-age=300", // unhashed: never immutable
		"/favicon.ico":          "public, max-age=300",
	} {
		if got := fileReq(s, "GET", target).Header().Get("Cache-Control"); got != want {
			t.Errorf("%s Cache-Control = %q, want %q", target, got, want)
		}
	}
	if rec := fileReq(s, "GET", "/assets/nope.js"); rec.Header().Get("Cache-Control") != "" {
		t.Errorf("404 Cache-Control = %q", rec.Header().Get("Cache-Control"))
	}
}

func TestSPAFSEmbedStyle(t *testing.T) {
	bundle := fstest.MapFS{
		"index.html":       {Data: []byte(indexHTML)},
		"assets/a.js":      {Data: []byte("js")},
		".hidden":          {Data: []byte("x")},
		"custom/main.html": {Data: []byte("custom")},
	}
	s := newFilesServer(t)
	must(t, s.SPAFS("/", bundle, WithSPAIndex("custom/main.html")))
	html := []string{"Accept", htmlAccept}
	if rec := fileReq(s, "GET", "/x", html...); rec.Code != 200 || rec.Body.String() != "custom" {
		t.Errorf("custom index: %d %q", rec.Code, rec.Body)
	}
	if rec := fileReq(s, "GET", "/assets/a.js"); rec.Code != 200 || rec.Header().Get("ETag") != "" {
		t.Errorf("zero mod time must give no validators: %d %v", rec.Code, rec.Header())
	}
	if rec := fileReq(s, "GET", "/.hidden", html...); rec.Code != 404 {
		t.Errorf("dotfile = %d", rec.Code)
	}
}

// onlyReader hides Seek, like the files of some archive filesystems.
type onlyReader struct{ fs.File }

func (o onlyReader) Read(b []byte) (int, error) { return o.File.Read(b) }

type noSeekFS struct{ fs.FS }

func (n noSeekFS) Open(name string) (fs.File, error) {
	f, err := n.FS.Open(name)
	if err != nil {
		return nil, err
	}
	return onlyReader{f}, nil
}

func TestStaticFSWithoutSeek(t *testing.T) {
	s := newFilesServer(t)
	must(t, s.StaticFS("/f", noSeekFS{fstest.MapFS{"a.txt": {Data: []byte("0123456789")}}}))
	rec := fileReq(s, "GET", "/f/a.txt", "Range", "bytes=1-3")
	if rec.Code != 206 || rec.Body.String() != "123" {
		t.Errorf("range over a non-seekable file = %d %q", rec.Code, rec.Body)
	}
}

func TestMissingFilesAfterStartup(t *testing.T) {
	dist, public := site(t)
	s := newFilesServer(t)
	must(t, s.Static("/assets", public))
	must(t, s.SPA("/", dist))
	must(t, os.Remove(filepath.Join(public, "logo.svg")))
	if rec := fileReq(s, "GET", "/assets/logo.svg"); rec.Code != 404 {
		t.Errorf("removed file = %d", rec.Code)
	}
	must(t, os.Remove(filepath.Join(dist, "index.html")))
	rec := fileReq(s, "GET", "/dashboard", "Accept", htmlAccept)
	if rec.Code != 500 || strings.Contains(rec.Body.String(), dist) || strings.Contains(rec.Body.String(), "id=root") {
		t.Errorf("missing entry point = %d %q (must be a generic 500, no path, no HTML)", rec.Code, rec.Body)
	}
}

func TestRegistrationErrors(t *testing.T) {
	dist, public := site(t)
	file := filepath.Join(dist, "index.html")
	emptyDir := t.TempDir()
	cases := map[string]func(s *Server) error{
		"empty prefix":         func(s *Server) error { return s.Static("", public) },
		"relative prefix":      func(s *Server) error { return s.Static("assets", public) },
		"trailing slash":       func(s *Server) error { return s.Static("/assets/", public) },
		"dot segment":          func(s *Server) error { return s.Static("/a/../b", public) },
		"wildcard":             func(s *Server) error { return s.Static("/a/*", public) },
		"param":                func(s *Server) error { return s.Static("/{id}", public) },
		"empty directory":      func(s *Server) error { return s.Static("/a", "") },
		"missing directory":    func(s *Server) error { return s.Static("/a", filepath.Join(public, "nope")) },
		"directory is a file":  func(s *Server) error { return s.Static("/a", file) },
		"nil fs":               func(s *Server) error { return s.StaticFS("/a", nil) },
		"nil option":           func(s *Server) error { return s.Static("/a", public, nil) },
		"bad cache control":    func(s *Server) error { return s.Static("/a", public, WithStaticCacheControl("a\r\nSet-Cookie: x")) },
		"empty cache control":  func(s *Server) error { return s.Static("/a", public, WithStaticCacheControl("")) },
		"immutable html":       func(s *Server) error { return s.SPA("/", dist, WithSPAHTMLCacheControl("public, immutable")) },
		"bad index name":       func(s *Server) error { return s.SPA("/", dist, WithSPAIndex("../x.html")) },
		"hidden index":         func(s *Server) error { return s.SPA("/", dist, WithSPAIndex(".env")) },
		"bad reserved":         func(s *Server) error { return s.SPA("/", dist, WithSPAReserved("api")) },
		"reserved root":        func(s *Server) error { return s.SPA("/", dist, WithSPAReserved("/")) },
		"bad immutable dir":    func(s *Server) error { return s.SPA("/", dist, WithSPAImmutableDir("../x")) },
		"no index":             func(s *Server) error { return s.SPA("/", emptyDir) },
		"index is a directory": func(s *Server) error { return s.SPA("/", dist, WithSPAIndex("assets")) },
		"nil spa fs":           func(s *Server) error { return s.SPAFS("/", nil) },
		"spa fs without index": func(s *Server) error { return s.SPAFS("/", fstest.MapFS{"a.js": {}}) },
		"two roots": func(s *Server) error {
			if err := s.SPA("/", dist); err != nil {
				return err
			}
			return s.Static("/", public)
		},
		"same prefix": func(s *Server) error {
			if err := s.Static("/a", public); err != nil {
				return err
			}
			return s.Static("/a", public)
		},
		"nested prefix": func(s *Server) error {
			if err := s.Static("/a", public); err != nil {
				return err
			}
			return s.Static("/a/b", public)
		},
		"parent prefix": func(s *Server) error {
			if err := s.Static("/a/b", public); err != nil {
				return err
			}
			return s.SPA("/a", dist)
		},
		"existing route": func(s *Server) error {
			s.Router().Get("/a/x", func(http.ResponseWriter, *http.Request) {})
			return s.Static("/a", public)
		},
		"existing param route": func(s *Server) error {
			s.Router().Get("/a/{id}", func(http.ResponseWriter, *http.Request) {})
			return s.Static("/a", public)
		},
		"existing catch-all above": func(s *Server) error {
			s.Router().Get("/a/*", func(http.ResponseWriter, *http.Request) {})
			return s.Static("/a/b", public)
		},
	}
	for name, fn := range cases {
		t.Run(name, func(t *testing.T) {
			if err := fn(newFilesServer(t)); err == nil {
				t.Fatal("want an error")
			} else if strings.Contains(err.Error(), "panic") {
				t.Fatalf("a panic leaked: %v", err)
			}
		})
	}
	// the failures above must not leave a half-registered mount behind
	s := newFilesServer(t)
	_ = s.Static("/a", public, WithStaticCacheControl(""))
	must(t, s.Static("/a", public))
}

func TestRootMountsCoexistWithRoutes(t *testing.T) {
	dist, public := site(t)
	s := newFilesServer(t)
	s.Router().Get("/assets/dynamic", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "dyn") })
	must(t, s.SPA("/", dist)) // the root fallback is not a conflict
	must(t, s.Static("/pub", public))
	if rec := fileReq(s, "GET", "/assets/dynamic"); rec.Body.String() != "dyn" {
		t.Errorf("route = %q", rec.Body)
	}
	if rec := fileReq(s, "GET", "/pub/logo.svg"); rec.Code != 200 {
		t.Errorf("static = %d", rec.Code)
	}
}

func TestStartRejectsLateConflictsAndStopClosesRoots(t *testing.T) {
	_, public := site(t)
	cfg := Config{}
	cfg.Server.Port = 18999
	cfg.Server.Host = "127.0.0.1"
	s := New(cfg, Dependencies{App: module.App{Name: "t", Version: "1"}})
	must(t, s.Static("/assets", public))
	s.Router().Get("/assets/late", func(http.ResponseWriter, *http.Request) {}) // registered after the mount
	if err := s.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "conflicts with the route /assets/late") {
		t.Fatalf("Start = %v, want the conflict", err)
	}
	if len(s.files.roots) != 1 {
		t.Fatal("the root handle must be kept until Stop")
	}
	must(t, s.Stop(context.Background()))
	if len(s.files.roots) != 0 {
		t.Fatal("Stop must release the directory handles")
	}
}

func TestRegistrationAfterServingIsAnError(t *testing.T) {
	dist, public := site(t)
	s := newFilesServer(t)
	_ = fileReq(s, "GET", "/") // the first request locks the router
	if err := s.Static("/a", public); err == nil {
		t.Error("Static after serving must fail")
	}
	if err := s.SPA("/", dist); err == nil {
		t.Error("SPA after serving must fail")
	}
}

func TestMiddlewareAndMultipleServers(t *testing.T) {
	dist, public := site(t)
	var hits atomic.Int32
	mk := func() *Server {
		s := newFilesServer(t)
		s.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.Header().Set("X-Test", "mw")
				next.ServeHTTP(w, r)
			})
		})
		must(t, s.Static("/assets", public))
		must(t, s.SPA("/", dist))
		return s
	}
	a, b := mk(), mk() // the same prefixes on two applications share nothing
	for _, s := range []*Server{a, b} {
		for _, target := range []string{"/assets/logo.svg", "/assets/nope.js", "/dashboard", "/"} {
			if rec := fileReq(s, "GET", target, "Accept", htmlAccept); rec.Header().Get("X-Test") != "mw" {
				t.Errorf("GET %s skipped the global middleware", target)
			}
		}
	}
	if hits.Load() != 8 {
		t.Errorf("middleware ran %d times", hits.Load())
	}
}

func TestSecurityHeadersApplyToFiles(t *testing.T) {
	dist, _ := site(t)
	cfg := Config{}
	s := New(cfg, Dependencies{App: module.App{Name: "t", Version: "1"}})
	must(t, s.SPA("/", dist))
	rec := fileReq(s, "GET", "/")
	if rec.Header().Get("Content-Security-Policy") == "" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("security headers missing: %v", rec.Header())
	}
}

func TestAcceptsHTML(t *testing.T) {
	for in, want := range map[string]bool{
		"text/html": true, "TEXT/HTML;q=0.1": true, "text/html;q=0": false, "text/html; q=0.0": false,
		"*/*": false, "text/*": false, "application/json": false, "": false,
		"application/json, text/html": true, "text/htmlx": false,
	} {
		if got := acceptsHTML([]string{in}); got != want {
			t.Errorf("acceptsHTML(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestStaticBodyIsExact(t *testing.T) {
	_, public := site(t)
	s := newFilesServer(t)
	must(t, s.Static("/assets", public))
	rec := fileReq(s, "GET", "/assets/data.bin")
	if !bytes.Equal(rec.Body.Bytes(), []byte("0123456789")) || rec.Header().Get("Content-Length") != "10" {
		t.Errorf("body %q len %q", rec.Body, rec.Header().Get("Content-Length"))
	}
}
