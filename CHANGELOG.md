# Changelog

All notable changes to nForge are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **HTTPS and HTTP/3 transports**, opt-in through `http.server.tls` and
  `http.server.http3`. HTTPS serves over TCP with TLS 1.3 as the minimum
  (`min_version: "1.2"` is the only alternative); HTTP/3 serves over
  QUIC/UDP (`github.com/quic-go/quic-go` v0.64.1). Plain HTTP, HTTPS and
  HTTP/3 share one router and middleware chain. `tls.disable_insecure_http`
  closes the plain listener; `tls.redirect_http` turns it into a
  redirect-only 308 listener to a configured host (`tls.redirect_host`, or a
  loopback `http.server.host`; the request `Host` is never used).
  HTTP/3 reuses the TLS certificate (`same_keycert_tls`, default) or its own
  pair, and works without HTTPS. HTTPS responses advertise `Alt-Svc` while
  QUIC is bound (`http3.advertise`). `http3.max_idle_timeout` and
  `http3.max_stream_timeout` are enforced; `allow_0rtt: true` is rejected.
- `ServerHttp.StartContext(ctx)`, the context-driven form of `Start`.
- `config.ValidateServer`, `ServerConfig.RedirectAuthority` and the
  `DefaultHTTPPort`/`DefaultTLSPort`/`DefaultHTTP3Port` constants.
- `_examples/basic/config.https-http3.yaml`, a committed **local-development
  only** certificate (`_examples/basic/certs/`, its key is public on purpose),
  and a live test that talks real HTTPS, HTTP/3 and the redirect.

### Changed

- **`http.server.port` is no longer required**: omitted or `0` means `8080`
  (`tls.port` and `http3.port` default to `8443`, independently). A config
  without an `http.server` section now loads.
- `Start` now also runs `Deps.Shutdown` when startup fails (a bad certificate
  or an occupied port), so dependencies are never left open; startup binds
  every listener before serving and is all-or-nothing.
- The default `http.docs.hosts` uses `https://…:{tls.port}` when HTTPS is on
  and plain HTTP only redirects or is closed.
- **JSON decode is 37-55% faster, and strict or `codec:"required"` decode
  49-57% faster.** Targets that cannot reach an interface (`any`,
  `map[string]any`, `[]any`) are decoded with `json.Unmarshal` instead of a
  `Decoder`, which halves the memory of a typical decode (3.9 MB → 1.8 MB on
  a ~1 MB payload). Targets that reach an interface keep the `Decoder` with
  `UseNumber`, so 64-bit integers inside `any` stay exact. Syntax errors are
  rerun through the `Decoder` path, so error types, messages and offsets are
  unchanged. The presence walker no longer re-runs the eight `Implements`
  checks of `isOpaque` for every value (36% of its CPU): the answer is cached
  per type, as are the walker's descend decision and the field positions.
  Measured on small (0.6 KB), medium (20 KB) and large (1 MB) payloads.
  `Codec.DecodeFrom` is unchanged.

### Fixed

- **Secrets no longer leak when a whole `config.Config` is printed or
  logged.** `Config` (and `database.Config`) now redact the database
  password and DSN, Redis password, URL and CA path, JOSE key secrets and key
  file paths, TLS key paths and OTel endpoint credentials in `fmt`, `slog`
  (text and JSON), `encoding/json` and YAML output. Previously the slog JSON
  handler printed every secret, including the JOSE HS256 key.
  `database.Config`'s DSN redaction also covers the `password` and
  `sslpassword` query parameters.

- **XML decoding is bounded in depth and no longer quadratic in memory.** A
  683 KB document with 100 000 nested elements used about 10 GB; element
  nesting is now limited to 256 levels (`SyntaxError`), and the location of a
  value (`a.b[2].c`) is kept as a linked list and rendered only when an error
  is reported, which also removes the same blow-up from the JSON/CBOR presence
  fallback (131 KB -> 337 MB before).

### Notes

- Nothing changes for existing apps: with no `tls`/`http3` section the server
  is the same plain HTTP server, and `ServerHttp.Srv` is still that
  `*http.Server`.
- Open UDP/8443 in firewalls and load balancers for HTTP/3.
- The `codec` package imports `encoding/json/jsontext`, so it needs the
  `jsonv2` experiment, which Go 1.27 enables by default: building with
  `GOEXPERIMENT=nojsonv2` fails. Benchmarks showed no gain from switching
  the codec to `encoding/json/v2` (encode ties; decode is within about 10% on
  medium and large payloads and about 30% on a 0.6 KB one once the changes
  above are in) and v2 changes `time.Duration`, nil
  slices, key case matching and numbers in `any`, so the codec stays on
  `encoding/json`.

## [v0.1.2] - 2026-10-18

### Changed

- **JSON decode with `codec:"required"` or strict mode uses about a third of
  the memory.** The presence check decoded the whole payload into a generic
  tree (`map[string]any`, every value boxed) before decoding it again into
  the target. It now walks the payload's tokens with
  `encoding/json/jsontext`, descending only into values whose type can fail
  the check, with no per-value allocations. Measured on a ~76 KB payload
  (1000 items): 2.59 MB / 50,959 allocations → 0.89 MB / 8,066 allocations,
  about 2x faster; required fields now cost about the same as none. A typed
  POST request went from 63 to 46 allocations.
- Errors are unchanged: the walker reports exactly the error the tree walk
  reported (same type, same field path, same choice when a payload has
  several problems), and falls back to the tree for invalid syntax,
  duplicate names, one field matched by two spellings and names with
  invalid UTF-8. A differential test and a fuzzer (`FuzzPresenceJSON`)
  compare both. CBOR keeps the tree.
- The response `Content-Type` with charset is a constant (one allocation
  less per JSON/XML response).

### Notes

- Passing `deps.Deps` / `config.Config` by pointer was evaluated and not
  done: every by-value use is at startup (a few KB, once), never per
  request, and the value semantics are deliberate (e.g. `NewHTTPServer`
  applies defaults to a copy).

## [v0.1.1] - 2026-10-18

### Security

- **HTTP server timeouts.** `nhttp.NewHTTPServer` created an `http.Server`
  with no timeouts, so slow or idle clients could hold connections open
  indefinitely (Slowloris, gosec G112). The server now sets
  `ReadHeaderTimeout` (10s), `ReadTimeout` (30s), `WriteTimeout` (30s) and
  `IdleTimeout` (120s). They cannot be disabled.
- The cache example's raw-bytes route (`GET /raw/{key}`) now sends
  `X-Content-Type-Options: nosniff`, so client-stored bytes are never sniffed
  as HTML.

### Added

- `http.server.read_header_timeout`, `read_timeout`, `write_timeout` and
  `idle_timeout` configuration keys (YAML and env file, e.g.
  `HTTP_SERVER_WRITE_TIMEOUT=2m`). Zero means the default; negative values,
  or a `read_header_timeout` longer than `read_timeout`, fail at load.
- `config.ApplyServerDefaults`, also applied by `NewHTTPServer`, so a
  `Config` built without `config.Load` still gets a server with timeouts.
- `.golangci.yml`: the repository's golangci-lint v2 configuration (standard
  linters plus bodyclose, errorlint, gosec, nilerr, noctx, rowserrcheck,
  sloglint, sqlclosecheck, unconvert, usestdlibvars, misspell; gofmt and
  goimports). The framework and the examples are clean against it.
- `CHANGELOG.md`.

### Changed

- **Behavior change:** responses that take longer than 30s are now cut by
  `write_timeout`. Long-running or streaming handlers should raise
  `http.server.write_timeout` or extend their own deadline with
  `http.NewResponseController(w).SetWriteDeadline`.
- Fatal startup logs of the logger and tracing setup use `slog.ErrorContext`.
- Sentinel error checks in the cache retry classification and in tests use
  `errors.Is` (same behavior: the compared values are plain sentinels).
- Intentional lint exceptions are documented in place with line-level
  `//nolint:<linter> // <reason>` (trace-ID and retry-jitter randomness, the
  AEAD nil nonce, the validated docs accent, error identity checks).

### Fixed

- **Cache (Redis): lost `context.DeadlineExceeded` cause.** go-redis uses the
  operation context's deadline as the socket deadline, so a read could time
  out a few microseconds before `ctx.Err()` reported it; the error then
  matched `ErrUnavailable` but not `context.DeadlineExceeded`. The deadline
  is now checked directly. This also made `TestRedisOperationDeadline` fail
  in roughly a third of runs.
- Build docs: golangci-lint must be **v2.15.0 or newer** for Go 1.27.2.
  Older releases (v2.13.x) fail every package with "export data version"
  typecheck errors; the previously documented `GOTOOLCHAIN=local` workaround
  does not fix it.

## [v0.1.0] - 2026-10-18

First public release: layered configuration, slog logging, OpenTelemetry
tracing, the JSON/CBOR/XML codec, the HTTP server with typed routes and
OpenAPI 3.1 docs, PostgreSQL with multi-tenancy, JOSE/JWT authentication,
the Redis/memory cache, `ncrypto` and `safecalc`.

[v0.1.2]: https://github.com/ams-soft/nforge/compare/v0.1.1...v0.1.2
[v0.1.1]: https://github.com/ams-soft/nforge/compare/v0.1.0...v0.1.1
[v0.1.0]: https://github.com/ams-soft/nforge/releases/tag/v0.1.0
