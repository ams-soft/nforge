# static

Demonstrates static files and a single-page application served by the same
nForge HTTP server as the API: `Server.Static`, `Server.SPA` and their
`fs.FS` variants (`StaticFS`, `SPAFS`, for `embed.FS`). No database, cache or
Docker, and no Node/npm: `frontend/dist` is a tiny hand-written build fixture.

```bash
go run ./_examples/static -config _examples/static/config.yaml           # files from the directories
go run ./_examples/static -config _examples/static/config.yaml -embed    # files compiled into the binary
```

Run from the repository root (the directories are relative to the working
directory; `-dist` and `-public` change them), then open
<http://127.0.0.1:8080/>.

| Request | Answer |
|---|---|
| `GET /` | the SPA entry point (`frontend/dist/index.html`) |
| `GET /dashboard`, `/users/42` with `Accept: text/html` | the entry point (a client-side route) |
| `GET /dashboard` with `curl` (`Accept: */*`) | 404: only a browser navigation gets HTML |
| `GET /assets/logo.svg` | `public/logo.svg` (`Static("/assets", ...)`) |
| `GET /build/app.3f9c1e.js` | the hashed bundle, `Cache-Control: public, max-age=31536000, immutable` |
| `GET /api/health` | the API route (`{"success":true,...}`), never the SPA |
| `GET /api/missing`, `GET /build/missing.js`, `GET /.env` | 404, never HTML |

```bash
curl -si http://127.0.0.1:8080/api/health
curl -si -H 'Accept: text/html' http://127.0.0.1:8080/dashboard
curl -si http://127.0.0.1:8080/dashboard                    # 404
curl -si -H 'Accept: text/html' http://127.0.0.1:8080/api/missing   # 404
curl -si -H 'Range: bytes=0-9' http://127.0.0.1:8080/assets/logo.svg
```

## How it is wired

`main.go` registers one application module, `frontend`, whose `Init` does
everything before any listener opens:

```go
nhttp.Get(server.Typed(), "/api/health", health)                  // the API
server.Static("/assets", "./public")                              // public files under a prefix
server.SPA("/", "./frontend/dist",                                // the application, last
    nhttp.WithSPAReserved("/api"),                                // unknown /api/... is a 404
    nhttp.WithSPAImmutableDir("build"))                           // only hashed files are immutable
```

- Registration returns an error: a missing directory, a missing `index.html`
  or overlapping mounts stop `Start` (the tests check it).
- The root SPA is the router's last resort, so it cannot shadow `/api/health`,
  `/health` or any route registered in any order. The built-in routes (docs,
  health, readiness, graph, JWKS) are reserved automatically; your own API
  prefixes are declared with `WithSPAReserved`.
- Everything runs through the normal middleware chain (logging, tracing,
  security headers, CORS) on every transport.
- Directories are opened with `os.OpenRoot`, symbolic links are refused and
  dotfiles, keys and configuration files are never served. See AGENTS.md §7.14.
- `-embed` serves the same files from an `embed.FS` (`fs.Sub` + `StaticFS` /
  `SPAFS`). Embedded files have no modification time, so they get no
  `ETag`/`Last-Modified`; the hashed bundle is still cached for a year.

Frontend visibility is not authorization: the SPA only draws screens, the API
decides what a caller may read.

## Tests

`go test ./_examples/static/...` starts the real application (the checked-in
`config.yaml` with an env file for a free port) in both modes and drives the
server's handler: routes, SPA navigation, 404 rules, cache headers, and the
start failing for a frontend directory without `index.html`.
