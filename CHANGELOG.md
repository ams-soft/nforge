# Changelog

All notable changes to nForge are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Package `module`: a neutral core that resolves explicitly registered modules
  into a deterministic, acyclic plan before anything starts, and runs their
  lifecycle (rollback, reverse shutdown, status).
- `deps.Bootstrap(ctx, cfg, extra...)`, exported capabilities (`deps.CapDatabase`,
  `CapCache`, ...), `deps.Value`, `Deps.Graph` and `Deps.ModuleStatus`.
- `http.routes.graph`, off by default and compiled only with `-tags nforge_graph`:
  an HTML page with the module graph in three tabs (Visual, Mermaid JS, JSON).
  Mermaid is embedded, nothing loads from a CDN. Enabling it in a binary built
  without the tag panics at startup.
  The page is **public** and exposes the topology: `env: prd` warns once per
  bootstrap, and the warning is not access control. Restrict the path at the
  gateway. See `AGENTS.md` §7.13.
- `FuzzDecodeXML` and `FuzzDecodeCBOR`.

### Changed

- `deps` is now an orchestrator: logger, tracing, codec, database, JOSE and cache
  are module descriptors. `NewDeps`, `NewDepsE`, the `Deps` fields, the start
  sequence and the shutdown order are unchanged.
- A `Config` built by hand with `http.auth.default_profile` and jose disabled now
  fails at bootstrap instead of panicking when a private route is registered.
  `config.Load` already refused it.

### Fixed

- A shutdown hook that hung until the deadline left every later hook with an
  expired context, so OTel logs and traces, which close last, could not flush.
  Those hooks now get 1 s of their own, so `Deps.Shutdown` can outlast its context
  by 1 s per remaining hook.

## [v0.2.0] - 2026-11-05

This release contains several breaking changes; read the **Upgrading** notes below.

### Added

- A startup warning when HTTPS or HTTP/3 is on while `cache.redis` or the
  database connection does not use TLS.
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

- **Smaller hardening items from a second read of the codebase.** A `jws`
  profile cannot share the `typ` of a `jwt` profile (config error); query
  float parameters reject `NaN` and `Inf`; a transaction's rollback no longer
  uses the request context (a canceled request left it open); and
  `database.DB.Shutdown` honors its context instead of waiting forever for a
  stuck connection. A key source that is a FIFO is refused instead of blocking
  startup.

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

- **A response (or encode input) that contains a reference cycle is an error,
  not a crash.** The CBOR library and the XML encoder recursed until the Go
  stack overflowed, which kills the whole process, and `codec.CheckRequired`
  (run on every typed-route response) did the same. All three now return an
  `EncodeError` (a typed route answers `500 failed to build the response`).
  Only fields the format encodes count, and a pointer reached twice by
  different routes is not a cycle.
- `codec.CheckRequired` no longer walks a response whose type has no
  `codec:"required"` field and reaches no interface: it took about 1.9 ms per
  MB of response for nothing.

- **Configuration typos fail the load instead of silently using a default.**
  Unknown YAML keys are now an error in every section (they were ignored
  outside `jose`: `http.cors.origns` left `origins` empty, which means "any
  origin", and `http.security.enabld` left the security headers off).
  Env-file keys that match no field are an error too. `env` must be `local`,
  `dev`, `stg` or `prd` (it is lower-cased; `production`/`prod` used to run
  at DEBUG without the production overlay). A `-config` file that does not
  exist, and an overlay that cannot be read, are errors. Unset `${VAR}`
  references are reported in `Config.Warnings` and logged at startup.
  Top-level `x-*` keys are ignored, for application data.

- **A fatal startup error is no longer silent.** With logging disabled (the
  default) `deps.NewDeps` used to exit with status 1 and print nothing, and
  with OTel logging it exited before the batch was flushed. The reason is now
  always written to stderr, the modules already started are shut down first,
  and `deps.NewDepsE` returns the error for embedding and tests (`NewDeps` is
  the exit-on-error wrapper). A tracing setup failure is an error instead of a
  panic.

- **Sizes that overflow are an error.** `config.ByteSize` turned values such
  as `9999999999gb` or `9223372036854775807` into a negative number (a
  `float64` to `int64` conversion) without any error. It now shares the
  parser of `cache.ByteSize`: whole numbers are exact and overflow is rejected.

- **Env-file values are literal.** godotenv expanded `$NAME` / `${NAME}` in
  unquoted and double-quoted values from the keys defined earlier in the file
  (an unknown name became empty), so a password such as `pa$SWORD` silently
  lost a piece. `$` is now an ordinary character in every value. A value
  that relied on `B=$A` expansion must be written out.

- **`cache.GetOrLoad` heals an entry that no longer decodes.** After a deploy
  that adds a `codec:"required"` field or tightens `Validate`, every call for
  a key written by the old version failed with `ErrDecode` until its TTL ended
  and the loader never ran. A hit that does not decode is now a miss: it is
  logged as a warning (without key or value), reloaded and overwritten.
  `Get` is unchanged and stays strict.
- `cache.limits.max_value_bytes` larger than `codec.max_payload_bytes` is now
  a configuration error: such values could be stored but never read back.

- **`Validate()` now runs on nested values.** The registry called
  `Validate() error` on the decoded root only, so a method on a nested struct,
  a slice element or a map value was silently never called. It now runs on
  every value inside the target (children before parents, pointer or value
  receiver, location in the message) for all formats and for query strings.

- **The docs page no longer trusts the whole CDN.** Swagger UI is pinned to
  `swagger-ui-dist@5.33.1` with Subresource Integrity hashes (it was the
  floating `@5`, unverified), and the page's `script-src` is the per-render
  nonce alone instead of the nonce plus all of `cdn.jsdelivr.net`.

- **Decode errors no longer name Go types.** A type mismatch answered
  `field "addr" expects nhttp.Address, got value string`, disclosing package
  and type names to clients. The message now says `expects object` (`integer`,
  `string`, `array`, ...) through the new `codec.WireType`; `TypeError.Expected`
  still holds the Go type for logs and `errors.As` callers, and a new
  `TypeError.Wire` holds the payload-level name. One XML error message that was
  in Portuguese is now English.

- **Guard rails on risky settings.** `codec.max_payload_bytes` is capped at
  256 MiB; JWT `ttl`/`max_ttl` are capped by the new `jose.limits.max_token_lifetime`
  (default 720h, at most 8760h) because tokens cannot be revoked; startup now
  warns (in the log, not an error) about a TLS private key that group or others
  can read, Redis credentials sent without TLS to a non-local host, and a
  PostgreSQL password sent with an `sslmode` that can fall back to clear text.
- **`http.security.hsts.include_subdomains` now defaults to `false`.**
  `includeSubDomains` pins HTTPS on every subdomain for the whole `max_age`,
  which breaks unrelated HTTP-only services when the API is served from an apex
  or shared domain; set it to `true` explicitly for a dedicated host.

- `Authorization: Bearerx token` (any scheme that merely starts with
  "Bearer") is now 401 `authentication required` like every other scheme, not
  a 400 malformed bearer request.
- Log records carry `source` (file:line) only in `env: local`, not in
  dev/stg/prd or the OTel export.
- `safecalc.Round` returns its input unchanged when there are no digits left to
  round (`Round(1.5, 400)` was NaN, `Round(1e300, 10)` was +Inf).
- `Deps.Shutdown` and `ClearAux` are safe to call concurrently: a closer never
  runs twice when an application also shuts down on its own signal.

- **Request limits.** Every request context now has a deadline,
  `http.server.handler_timeout` (default 30s), so handlers, queries and loaders
  that honor `ctx` stop instead of outliving the connection; every request
  body is bounded by `http.server.max_body_bytes` (default
  `codec.max_payload_bytes`), which covers raw routes that read `r.Body`
  directly (a typed route over a smaller limit answers 413); and PostgreSQL
  connections get `statement_timeout` (`database.connection.statement_timeout`,
  default 30s, a value in the DSN wins) so a runaway statement cannot hold a
  pooled connection forever.
- **A request body without a `Content-Type` is refused** (415 `missing
  content-type`). A cross-site page can send an untyped body without a CORS
  preflight, which let any website make the API decode and act on
  attacker-chosen JSON. Requests without a body are unaffected.
- Envelope responses carry `X-Content-Type-Options: nosniff`, and responses of
  private (`IsPrivate`) typed routes, 401/403 included, carry
  `Cache-Control: no-store` unless the handler sets its own.

- **`cache.IncrementWithTTL`** creates a counter and gives it its TTL in one
  atomic step (Redis: a Lua script; memory: under the lock). Building a
  rate-limit window from `Increment` + `Expire` left a counter that never reset
  whenever the process died, timed out or lost the reply between the two calls.
  An existing counter keeps its remaining TTL.

- **TLS certificates and JOSE keys rotate without a restart.** HTTPS and HTTP/3
  now serve the certificate through `GetCertificate`: a handshake re-reads a
  changed cert/key pair (checked at most every 30 s), and **SIGHUP** reloads
  every certificate and the JOSE keys (new `Deps.ReloadJOSE`). A pair that
  fails to load keeps the previous one. Previously a renewal meant restarting
  the process and dropping every connection.
- **Settings on the exported `ServerHttp.Srv` apply to every transport.**
  `MaxHeaderBytes`, `ErrorLog`, `ConnState`, `BaseContext`, `ConnContext`,
  `Protocols` and `HTTP2` were honored by the plain HTTP listener only; HTTPS,
  the redirect listener and HTTP/3 ignored them.

- **Tracing can be sampled and can distrust the caller.** New
  `tracing.otel.sample_ratio` (default 1) records a fraction of traces, and
  `tracing.otel.trust_incoming` (default **false**) makes the HTTP middleware
  ignore a caller's `traceparent`, `tracestate`, `baggage` and
  `X-Request-Id`, so a client can no longer force sampling, join another
  request's trace or inject baggage into downstream calls. Services behind a
  gateway set it to `true` to keep linking traces.
  `nhttp.Tracing` takes options (`WithTrustedIncomingContext`,
  `WithoutIncomingContext`; untrusted by default) and
  `telemetry.IDGenerator` is exported.

- **The public spec endpoints are cached.** `/docs/swagger.json` and
  `.yaml` were rebuilt by reflection on every unauthenticated request; they are
  now rendered once per route set, carry an `ETag` and answer `If-None-Match`
  with 304. Startup warns when `http.docs` is enabled in `env: prd`, because
  the page and the spec are public and list every route, auth profile and scope.

### Notes

- `safecalc.MoneyOpt.Decimals` (`safecalc.Places(n)`, 0..18, default 2) supports
  currencies with 0 (JPY) or 3 (KWD, BHD) minor digits in `Format` and
  `NewFromFloat`.

- **Upgrading:** with `env: prd`, a TLS private key readable by group or
  others, Redis credentials or HTTPS/HTTP/3 in front of Redis without TLS, a
  PostgreSQL `sslmode` that can fall back to clear text (non-local host) and
  are now load errors instead of warnings. Fix the setting or accept it
  explicitly with `prd_exceptions.tls_key_permissions`, `redis_plaintext` or
  `database_plaintext` set to `true` (it then stays a warning). Other
  environments are unchanged.
- In `env: prd`, `http.docs`, `http.routes.health` and `http.routes.readiness`
  enabled log one warning (they are unauthenticated); `app.internal_use: true`
  declares the service is not internet-facing and silences it. Config warnings
  are logged at ERROR level in `prd` so the log filter does not hide them.
- **Upgrading:** an unset `${VAR}` in a config file is now a load error
  (it used to expand to an empty string with a warning). Use
  `${VAR:-default}`, or `${VAR:-}` for an optional variable.
- **Upgrading:** `http.security` is on by default (CSP, HSTS, X-Frame-Options,
  Referrer-Policy, Cross-Origin-*). Set `http.security.enabled: false` to
  restore the old behavior. `http.cors` stays off: with no origins it would
  mean "any origin".
- **Upgrading:** a scalar typed-query parameter sent twice (`?page=1&page=2`)
  is now a 400; only slice fields accept repetition.
- **Upgrading:** `tracing.otel.trust_incoming` now defaults to `false` and
  `nhttp.Tracing()` ignores the caller's trace context unless given
  `WithTrustedIncomingContext()`. Services that relied on a gateway's
  `traceparent` or `X-Request-Id` must set `trust_incoming: true`.
- **Upgrading:** the process now handles SIGHUP (it reloads credentials)
  instead of terminating on it.
- **Upgrading:** clients that send a body with no `Content-Type` header now
  get 415; long-running statements and handlers over 30s need
  `database.connection.statement_timeout` / `http.server.handler_timeout`
  raised.
- **Upgrading:** requests that decode into types with a nested `Validate()`
  are now validated by it, so some payloads that were accepted may start to
  answer 422.
- **Upgrading:** configs and env files with keys that match no field now
  fail to load; rename or remove them (or move application data under a
  top-level `x-` key).
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

[v0.2.0]: https://github.com/ams-soft/nforge/compare/v0.1.2...v0.2.0
[v0.1.2]: https://github.com/ams-soft/nforge/compare/v0.1.1...v0.1.2
[v0.1.1]: https://github.com/ams-soft/nforge/compare/v0.1.0...v0.1.1
[v0.1.0]: https://github.com/ams-soft/nforge/releases/tag/v0.1.0
