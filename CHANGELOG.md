# Changelog

All notable changes to nForge are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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
