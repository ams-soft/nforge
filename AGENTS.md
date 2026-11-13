# AGENTS.md — nForge (Nano Forge)

This file is the single source of truth for any AI agent (or human) working on
this repository. Read it fully before changing code. It explains **what nForge
is, how every package works, how the pieces fit together at runtime, the
conventions the code follows, and the non-obvious traps**. If something here
disagrees with the code, the code wins — fix this file in the same change.

---

## 1. What nForge is

nForge (module `github.com/ams-soft/nforge`, by AMS Software e Consultoria
LTDA) is a small, opinionated **Go application runtime**, built first for
HTTP APIs: an application is a set of explicitly registered modules (HTTP is
one of them and optional, so HTTP-only, worker-only and mixed applications
share one lifecycle), wired by a typed capability graph and orchestrated by
package `app`. An application gets, from one `config.yaml`:

- **An application lifecycle** (`app`, `module`): explicit registration,
  configuration validated before any I/O, a deterministic dependency graph,
  every Init before any Start, ingress started last and drained first,
  aggregated readiness, runtime failure handling, signal-driven `Run` and an
  idempotent graceful shutdown under one deadline (§3).
- **Layered configuration** (YAML + `${ENV}` expansion + `.env` overrides +
  per-environment overlays + defaults + validation).
- **Structured logging** with `log/slog` — pretty terminal output (`tint`)
  and/or OpenTelemetry log export (OTLP/gRPC), with `request_id` injected
  automatically.
- **Distributed tracing** with OpenTelemetry, where `request_id == trace_id`.
- **A multi-format codec** (JSON, CBOR, XML) driven by a single struct
  definition, with strict mode, required fields, payload limits, XXE
  protection and post-decode validation.
- **An HTTP server** on top of `github.com/ams-soft/go-ams-router` (a chi-like,
  zero-alloc router by the same company) with:
  - a standard response **envelope** (`{success, status, data | error}`);
  - **typed routes** (`TGet/TPost/TPut/TPatch/TDelete`) that decode and
    validate the request *before* the handler and validate the response
    *after* it;
  - **auto-generated OpenAPI 3.1 docs** (HTML page + `swagger.json` +
    `swagger.yaml`) built from the typed routes;
  - built-in health/readiness routes and graceful shutdown;
  - **static files and single-page applications** (`Server.Static`/`SPA`, from a
    directory or an `fs.FS` such as `embed.FS`) registered in Go, with a SPA
    fallback that never shadows the API (§7.14).
- **PostgreSQL access** via `pgx/v5` pool + Squirrel query builder, with a
  transaction helper and optional **multi-tenant scoping** (Row-Level
  Security or tenant column) plus tenant middlewares in `nhttp`.
- **JOSE** (`jose/`): compact JWS/JWE and JWT issuance/validation through
  explicitly configured profiles, plus **bearer authentication** in `nhttp`
  (`Authenticate`, scopes, `Principal`, enforced `IsPrivate` typed routes)
  and a public **JWKS** endpoint.
- **Cache** (`cache/`): one non-generic `*cache.Cache` (`cache.Capability`) on
  Redis (go-redis) or a bounded in-process LRU, chosen by config: codec
  objects, raw bytes, counters, TTLs, NX, cache-aside `GetOrLoad`, tenant
  namespacing through the framework tenant context, readiness and LIFO
  shutdown.
- **Messaging** (`message/`): RabbitMQ AMQP 0-9-1 with confirmed publishing, fanout publish and ephemeral subscriptions,
  Classic and Quorum queues, opt-in DLQ/retry, concurrent consumers and
  lifecycle-integrated reconnect/readiness. `message.Module()` owns the
  connection; `message.Consumers()` is ingress and drains before it closes.
- **Scheduler** (`scheduler/`): an independent infrastructure module that runs
  handlers on five-field Cron expressions (`AddJob("0 7 * * *", fn)`), local mode,
  IANA time zone, parallel executions with an optional per-job cap that skips
  (never queues), no retry, no persistence; an ingress module that needs no HTTP
  (§11D).
- **Email** (`email/`): an independent infrastructure module with a synchronous
  SMTP `Client.Send` (one recipient, text and/or HTML, verified STARTTLS or
  implicit TLS, no retry, no queue; success is "accepted by the server", never
  "delivered") and `email.RenderMJML`, a pure-Go MJML-to-HTML helper that needs
  no module (§11E).
- **Metrics** (`metrics/`): a dependency-free registry (`metrics.Capability`)
  that renders the Prometheus text format; the HTTP server, SSE, Messaging, the
  database pool and the cache register their instruments on it when it is
  present (§11C).
- **Server-Sent Events** (`sse/`): an explicitly registered module with
  user-targeted delivery only (`Manager.PublishToUser`, `Manager.Handler`) over a
  `memory` or `rabbitmq` driver; authenticated streams, bounded per-connection
  queues, heartbeats, limits and a graceful shutdown that closes streams before
  HTTP drains (§11B).
- **Crypto** (`crypto/`, package `ncrypto`): standalone primitives with no
  config or module wiring: AEAD encryption (XChaCha20-Poly1305, AES-256-GCM),
  HMAC-SHA256, password hashing (bcrypt, Argon2id), data hashing (BLAKE3,
  SHA-256) and secure random values (bytes, keys, ranges, strings, ULIDs).
- **Transports** (`http.server.tls`, `http.server.http3`): plain HTTP by default
  (`:8080`), opt-in HTTPS over TCP (`:8443`, TLS 1.3 minimum) and HTTP/3 over
  QUIC/UDP (`:8443`), one shared router, an optional redirect-only HTTP
  listener (§7.12).
- **Helpers** (`helper/`): small standalone utilities; today `safecalc`
  (monetary amounts in integer cents with exact display formatting) and
  `qrcode` (QR Codes as PNG, SVG, Base64 and Data URI, rounded styles, logo).

Framework version: `version` in the **module root** `config.yaml`
(currently `0.9.0`), embedded at build time and exposed as `nforge.Version`
(`version.go`). This file is *not* the application's config. Releases are
git tags `vX.Y.Z` on `main` (published on pkg.go.dev through the Go module
proxy); bump this `version` in the commit that gets tagged, so
`nforge.Version` matches the tag, and add the release to `CHANGELOG.md`.
Never move or recreate a published tag: the module proxy keeps the first
content it saw for a version forever; fix forward with a new patch release.

Toolchain: **Go 1.27**, pinned to `toolchain go1.27.4` in `go.mod` (stdlib
security fixes; `GOTOOLCHAIN=auto` downloads it; go-ams-router itself
needs 1.24+). The code uses
modern Go freely: `range` over ints, `strings.SplitSeq`, `reflect.TypeFor`,
generics, `errors.Join`, `math/rand/v2`, `maps`/`slices` iterators.

---

## 2. Repository layout

```
.
├── version.go          # package nforge: Version, read from the embedded root config.yaml
├── config.yaml         # FRAMEWORK metadata only (version). Not an app config.
├── CHANGELOG.md        # release notes, one section per tag
├── .golangci.yml       # golangci-lint v2 configuration (§15)
├── .github/workflows/  # CI (§15)
├── docs_test.go        # versions cited in the docs must match go.mod and config.yaml
├── docs_config_test.go # every configuration key of every module is documented in §4.2
├── imports_test.go     # the import rules below, signals owned by app only, deps stays removed
├── app/                # the application orchestrator: New, Start, Run (signals), Shutdown, Status, Reload
├── module/             # the capability/DI graph: Descriptor, Definition, Build, Runtime, Scope; stdlib only
├── config/             # the configuration loader: Document (raw sections), Binding, env file, overlays
├── logs/               # slog setup (terminal tint + OTel) and the logger module (section logging)
├── telemetry/          # OTel resource, TracerProvider, trace-id generator, the tracing module (section tracing)
├── codec/              # multi-format encode/decode (JSON, CBOR, XML), Registry, ByteSize, the codec module
├── database/           # PostgreSQL: Config, pool, DB (Executor + Squirrel), transactions, tenancy, the database module
│   └── internal/rlsexec/   # tenant-scoped Executor of rls mode (set_config batches) + SetTenantSQL
├── jose/               # JOSE: config, keys/JWK/JWKS, compact JWS/JWE, JWT, Service lifecycle, the jose module
│   └── internal/strictjson/  # strict protocol JSON: UTF-8, depth, duplicate members, trailing data
├── cache/              # cache: Config, Open, Redis + memory drivers, keyspace/tenant, GetOrLoad flights, the cache module
├── message/            # RabbitMQ transport: queues, confirms, consumers, retry/DLQ, readiness and lifecycle modules
├── metrics/            # metrics registry (Prometheus text format) and the metrics module (§11C)
├── scheduler/          # Cron scheduler: Config, parser, Scheduler (AddJob), the scheduler module (§11D)
├── email/              # SMTP client (Send), MJML renderer (RenderMJML), the email module (§11E)
├── sse/                # Server-Sent Events: Manager, Handler, memory + rabbitmq drivers, the sse module (§11B)
├── crypto/             # package ncrypto: AEAD (XChaCha20, AES-GCM), HMAC, password/data hashes, secure random
├── helper/
│   ├── safecalc/       # money in integer cents: New, NewFromFloat (half-even), Format, BRL
│   └── qrcode/         # QR Code encoder + PNG/SVG renderers, rounded styles, logo (§13.2)
├── internal/apptest/   # test support: applications from inline YAML (Load, New, Start, Quiet)
├── http/               # package nhttp: only the public surface at the root
│   ├── module.go       # Module (section http, ingress), Capability, cross-module Check
│   ├── config*.go      # Config of the http section: types, defaults, validation, transports, security, graph path, warnings
│   ├── server.go       # Server, New, Dependencies, Use, standard middleware chain
│   ├── transport.go    # Start/Stop/Reload: HTTP/HTTPS/HTTP3 listeners, redirect-only handler, Alt-Svc
│   ├── certs.go        # certReloader: certificate rotation on file change and on Reload
│   ├── routes.go       # built-in routes: health/readiness, docs + OpenAPI spec, JWKS (collision check)
│   ├── graph.go        # module graph page and its Mermaid script (build tag nforge_graph; graph_off.go without it)
│   ├── static.go       # Static/StaticFS/SPA/SPAFS: file mounts, SPA fallback, os.Root directories (§7.14)
│   ├── response.go     # Send*/Parse*/ParseStatus, Envelope, ErrorTitle, SuccessResponse/ErrorResponse (aliases)
│   ├── middleware.go   # Tracing, Logger (thin wrappers)
│   ├── typed_route.go  # Opts, TypedRoute, TypedRouter, T*, Body/Query, request flow (wrap)
│   ├── auth.go         # Authenticate, options, RequireScopes, Principal + PrincipalFromContext
│   ├── tenant.go       # Tenant/SetTenant middlewares, resolvers (claim, header), Opts.Tenant guard
│   ├── security.go     # http.security / http.cors → router Security/CORS options, docs CSP header
│   └── internal/
│       ├── envelope/   # envelope types, Title, BodyAllowed (shared with capture and openapi)
│       ├── capture/    # capture.Writer handed to typed handlers + response conforming
│       ├── query/      # typed query string model and decoding
│       ├── openapi/    # OpenAPI 3.1 builder, schemas, docs page (embedded docs.html)
│       ├── graph/      # graph Document (the canonical form), JSON, Mermaid, page.html, embedded Mermaid bundle
│       ├── authn/      # bearer transport, WWW-Authenticate challenge, scope/realm/header syntax
│       ├── jwks/       # JWKS handler (GET/HEAD, ETag, 304)
│       ├── tracing/    # Tracing middleware + request_id = trace_id
│       └── accesslog/  # Logger middleware + statusWriter
├── _examples/          # runnable examples (§14): basic, complete, jose, cache, database, message, static, sse, scheduler, email
│   ├── <example>/docker-compose.yml # local dependencies stay with each example
│   ├── message/        # RabbitMQ worker-only producer/consumer/reprocess example
│   └── observability/  # collector/tempo/loki configs + Grafana provisioning for complete
├── README.md / LICENSE # project overview; MIT, AMS Software e Consultoria LTDA
├── llms.txt            # llmstxt.org index for LLMs: notes, links to packages and examples; the reference is this file
├── .air.toml           # hot reload of _examples/complete (gitignored)
└── Makefile            # container-up / dev (gitignored)
```

Notes:
- The `http/` directory's package name is **`nhttp`** (to avoid clashing with
  `net/http`, which is always imported as `stdhttp` inside it). Applications
  import it as `nhttp "github.com/ams-soft/nforge/http"`.
- `internal/` subpackages hold implementation details that must not become
  public API. They are only imported by their parent and follow its import
  rules. Public types that internal code needs are defined there and
  re-exported as aliases (`nhttp.SuccessResponse = envelope.Response`); keep
  that to a minimum — where every file depends on the package's public
  errors/config (`cache`, `codec`, most of `jose`), the code stays flat.
- The `crypto/` directory's package name is **`ncrypto`** (to avoid clashing
  with the stdlib `crypto`); import it as
  `ncrypto "github.com/ams-soft/nforge/crypto"`.
- `_examples/` starts with `_`, so `go build ./...` / `go test ./...` ignore it.
  Name the examples explicitly (§15). They are in the same module.
- `.gitignore` excludes `.air.toml`, every local `.env`, `_examples/*/tmp/`,
  `_examples/jose/keys/`, `.idea/`, `Makefile`, `sandbox*` and `./basic`
  (the binary of `go build ./_examples/complete`).

### Package dependency graph (imports)

```
module    (stdlib only)
config    (no internal deps; yaml.v3 + godotenv)
app ──► module, config
codec ──► module
database ──► module
jose ──► module (go-jose/v4 behind unexported adapters)
cache ──► module, codec, database (tenant context only; go-redis/v9 behind unexported adapters)
message ──► module, codec, telemetry (trace context), metrics (RabbitMQ AMQP 0-9-1 driver; no AMQP types in its public API)
metrics ──► module
scheduler ──► module, metrics (stdlib only otherwise; no HTTP, no broker)
email ──► module, metrics, telemetry (stdlib net/smtp + mjmlgo; no HTTP, no broker, no database)
sse ──► module, codec, nhttp (principal, BeginStream, SendJSON), message (fanout exchange for the rabbitmq driver), metrics
telemetry ──► module
logs ──► module, telemetry
ncrypto, helper/*   (no internal deps)
nhttp ──► nforge (Version), module, codec, database, jose, cache, message, telemetry, go-ams-router, quic-go (http3)
internal/apptest ──► app, config, module (tests only)
```

Keep it acyclic. `module` and `config` know no concrete module; `app` knows
only `module` and `config`, so it orchestrates any set of modules without
importing them. The infrastructure packages import `module` (to describe
themselves) and nothing above them. Only `app` imports `os/signal`.
`imports_test.go` enforces these rules (internal subpackages included), and
fails for a package without a rule.

---

## 3. Runtime lifecycle (how an app boots and stops)

Canonical `main` (see `_examples/complete/main.go`):

```go
cfg, err := config.Load()                 // 1. flags -config / -envfile; one logical YAML, read once
if err != nil { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
a, err := app.New(cfg, app.Modules(       // 2. explicit registration; configure + resolve + check, no I/O
    logs.Module(), telemetry.Module(), codec.Module(),
    database.Module(), jose.Module(), cache.Module(),
    nhttp.Module(), apiModule(),          //    HTTP is optional: a worker-only app omits it
))
if err != nil { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
if err := a.Run(context.Background()); err != nil { // 3. Start, block on SIGINT/SIGTERM/fatal, Shutdown
    fmt.Fprintln(os.Stderr, err); os.Exit(1)        //    only main chooses the exit code
}
```

An application module declares what it uses and registers its routes (or
consumers) during `Init`, before any listener opens:

```go
func apiModule() module.Descriptor {
    return module.New("api", module.Definition{
        Requires: []module.Handle{nhttp.Capability, database.Capability},
        Optional: []module.Handle{module.Logger},
        Init: func(ctx context.Context, s *module.Scope) (module.Hooks, error) {
            server, db := module.Require(s, nhttp.Capability), module.Require(s, database.Capability)
            nhttp.Get(server.Typed(), "/users", listUsers(db))
            return module.Hooks{}, nil
        },
    })
}
```

### 3.1 Registration, configuration and the graph (`app.New`, `module.Build`)

A module is a `module.Descriptor{ID, Section, Configure}`. `app.New` binds the
configuration (§4) and calls `module.Build`, which runs three pure steps with
**no I/O** and fails before anything opens:

1. **Configure** every descriptor: it decodes its own section strictly
   (`Source.Decode`: base YAML, env file, overlay), applies its defaults,
   validates it and returns a `module.Definition`: `Disabled`, `Provides`,
   `Requires`, `Optional` (typed `module.Handle`s), `Ingress`, `NonCritical`,
   `Config` (the validated typed section), `Check` and `Init`. Errors of
   independent modules are **aggregated** (`*module.ConfigError`, text
   `module <id>: ...`), and so are the sections no registered module owns and
   the env file keys nothing consumed (`config.Binding.Finish`). A panicking
   Configure is an error, not a crash.
2. **Resolve** the graph: duplicate IDs or section owners
   (`ErrDuplicateModule`), invalid IDs or definitions (`ErrInvalidModule`),
   two enabled providers of one capability (`ErrAmbiguousProvider`), a
   required capability nobody provides (`ErrMissingDependency`) or whose
   provider is disabled (`ErrDisabledDependency`; never enabled for you), two
   declarations of one capability ID with different Go types
   (`ErrTypeMismatch`), cycles (`ErrCycle`). Optional edges exist only when
   the provider is enabled. The order is topological with ties by
   registration order: one configuration gives one plan. The plan is frozen.
3. **Check** each enabled module against the resolved graph. `View` gives the
   validated `Config` of the providers it declared
   (`module.ConfigOf[C](v, handle)`, also for a registered but disabled
   provider; an undeclared one is `ErrUndeclared`) and `Warn`. This is where
   cross-section rules live: `cache.codec` must be in `codec.formats.enabled`,
   `http.auth.default_profile` must be a consuming jwt profile of the jose
   module, the JWKS path must not collide with a built-in route, HTTPS in
   front of a database, Redis or RabbitMQ without TLS warns.

`Source.Warn(exception, msg)` / `View.Warn` record valid settings that weaken
the deployment: outside `prd` they are `Application.Warnings()` (logged at
WARN, at ERROR in `prd`, once the modules are initialized); with `env: prd`
each one is an error of `app.New` unless `prd_exceptions.<exception>` is true.

Capabilities are `module.Capability[T]` handles; the Go type is part of the
identity. `module.Logger` (`*slog.Logger`, ID `logger`) is defined in
`module` so every module can declare it without importing `logs`. Each
infrastructure package exports its own: `codec.Capability`,
`database.Capability`, `jose.Capability`, `cache.Capability`,
`message.Capability`, `telemetry.Capability` (`*telemetry.Tracing`), `nhttp.Capability`
(`*nhttp.Server`). Capabilities are singletons **per application**: two
applications in one process share nothing.

### 3.2 Start (`Application.Start`, `module.Runtime.Start`)

1. **Init every module** in dependency order. `Init(ctx, scope)` may allocate
   resources (pool, keys, router) and publishes its capabilities with
   `module.Provide`; it must **not** accept external work, and it must release
   what it created before returning an error. Every declared capability must
   be published with its declared type (`ErrNotProvided` otherwise). The
   scope rejects undeclared reads and wrong handle types (a panic, which the
   runtime turns into an Init error with rollback). `Scope.Context()` lives
   until the shutdown ends, for background work the module owns.
2. `OnInitialized`: the configuration warnings are logged with the
   configured logger.
3. **Start**, only after every Init succeeded: first the ordinary modules in
   dependency order, then the **ingress** modules (`Definition.Ingress`: the
   HTTP listeners, queue consumers). `Start` returns once the service is
   active (listeners bound) and never blocks on a serve loop; the module owns
   its goroutines and reports their unexpected end with
   `Scope.ReportFailure`. So no request or job can reach a handler that is not
   registered yet.

Any failure (an Init or Start error, a canceled ctx, a failure reported
during the start, a concurrent `Shutdown`) logs `[Fatal ERROR] ...` with the
logger that exists, stops everything that was initialized (with
`RollbackTimeout`, 5 s, independent of the start ctx) and returns a
`*module.LifecycleError{Module, Phase, Err}` joined with the rollback errors.
The application is then `failed` and cannot be started again.

### 3.3 Running, readiness and failures

- `Application.Status(ctx)` returns a `module.Report{Phase, Ready, Modules}`,
  independent of any transport. Phases: `validated`, `initializing`,
  `starting`, `started`, `stopping`, `stopped`, `failed`. Module states:
  `disabled`, `pending`, `initialized`, `started`, `stopping`, `stopped`,
  `failed`; availability `ready`, `not_ready`, `disabled`.
- `Ready` is true only while the application is started, no module failed
  and every **critical** enabled module is ready (`NonCritical` modules show
  their state without counting). Probes (`Hooks.Ready`) are concurrency-safe,
  context-aware, run with their own 2 s bound and are **shared**: a sample is
  reused for 1 s and concurrent callers wait for the probe in progress, so
  readiness traffic cannot flood PostgreSQL or Redis
  (`app.WithProbeInterval`). Liveness (`/health`) never probes.
- A recoverable outage makes `Ready` return an error (`not_ready`) until it
  ends: the module's own reconnection policy applies (pgx and go-redis
  reconnect). A **fatal** runtime failure (`Scope.ReportFailure`: a listener
  that died) marks the module `failed`, drops readiness at once and closes
  `Application.Failed()`; `Run` then shuts down and returns it.
- `Application.Reload(ctx)` runs the `Hooks.Reload` of the started modules in
  order (TLS certificates, JOSE key files); a failure keeps the previous
  credentials. `Run` calls it on **SIGHUP**.

### 3.4 Shutdown (`Application.Shutdown`, `Run`)

`Shutdown(ctx)` is idempotent and safe for concurrent use: every call waits
for the first and returns its aggregated result. ctx is the **global
deadline** (`Run` uses `app.WithShutdownTimeout`, 15 s, on a context
independent of the one that ended).

1. A start in progress is canceled and waited for.
2. **Quiesce**: the ingress modules stop first. `nhttp` stops accepting,
   drains the requests in flight (at most 10 s within the deadline), closes
   its listeners and waits for its serve loops; a consumer stops fetching
   and finishes its jobs.
3. **Barrier**: only when every ingress module finished do the others stop,
   each one after every module that depends on it (independent modules
   concurrently). A resource is therefore never closed while a handler or a
   consumer may still use it; the logger stops last.
4. Every hook runs even if another fails; errors are joined
   (`*module.LifecycleError` with `PhaseStop`). Once ctx has ended, the hooks
   still running or not yet started share **one** extra window
   (`Options.StopGrace`, 1 s), so the overrun is bounded. A Stop hook that
   has not returned by then is abandoned with `ErrStopTimeout`, and the
   modules it depends on are **left open** (reported, not closed under it).
5. `Scope.Context()` ends; the phase is `stopped`.

`Run(ctx)` = `Start` + wait for ctx, SIGINT/SIGTERM or `Failed()` + SIGHUP
reloads + `Shutdown`. It returns nil after a clean signal-driven stop and the
runtime failure (joined with any shutdown error) otherwise. It works with zero
HTTP modules. `Start`/`Shutdown` are the library API for tests and
embedding: no signals, no `os.Exit`.

**Process globals.** By default nothing global is touched: no
`slog.SetDefault`, `codec.SetDefault` or OTel globals, so several
applications run in one process. `app.WithProcessGlobals()` lets the logging,
codec and tracing modules install them (for code that reads `slog.Default` or
`codec.Default`). `nhttp` always puts the application's `Registry` in the
request context when it is not the process default, so `Send*`/`Parse*` use
it; spans always go to the application's provider. A raw `Authenticate`
still logs through `slog.Default`.

`crypto` and `helper/*` have no lifecycle and are not modules.

### 3.5 Middleware chain (fixed order, set in `setStandardHandler`)

1. `Tracing()` — only with the tracing module enabled. Must be first.
2. `Logger(logger)` — only when the logger has an output (it is enabled for errors).
3. Drain header (`Connection: close` once `shutdown_delay` started) and, with
   the metrics module, the request metrics middleware (§11C).
   `http.server.max_inflight_requests` (when positive) sheds load here: past
   the limit a request is answered 503 + `Retry-After` at once; a stream gives
   its slot back at `BeginStream`.
4. `middleware.Recoverer` (from go-ams-router) — always, then the
   application's `Registry` in the request context when it is not the process
   default. The router's `NotFound` and `MethodNotAllowed` answer the envelope
   (§7.2), not net/http's text (a root SPA still owns `NotFound`).
5. Body limit — always: `http.MaxBytesReader` at `http.server.max_body_bytes`
   (default: the Registry's payload limit) + 1 byte, so raw routes that read
   `r.Body` are bounded and typed routes still get their 413 from the codec
   first (`ParseStatus` also maps `*http.MaxBytesError` to 413).
6. Handler timeout — **opt-in**: only when `http.server.handler_timeout` is
   positive (default: disabled) the request context gets that deadline, so
   handlers, queries and loaders that honor `ctx` stop. For one route, mount
   `nhttp.Timeout(d)` with `With`. A stream calls `nhttp.BeginStream(w, r)`,
   which releases this global deadline (not a route-level `Timeout`), the
   HTTP/3 stream timeout and the connection read/write deadlines (§7.1).
7. `middleware.Security` (go-ams-router) — unless `http.security.enabled: false` (§7.11).
8. `middleware.CORS` (go-ams-router) — only if `http.cors.enabled` (§7.11).
9. App global middlewares — whatever `Server.Use` adds (application modules call it in Init).

go-ams-router builds its middleware chain lazily on the first request, so
`Use` is valid any time **before the server serves its first request**, even
after routes were registered. Calling `Use` **or registering a route** after
serving starts panics; the module lifecycle makes that impossible for module
code (every Init runs before HTTP starts). For inline routers (`With`/`Group`),
middlewares must be added before routes on that inline router.

Built-in routes registered by `nhttp.New` (and so by the module's Init):
- Docs (`http.docs.enabled`): `GET {path}`, `{path}/swagger.json`,
  `{path}/swagger.yaml`.
- JWKS (jose module enabled and `jose.jwks.enabled`): `GET`/`HEAD`
  `{jose.jwks.path}`; see §7.9.
- Module graph (`http.routes.graph.enabled`): `GET {path}`,
  `GET {path}/mermaid.min.js`; see §7.13.
- Health/readiness (if either is enabled): see §7.6.
- Metrics (`http.routes.metrics.enabled`, needs the metrics module): `GET {path}`, the registry in the Prometheus text format (§11C).

---

## 4. Configuration (`config/`)

### 4.1 Loading pipeline (`LoadFlags(configPath, envFilePath)`)

`config` reads the configuration **once** and knows no module schema: it
returns an immutable `*config.Document` of raw sections. Only the reserved
application sections are typed there: `env`, `app` (`Document.App()`:
`name`, `version`, `internal_use`) and `prd_exceptions`. Every other
top-level key is the section of the module that owns it
(`module.Descriptor.Section`), which decodes it strictly, applies its
defaults and validates it (§3.1). YAML cannot activate a module: a section
no registered module owns is an error of `app.New`, and so is an env file key
nothing consumed (`config.Binding.Finish`); registration is authoritative.

`config.Load()` parses the flags `-config` and `-envfile` (it calls
`flag.Parse()` — define your own flags before calling it) and delegates to
`LoadFlags`. Per section, order of precedence, **later wins**:

1. **Base YAML**: `-config` path, or `./config.yaml` in the **current working
   directory** (at most 8 MiB). A `-config` path that does not exist is an
   error; only the default `./config.yaml` may be absent (then `app.name`
   must come from the env file). **Unknown keys are errors** in every section
   (the owner decodes with `KnownFields`): a typo such as `http.cors.origns`
   would otherwise leave `origins` empty, which means "any origin". A section
   that appears twice is an error. Top-level keys starting with `x-` are
   ignored (room for application data in the same file).
   - `${VAR}` references in any YAML scalar are expanded from the **process
     environment** (an **unset** variable is a load error naming every missing one;
     `${VAR:-default}` supplies a value when VAR is unset or empty, and
     `${VAR:-}` declares it optional). Expansion is per YAML node, not
     on raw text, so values with YAML special characters are safe. For
     unquoted scalars the tag is reset so `port: ${DB_PORT}` still decodes as
     an int; quoted scalars stay strings.
2. **Env file** (`-envfile`, at most 8 MiB, parsed with godotenv but with
   every value taken literally: `$` is an ordinary character, nothing is
   expanded, so a password like `pa$SWORD` is kept intact): each leaf field of
   the owner's schema is set from a key made of its YAML path, upper-cased,
   joined by `_`: `http.server.port` → `HTTP_SERVER_PORT`,
   `database.pool.max_conns` → `DATABASE_POOL_MAX_CONNS`,
   `codec.max_payload_bytes` → `CODEC_MAX_PAYLOAD_BYTES`, `env` → `ENV`,
   `app.name` → `APP_NAME`. Supported: string, ints, uints (overflow
   checked), bool, floats, `time.Duration` (`"5s"`), pointers, `[]string`
   (comma-separated), and any `encoding.TextUnmarshaler` (e.g.
   `codec.ByteSize`). A section only the env file sets is decoded too. Note:
   only the env **file** is read this way — real process env vars are only
   used via `${VAR}` in YAML. A key that matches no field of a registered
   module is an error (`unknown keys ...`), as are lower-case keys. Sequences
   of structs (`jose.keys`, `jose.profiles`) and maps are YAML-only: an env
   key naming one (e.g. `JOSE_KEYS`) is an explicit error; use `${VAR}`
   inside the YAML entries instead. Scalar JOSE leaves work
   (`JOSE_LIMITS_MAX_TOKEN_BYTES`, `JOSE_JWKS_CACHE_MAX_AGE`,
   `HTTP_AUTH_REALM`).
3. **Overlay**: based on the (possibly overridden) `env`, which must be one of
   `local`, `dev`, `stg`, `prd` (case-insensitive; stored lower-case; anything
   else is a load error): `prd` → `config.prd.yaml`, `stg` →
   `config.stg.yaml`, `dev` → `config.dev.yaml`; `local` has none. Path is
   relative to the **CWD**, not to the base config's directory. A missing file
   is skipped; any other `Stat` or read error is a load error. It overrides
   the env file too, and may set `env` (validated again).
4. `app.name` and `app.version` are required (load error).
5. **Defaults and validation** belong to each module's `Configure` (§3.1);
   cross-section rules to its `Check`. The weakening settings
   (`Source.Warn`) are: a TLS private key readable by group or others (http),
   Redis credentials without TLS to a non-local host (cache), a PostgreSQL
   password with `sslmode` disable/allow/prefer (the pgx default) to a
   non-local host (database), HTTPS or HTTP/3 served while the cache's Redis
   or the database does not use TLS (http `Check`; a Unix socket is exempt).
   Outside `prd` they are `Application.Warnings()`; with `env: prd` each one
   is an **error of `app.New`** unless its `prd_exceptions` key is true
   (`tls_key_permissions`, `redis_plaintext`, `database_plaintext`, `message_plaintext`), in which
   case it stays a warning. In `prd`, `http.docs`, `http.routes.health`,
   `http.routes.readiness` or `http.routes.graph` enabled is only a warning
   (unauthenticated endpoints), silenced by `app.internal_use: true`. The app
   logs the warnings at ERROR level in `prd`, because that environment filters
   the log below ERROR. `codec.max_payload_bytes` must be at most 256 MiB.

One `Document` may serve several applications: `Document.Bind()` gives each
its own bookkeeping of consumed sections and keys. The document is
immutable during runtime; there is no hot reload of YAML (credential files
reload through `Application.Reload`, §3.3).

### 4.2 Full schema, defaults and validation

```yaml
env: local                    # local (default) | dev | stg | prd — drives log level and overlay

app:
  name: my-app                # REQUIRED
  version: 1.0.0              # REQUIRED
  internal_use: false         # true = not reachable from the internet; silences the prd warning about docs/health/readiness
  # Document.OnlineSince() is the load time, not configurable

http:
  server:
    host: ""                  # "" = all interfaces
    port: 8080                # OPTIONAL, default 8080 (plain HTTP, tcp); explicit range 1..65535
    header: true              # print the nForge banner (zero value: false)
    tls:                      # HTTPS over tcp; off by default (no silent protocol upgrade)
      enabled: false
      port: 8443              # OPTIONAL, default 8443 (tcp)
      cert_file: ""           # PEM chain; cert_file + key_file required when enabled (also by http3 reuse)
      key_file: ""
      min_version: "1.3"      # "1.3" (default) | "1.2"; never 1.0/1.1. Applies to tcp only: QUIC is always TLS 1.3
      disable_insecure_http: false   # close the plain HTTP listener (port above)
      redirect_http: false    # plain HTTP serves only 308 → https; needs tls.enabled; excludes disable_insecure_http
      redirect_host: ""       # public host for the Location; required unless http.server.host is loopback; never from the request
    http3:                    # HTTP/3 over QUIC (udp); off by default; works without tls.enabled
      enabled: false
      port: 8443              # OPTIONAL, default 8443 (udp); defaulted independently of tls.port
      same_keycert_tls: <true> # *bool; true reuses tls.cert_file/key_file (even with tls.enabled: false); false needs the two below
      cert_file: ""           # only with same_keycert_tls: false (set with true → error)
      key_file: ""
      advertise: <true>       # *bool; Alt-Svc on HTTPS responses (effective only with tls.enabled and a bound QUIC listener)
      max_idle_timeout: 30s   # QUIC connection idle timeout
      max_stream_timeout: 30s # one HTTP/3 request: context deadline + stream read/write deadlines; a stream releases it
      allow_0rtt: false       # reserved: true is rejected at load (0-RTT requests are replayable)
    read_header_timeout: 10s  # http.Server timeouts: 0 = default, never "none"
    read_timeout: 30s         #   (Slowloris); negative → error;
    write_timeout: 30s        #   read_header_timeout <= read_timeout; a stream releases read/write
    idle_timeout: 120s        #   with BeginStream (http/config_server.go, also applied by nhttp.New)
    max_inflight_requests: 0  # sheds load: 503 + Retry-After past N running requests; 0 = unlimited; at most 1000000
    shutdown_delay: 0s        # keep serving (not ready) this long before the listeners close; at most 5m; counts against app.WithShutdownTimeout
    handler_timeout: 0s       # OPT-IN deadline of every request context; omitted or 0s = disabled, positive enables; streams release it
    max_body_bytes: <codec.max_payload_bytes>   # bounds every request body, raw routes too; at most 1gb
  docs:
    enabled: false
    path: /docs               # normalized to "/x" (leading slash, no trailing)
    title: <app.name>
    version: <app.version>
    auth: bearer              # bearer | none (validated) — documented scheme; none + private typed routes panics
    hosts: ["http://{host|localhost}:{port}"]   # 0.0.0.0 / :: / "" → localhost; "https://…:{tls.port}" when http only redirects/is closed
    accent: "#007d9cbf"       # validated hex: #RGB, #RGBA, #RRGGBB, #RRGGBBAA
  routes:
    health:    { enabled: false, path: /health }   # path defaults only when enabled
    readiness: { enabled: false, path: /ready }
    metrics:   { enabled: false, path: /metrics }  # Prometheus text format; needs metrics.Module(); unauthenticated: keep it internal
    graph:     { enabled: false, path: /graph }    # module graph page, §7.13; literal path, also serves {path}/mermaid.min.js
  auth:                       # bearer auth of private typed routes; see §7.9
    default_profile: ""       # jwt profile for IsPrivate routes without AuthProfile; requires jose.enabled
    realm: nforge-api         # WWW-Authenticate realm; printable ASCII, no quotes/backslashes
  tenant:                     # source of Opts.Tenant / server.TenantMiddleware(); see §7.10
    source: claim             # claim (Principal.TenantID) | header (validated)
    header: X-Tenant-ID       # header name (RFC 9110 token), read when source is header
  security:                   # router middleware.Security (helmet); see §7.11; validated only when enabled
    enabled: <true>           # *bool; on unless set to false
    disable: []               # header names not to send, case-insensitive (x-frame-options, …)
    csp: { directives: {}, disable_defaults: false, report_only: false }  # directives YAML only; null removes, [] valueless
    hsts: { max_age: 8760h, include_subdomains: <false>, preload: false }  # include_subdomains is *bool; true pins HTTPS on every subdomain
    frame_options: SAMEORIGIN # SAMEORIGIN | DENY
    referrer_policy: [no-referrer]
    cross_origin_opener_policy: same-origin     # | same-origin-allow-popups | unsafe-none
    cross_origin_resource_policy: same-origin   # | same-site | cross-origin
    cross_origin_embedder_policy: ""            # not sent | require-corp | credentialless | unsafe-none
  cors:                       # router middleware.CORS (Express cors); see §7.11; validated only when enabled
    enabled: false
    origins: []               # exact lowercase origins; ["*"] = any origin. Enabled with no origins and no patterns is an ERROR unless allow_any_origin
    allow_any_origin: false   # the explicit form of origins: ["*"]; excludes origins and origin_patterns
    origin_patterns: []       # regexps, must be anchored ^…$
    methods: [GET, HEAD, PUT, PATCH, POST, DELETE]
    allowed_headers: []       # [] = echo Access-Control-Request-Headers
    exposed_headers: []
    credentials: false        # requires explicit origins/patterns; "*" headers rejected with it
    max_age: 0s               # preflight cache (whole seconds); 0 = not sent
    options_success_status: 204   # 200..299

database:                     # database.Config — see §9
  enabled: false
  driver: postgres            # postgres | postgresql | "" ; anything else → ErrUnsupportedDriver
  dsn: ""                     # wins over the individual fields
  host/port/name/user/password: ...
  ssl: { mode: "" }           # libpq sslmode; "" = pgx default (prefer)
  connection: { connect_timeout: 5s, statement_timeout: 30s }   # PostgreSQL statement_timeout of every connection; a value in the DSN wins
  pool: { min_conns, max_conns, max_conn_lifetime, max_conn_idle_time, health_check_period }
  tenancy:                    # database.TenancyConfig — see §9.1; validated at load only when database.enabled
    enabled: false
    mode: ""                  # rls | column — REQUIRED when enabled
    column: tenant_id         # plain identifier (written into SQL by TenantEq)
    schema: public            # schema of unqualified tables
    setting: app.tenant_id    # rls only; custom GUC "prefix.name"
    tables: []                # "table" | "schema.table"; checked at startup

logging:
  output:
    otel:
      enabled: false          # OTel log export (the logger module); traces are the tracing module
      endpoint: ""            # "host:port" or URL; "" = OTel default localhost:4317
      insecure: false
    terminal:
      enabled: false

tracing:
  otel:
    enabled: <true>           # *bool; nil = true. Registering telemetry.Module is the opt-in; false disables the module
    endpoint: ""
    insecure: false
    sample_ratio: <1>         # *float64, 0..1; nil = 1 (every request); ParentBased(TraceIDRatioBased)
    trust_incoming: <false>   # *bool; true: continue the caller's traceparent, adopt X-Request-Id, keep baggage (behind a gateway)

codec:
  formats: { enabled: [json] }      # json | cbor | xml
  default: json                     # must be one of the enabled formats
  strict_unknown_fields: false
  max_payload_bytes: 1mb            # ByteSize; at most 256mb
  max_elements: 250000              # values per document (JSON tokens, XML elements+attributes): bounds decode cost; 1..100000000
  xml: { disallow_dtd: true }       # *bool; nil = true

jose:                         # jose.Config — see §10 (unknown keys inside jose are rejected)
  enabled: false              # false: nothing is read, the jose module publishes nothing
  limits: { max_token_bytes: 16384, max_header_bytes: 2048, max_payload_bytes: 8192,
            max_json_depth: 16, max_key_file_bytes: 65536, max_profiles: 32, max_keys: 64,
            max_token_lifetime: 720h (≤ 8760h; caps every jwt ttl/max_ttl) }
  keys: [ { kid, algorithm: HS256, operations: <all of the algorithm>,
            secret: ${ENV} | source: { format: pem|jwk|oct, file }, publish: false } ]
  profiles: [ { name, kind, typ, signing_key, signing_algorithm, verification_keys, verification_algorithms,
                encryption_key, encryption_algorithm, decryption_keys, decryption_algorithms,
                content_encryption: A256GCM, jwt: { issuer, audiences, ttl: 15m, max_ttl: 15m, clock_skew: 30s } } ]
  jwks: { enabled: false, path: /.well-known/jwks.json, cache_max_age: 60s }

metrics:                      # metrics.Config — see §11C; the whole section is optional once metrics.Module() is registered
  enabled: <true>             # *bool; omitted = true; false keeps the registered module out of the run

prd_exceptions:               # insecure settings env prd accepts on purpose (default false: load error); see §4.1
  tls_key_permissions: false  # TLS key readable by group or others
  redis_plaintext: false      # Redis without TLS (credentials, or behind HTTPS/HTTP/3)
  database_plaintext: false   # PostgreSQL sslmode that can fall back to clear text
  message_plaintext: false    # RabbitMQ credentials or traffic without TLS

cache:                        # cache.Config — see §11; validated only when enabled, and only the selected driver
  enabled: false              # false: nothing is opened, the cache module publishes nothing
  driver: redis               # redis | memory
  codec: json                 # json | cbor; must also be in codec.formats.enabled
  prefix: ""                  # "" → nforge:<env>:[<tenant>:]<app.name>: ; custom → <prefix>:[<tenant>:]
  limits: { max_key_bytes: 1024 (≤ 64 KiB), max_value_bytes: 1mb (≤ 64 MiB), max_inflight_loads: 1024 }
  load_timeout: 5s            # bounds a shared GetOrLoad loader; there is NO default entry TTL
  redis:
    url: ""                   # redis:// | rediss://; wins for address, credentials, database, TLS; query options rejected
    host: localhost
    port: 6379
    username: "" / password: ""   # optional; no AUTH when both empty
    database: 0
    tls: { enabled: false, server_name: <host>, ca_file: "" }   # TLS 1.2+, hostname verified; no skip-verify
    pool: { size: 32, min_idle_conns: 2, max_idle_conns: 32, max_active_conns: 32,
            max_conn_idle_time: 5m, max_conn_lifetime: 30m }    # min_idle ≤ max_idle ≤ size ≤ max_active
    timeouts: { dial: 1s, read: 1s, write: 1s, pool: 500ms, operation: 2s }
    retry: { read_retries: 1, min_backoff: 50ms, max_backoff: 500ms }   # *int, 0..3; reads only
  memory: { max_entries: 10000, max_bytes: 64mb, cleanup_interval: 30s }  # max_bytes ≥ max_key + max_value

message:                      # message.Config — RabbitMQ only; disabled by default
  enabled: false              # no connection or worker unless enabled
  driver: rabbitmq             # only supported driver
  namespace: nforge            # managed dead-letter exchange prefix
  payload_format: json         # must be in codec.formats.enabled
  rabbitmq:
    url: ${RABBITMQ_URL}       # amqp:// or amqps://; redacted in output
    connection_name: ""       # defaults to app.name
    dial_timeout: 10s
    heartbeat: 30s
    tls: {ca_file: ""}         # optional CA; TLS 1.2+ with hostname verification
    reconnect: {initial_delay: 1s, max_delay: 30s, multiplier: 2, jitter: true}
  publisher: {confirm_timeout: 10s, max_payload_bytes: 1mb} # cannot exceed codec limit
  consumer: {concurrency: 1, prefetch: 0, timeout: 30s, ack_mode: automatic} # 0 prefetch = concurrency
  queues:
    - {name: notifications} # Classic durable
    - {name: payments, max_priority: 10} # Classic priority 1..10
    - {name: audit, type: quorum, dead_letter: true}
    - {name: emails, dead_letter: true, retry: {delay: 10s, max_attempts: 3}}  # max_attempts = total handler invocations before the DLQ
    - {name: ephemeral, on_failure: drop}  # explicit: a failed message is discarded (silences the no-DLQ startup warning); or on_failure: dead_letter (needs dead_letter: true)

scheduler:                    # scheduler.Config — see §11D; the whole section is optional once scheduler.Module() is registered
  enabled: <true>             # *bool; omitted = true; false keeps the registered module out of the run (a module that requires it fails app.New)
  mode: local                 # local (default); distributed is reserved and fails app.New with ErrUnsupportedMode, never a local fallback
  timezone: UTC               # IANA name the expressions are evaluated in; invalid or "Local" fails app.New; default UTC
  max_concurrency: 0          # cap of simultaneous executions of ONE job (a default for every job; WithMaxConcurrency overrides); 0 = unlimited; negative → error; at the cap a firing is skipped

email:                        # email.Config — see §11E; registering email.Module() is the opt-in; from.address and smtp.host are required
  enabled: <true>             # *bool; omitted = true; false keeps the registered module out of the run (a module that requires it fails app.New)
  driver: smtp                # smtp (default) | noop (tests: validates, encodes and discards; needs no smtp section); anything else → ErrUnsupportedDriver
  timeout: 15s                # bounds one whole Send (dial, TLS, AUTH, envelope, DATA); 0 = default; at most 5m
  from:
    address: no-reply@example.com   # REQUIRED; bare ASCII mailbox (no display name); the envelope sender and the From header
    name: Example Application       # optional display name, any Unicode text without control characters, at most 128 bytes
  smtp:
    host: smtp.example.com    # REQUIRED; host name or IP only (no scheme, port or path); also the certificate's verified name
    port: 587                 # default 587 for starttls, 465 for tls
    username: ${SMTP_USERNAME}   # username and password together or both empty; empty = no AUTH (relay trusted by network)
    password: ${SMTP_PASSWORD}   # redacted in every fmt verb, slog and JSON
    security: starttls        # starttls (default) | tls (implicit TLS); no plaintext, no skip-verify

sse:                          # sse.Config — see §11B; the whole section is optional once sse.Module() is registered
  enabled: <true>             # *bool; omitted = true; false keeps the registered module out of the run
  driver: memory              # memory | rabbitmq; rabbitmq requires message.Module() enabled
  heartbeat: 15s              # ": heartbeat" comment frames; 1s..1h
  retry: 3s                   # "retry:" advertised to browsers; 1s..1h
  buffer_size: 64             # bounded queue per connection; a client that fills it is disconnected
  max_connections: 10000
  max_connections_per_user: 5 # <= max_connections
  max_connects_per_second: 100   # token bucket for connection attempts of this instance
  write_timeout: 10s          # deadline of every frame write
  max_lifetime: 15m           # stream age limit (+10% jitter); >= 1m; bounds token expiry/revocation lag
  send_buffer_bytes: 0        # cap on SO_SNDBUF of HTTP/1.1 streams so a client that never reads is cut; 0 = OS default, else 4096..16mb
  limits: { max_payload_bytes: 64kb (≤ 1mb), max_user_id_bytes: 512, max_event_name_size: 64, max_event_id_bytes: 128 }
  allowed_origins: []         # exact lowercase origins; [] = only the request's own origin; "*" rejected
  rabbitmq:
    exchange: ""              # "" → <message.namespace>.sse.<env>.<app.name> (sanitized)
    prefetch: 64              # unacknowledged fanout messages of this instance's queue
    routing: fanout           # fanout (every instance gets every event) | user (a direct exchange: an instance binds only the users it serves)
```

Owners of the sections: `logging` → `logs.Module` (ID `logger`), `tracing` →
`telemetry.Module`, `codec` → `codec.Module`, `database` →
`database.Module`, `jose` → `jose.Module`, `cache` → `cache.Module`,
`message` → `message.Module`, `scheduler` → `scheduler.Module`, `email` → `email.Module`, `sse` → `sse.Module`, `metrics` → `metrics.Module`, `http`
→ `nhttp.Module`; `env`, `app` and `prd_exceptions` are reserved (package
`config`). A section of a module the application did not register is an
error, not ignored.

`message` owns the RabbitMQ connection and requires `codec.Capability`; queue
declarations from YAML and `message.WithQueues` merge by name and must be
identical when duplicated. The separate `message.Consumers()` ingress starts
after all module Init hooks and drains before `message.Module()` closes the
connection. RabbitMQ confirm publishing is at-least-once; no exactly-once
guarantee exists. Queue type and arguments cannot be migrated automatically.
The message module warns on plaintext remote credentials, and HTTP adds the
same `message_plaintext` warning when HTTPS/HTTP3 fronts a non-TLS RabbitMQ
connection.

`jose` defaults and structural validation are `jose.ApplyDefaults` /
`jose.ValidateConfig` (applied only when enabled); key files are read later,
by `jose.New` in the module's Init. The http module's `Check` validates
`http.auth.default_profile` (a consuming jwt profile; it makes the jose module
a required dependency, so jose disabled is `ErrDisabledDependency`). `cache`
defaults come from `cache.ApplyDefaults` (always applied; zero caps/timeouts
take the default, `read_retries` is a `*int` so an explicit `0` disables
retries); `cache.ValidateConfig` runs when enabled, and the cache module's
`Check` requires `cache.codec` in `codec.formats.enabled` and
`max_value_bytes` ≤ `codec.max_payload_bytes`. The database module validates
the connection fields, the pool values and `database.tenancy` when enabled,
before any dial (`database.Config.Validate`). The CA file is read only by
`cache.Open`, and only for an enabled Redis TLS.

Booleans have **no implicit `true` defaults** except the `*bool` fields
(`tracing.otel.enabled`, `http.security.enabled`, `codec.xml.disallow_dtd`,
`http.security.hsts.include_subdomains`, whose default is `false`). Map fields
(`http.security.csp.directives`) cannot come from the env file (explicit
error). In particular
`http.routes.health.enabled`, `readiness.enabled`, `http.server.header` and
`http.docs.enabled` are `false` unless set.

Helpers: `logs.Settings.Enabled()` (terminal or OTel output on),
`telemetry.OtelSettings.IsEnabled()` (`tracing.otel.enabled != false`).

### 4.3 `ByteSize`

`codec.ByteSize` (int64) parses `"5mb"`, `"512kb"`, `"1.5gb"`, `"100b"`,
`"1048576"`, `"5 MiB"`; base 1024, case-insensitive. Implements
`UnmarshalText` and `UnmarshalYAML`. Constants `B, KB, MB, GB`. It is the
only implementation (`codec.ParseByteSize`); `cache.ByteSize` is an alias.
Whole numbers are exact (no float64 rounding above 2^53) and a value over
`math.MaxInt64` is an error, never a negative number.

### 4.4 Adding a config field — checklist

1. Add the field to the owning module's section type (`nhttp.Config`,
   `database.Config`, `cache.Config`, `jose.Config`, `codec.Settings`,
   `logs.Settings`, `telemetry.Settings`) with a `yaml:"snake_case"` tag and
   a trailing comment stating values and default.
2. If it needs a default, add it to the module's defaults (`ApplyDefaults`).
   Use a pointer (`*bool`) when "unset" must differ from the zero value.
3. Validate in the module's `Configure` (its section alone) or `Check` (it
   involves another module) if invalid values must stop startup. No I/O there.
4. The env-file key is derived automatically from the YAML path; make sure the
   type is supported by `setFieldValue` (or implement `TextUnmarshaler`).
5. Add tests next to the module (through `internal/apptest`), document the key
   in §4.2 (`docs_config_test.go` fails otherwise) and update the
   `config.yaml` of the example that covers the module (§14).

---

## 5. Logging (`logs/`)

`logs.Module()` (ID `logger`, section `logging`) publishes the application
logger under `module.Logger`, which every other module declares as optional.
With no output enabled it publishes a discarding logger (and no request log
middleware runs). It is a dependency of nearly everything, so it stops last
and flushes the OTel exporter. `logs.Setup(ctx, settings, app, opts...)`
returns `(*slog.Logger, flush, error)`; only with `logs.WithProcessGlobals()`
(the module passes it under `app.WithProcessGlobals`) does it call
`slog.SetDefault` and route the OTel error handler.

Handler stack:
```
contextHandler            // adds request_id from go-ams-router middleware.GetRequestID(ctx)
└── multiHandler          // fan-out; one failing handler doesn't block the others; records are cloned
    ├── leveledHandler(tint terminal, level = env level)       // if terminal.enabled
    └── leveledHandler(otelslog, level = min(env level, INFO)) // if otel.enabled
```

- Level by `env`: `local` → DEBUG, `dev` → INFO, `stg` → WARN, `prd` → ERROR,
  anything else → DEBUG. The match is **case-sensitive** (use lowercase env).
- The OTel handler never filters above INFO, so INFO logs always reach the
  collector even in `stg`/`prd`.
- Terminal output: tint, RFC 3339 time, `AddSource` only while the level is DEBUG
  (`env: local`; the OTel handler likewise): in stg/prd the file and line of every
  record are noise and publish build paths to the log backend.
- OTel: OTLP/gRPC exporter with a batch processor and the shared resource
  (§6). Endpoint containing `://` → `WithEndpointURL` (http:// implies
  insecure), else `WithEndpoint`.
- With process globals, OTel internal errors are routed via
  `otel.SetErrorHandler` to the **terminal only**, as WARN, to avoid feeding a
  failing exporter; without them they go to OTel's default handler.
- `leveledHandler.WithAttrs/WithGroup` re-wrap the handler — keep this when
  editing, or `logger.With(...)` loses its level filter. Same for
  `contextHandler` and `multiHandler`.
- `logs.NewNoop()` returns a discarding logger.

Always log with the context-aware methods (`InfoContext`, `ErrorContext`,
`LogAttrs(ctx, ...)`) inside requests so `request_id` and trace correlation
are attached. Module log messages use a bracketed prefix:
`[LoggerModule]`, `[TracingModule]`, `[CodecModule]`, `[DatabaseModule]`,
`[JOSEModule]`, `[CacheModule]`, `[SchedulerModule]`, `[EmailModule]`, `[HTTPModule]`, `[App]`, `[Config]`; fatal
startup and runtime errors use `[Fatal ERROR] ...`.

---

## 6. Telemetry (`telemetry/`)

- `NewResource` — shared by logs and traces so both reach Grafana with the same
  `service.name` (app.name), `service.version`, `deployment.environment` (env),
  host name, process pid/executable/runtime, plus `OTEL_RESOURCE_ATTRIBUTES`.
  **`WithProcess()` is deliberately not used**: it includes command-line
  args, which may leak secrets passed via flags. Do not add it.
- `telemetry.Module()` (ID `tracing`, section `tracing`) builds a
  `TracerProvider` (`NewTracerProvider`: OTLP/gRPC batch exporter, the shared
  resource, the custom `idGenerator`, the configured sampler) and publishes
  `*telemetry.Tracing{Provider, TrustIncoming}` under `telemetry.Capability`;
  nhttp's tracing middleware uses that provider. Registering it is the opt-in;
  `tracing.otel.enabled: false` disables it (no middleware, no request_id).
  With process globals it also installs the provider and the W3C propagator
  (`Propagator()`: traceparent + baggage) as the OTel globals. Its Stop
  flushes and shuts the provider down.
- `idGenerator` — for root spans it uses the trace ID stored in the context by
  `ContextWithTraceID` (falls back to random). `ParseTraceID` accepts 32 hex
  chars or a UUID (hyphens stripped); all-zero is rejected.

### 6.1 request_id == trace_id (HTTP `Tracing()` middleware)

By default the caller's trace context is **not trusted**: the
`traceparent`, `tracestate`, `baggage` and `X-Request-Id` of the request are
ignored, every request starts its own trace, and a client can neither force
sampling, nor reuse another request's trace id, nor inject baggage downstream.
Services behind a gateway (or calling each other) set
`tracing.otel.trust_incoming: true` (`WithTrustedIncomingContext()` on the raw
middleware) to link their traces.
`tracing.otel.sample_ratio` (default 1) records that fraction of traces by
trace id; with trust on, a sampled parent is still honored and a client that
picks the trace id also picks the sampling decision.

The chain is `traceIDFromRequestID → otelhttp → requestIDFromTrace → handler`:

1. With `trust_incoming`, if the incoming `X-Request-Id` looks like a trace ID (32 hex / UUID), it is
   put in the context so the root span gets that trace ID.
2. `otelhttp` creates the span (continuing an incoming `traceparent` if trusted —
   in that case the parent's trace ID wins).
3. `X-Request-Id` is overwritten with the span's trace ID, set as the
   `request_id` span attribute, and go-ams-router's `middleware.RequestID`
   stores it in the context and the response header. If the client-sent ID
   did not become the trace ID, it is kept as `request_id.original` — only
   when it has the router's request-ID shape (≤ 64 chars of
   `[A-Za-z0-9._-]`); anything else is not copied into the span.
4. The router creates its route context before the root middlewares, so
   after the handler the span is renamed `"<METHOD> <pattern>"` with
   `http.route` (full pattern across sub-routers, low cardinality).

**Important:** the `RequestID` middleware only runs as part of `Tracing()`.
With tracing disabled there is no `request_id` in the context/logs and no
`X-Request-Id` response header unless the app adds
`middleware.RequestID` itself as a global middleware.

---

## 7. HTTP (`http/`, package `nhttp`)

### 7.1 `Server` and `Module`

`nhttp.Module()` owns the `http` section and is an **ingress** module: its
Init builds the `Server` (router, chain, built-in routes, the banner with
`http.server.header`) and publishes it under `nhttp.Capability`; its Start
binds and serves after every other module started; its Stop drains and
closes first (§3.4); its Reload re-reads the TLS certificates. It requires
the codec module (and jose with `http.auth.default_profile`); logger,
tracing, jose, database and cache are optional (the last two only for the TLS
posture checks of `Check`). It never handles signals.

```go
type Server struct {
    Srv         *http.Server  // Srv.Handler is the go-ams-router Router shared by every transport
    TypedRoutes []TypedRoute  // source of the OpenAPI spec
    // unexported: cfg, app, logger, codec, jose, tracing, status, failed, listeners
}
```
Methods: `Router()`, `Typed()`, `Use(mw...)` (global middlewares),
`AddDocRoute`, `TenantMiddleware(opts...)` (§7.10), `Config()` (the effective
section), and the lifecycle the module calls: `Start(ctx)` (binds every
transport and returns once they are bound), `Stop(ctx)` (drain, close, wait;
once), `Reload(ctx)`. `nhttp.New(cfg, nhttp.Dependencies{App, Logger,
AccessLog, Codec, JOSE, Tracing, Status, Failed})` builds a server outside an
application (tests, embedding). `Srv` stays the application's plain HTTP
`*http.Server` (never started when `disable_insecure_http` or
`redirect_http` replaces it).

`New` sets `Srv.ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout` and
`IdleTimeout` from `http.server` (defaults 10s/30s/30s/120s). It applies the
defaults (`Config.ApplyDefaults`, `ApplyServerDefaults`) to a copy of the
section, so a `Config` built by hand also gets them. A handler that streams
or runs longer than `write_timeout` calls `nhttp.BeginStream(w, r)` (below) or
extends its own deadline with
`http.NewResponseController(w).SetWriteDeadline(...)`; `Srv` stays exported
for anything else, as long as it is changed before `Start`.

**Request deadlines and streams.** `handler_timeout` is **opt-in** (omitted or
`0s` = no request deadline; the transport timeouts above are not affected and
keep their defaults, so Slowloris protection stays). `nhttp.Timeout(d)` is the
same deadline for one route (`router.With(nhttp.Timeout(5*time.Second))`).
`nhttp.BeginStream(w, r)` is for long-lived responses: it releases the global
handler timeout, the HTTP/3 `max_stream_timeout` (context and stream
deadlines) and the TCP read/write deadlines of that one request, so the stream
is not cut at 30 s; the caller then sets a short write deadline before every
write. A route-level `Timeout` is the application's explicit opt-in and is not
released. The released deadlines are guards that stop their timer
(`deadlineCtx`), not a changed config: other requests keep them. `deadlineCtx` is
its own `context.Context` (not an embedded stdlib cancel context), so contexts
derived from the request (database drivers, HTTP clients) observe
`context.DeadlineExceeded` when it fires, exactly as with `context.WithTimeout`.

**Context errors in typed handlers.** A handler returning a context error is
not a server fault: `context.DeadlineExceeded` (the server's `handler_timeout`, a
route `Timeout`, a downstream deadline) answers **504** logged at WARN; a
`context.Canceled` while the request context is done (the client went away)
answers **499** (nginx's "client closed request", `nhttp.StatusClientClosedRequest`)
at DEBUG and the access log records it at INFO. Everything else stays 500 at ERROR.

**Rate and concurrency limits.** `nhttp.RateLimit(limit, window, opts...)` is a
fixed-window limiter per client key (`KeyByRemoteIP` by default, `KeyByPrincipal`,
`KeyByHeader` only for headers a trusted proxy sets): 429 with `Retry-After` and
`RateLimit-Limit/Remaining/Reset` headers, in-process memory by default or a
`*cache.Cache` (`WithRateLimitCache`, shared across instances on Redis, built on
`IncrementWithTTL`), fail-open on backend errors unless `WithRateLimitFailClosed`.
`http.server.max_inflight_requests` sheds load with 503 (see §3.5).

### 7.2 Response envelope

Every response written by nForge helpers has this shape (JSON shown; XML root
is `<response>`, CBOR uses the same keys):

```json
{ "success": true,  "status": 201, "data": { ... } }
{ "success": false, "status": 404,
  "error": { "title": "404 ERROR NOT FOUND", "detail": "user not found", "instance": "/api/v1/users/9" } }
```

- `SendJSON / SendXML / SendCBOR(w, r, status, message)` — `status < 400` →
  `data`; `>= 400` → `error.detail` (message may be a string or any value),
  `title = ErrorTitle(status)`, `instance = r.URL.Path`.
- `Envelope(r, status, msg)` builds the `SuccessResponse`; `ErrorTitle(404)` →
  `"404 ERROR NOT FOUND"`.
- Unmatched paths and methods answer the same envelope (`404 route not found`,
  `405 method not allowed` with `Allow`) in the default codec, not net/http's
  text; errors net/http produces before the router (431, malformed requests)
  stay plain. `499` titles as "Client Closed Request".
- Encoding happens **before** writing; on encode error nothing was sent and
  the caller can still respond with an error.
- RFC 9110: 1xx/204/304 get headers only, no body.
- `Content-Type` adds `; charset=utf-8` for JSON and XML only.

### 7.3 Plain (untyped) request parsing

`ParseJSON / ParseXML / ParseCBOR(r, &v)` decode the body through
`codec.Default()` (payload limit, strict mode, required fields, DTD block,
validation). Content-Type rules: a request **with a body** and no
Content-Type is refused (415 `missing content-type`; a cross-site page can send
an untyped body without a CORS preflight); a request without a body needs none;
otherwise it must be the format's type, a structured suffix (`application/vnd.x+json`, RFC 6839), or
`text/xml` for XML; parameters ignored. Map errors with `ParseStatus(err)`:

| Error | Status |
|---|---|
| `ErrUnsupportedMediaType`, `codec.ErrUnsupportedFormat` | 415 |
| `codec.ErrPayloadTooLarge` | 413 |
| `codec.ErrValidation` (Validator / Config.Validate) | 422 |
| anything else (syntax, unknown/missing field, type mismatch, …) | 400 |

Idiom:
```go
var u User
if err := nhttp.ParseJSON(r, &u); err != nil {
    _ = nhttp.SendJSON(w, r, nhttp.ParseStatus(err), err.Error())
    return
}
```

### 7.4 Typed routes (the main way to build APIs)

Prefer the generic functions: the compiler checks the types and the handler returns
instead of writing.

```go
nhttp.Get(api, "/users", func(r *http.Request, q ListUsersQuery) ([]User, error) { ... }, nhttp.Opts{DocTag: "users"})
nhttp.Post(api, "/users", func(r *http.Request, u User) (User, error) { ... }, nhttp.Opts{Status: http.StatusCreated})
nhttp.Delete(api, "/users/{id}", func(r *http.Request, _ nhttp.Empty) (nhttp.Empty, error) { ... })
return User{}, nhttp.Error(http.StatusNotFound, "user not found")   // its message reaches the client
```

`Get`, `Post`, `Put`, `Patch` and `Delete` are thin wrappers over the flow below, so
everything in it applies. `nhttp.Empty` means no request or no response; an `Empty`
response answers 204. `Opts.Status` sets the success status (2xx, else a panic). A
`*StatusError` of 400 or more becomes that response; any other error is a 500
`internal error` and the real error goes to the log. `r` gives the context, path
parameters and headers; a handler that must write the response itself (raw bytes, a
stream) uses `TGet` and friends, which stay supported:

```go
server.Typed().Route("/api/v1", func(api *nhttp.TypedRouter) {
    api.TGet("/users", ListUsersQuery{}, []User{}, listUsers, nhttp.Opts{DocTag: "users", Description: "List users"})
    api.TGet("/users/{id}", nil, User{}, getUser)
    api.TPost("/users", User{}, User{}, createUser, nhttp.Opts{IsPrivate: true, RequiredScopes: []string{"users:write"}})
    api.TDelete("/users/{id}", nil, nil, deleteUser)
})
```

Signature: `TXxx(pattern, request, response any, h http.HandlerFunc, opts ...Opts)`.
`request`/`response` are **sample values** (usually zero values) whose type is
what matters; pointers are dereferenced; `nil` means "none".

`Opts`: `Codec` (`"json"` default, `"xml"`, `"cbor"` or content type),
`IsPrivate` (**enforced** bearer JWT authentication, see §7.9, and documented
with bearer security + 400/401), `AuthProfile` (jose jwt profile; default
`http.auth.default_profile`), `RequiredScopes` (all required, 403 otherwise),
`DocTag`, `Headers` (documented as required header params), `Description`
(docs summary).

**Registration time (panics = programmer errors caught at startup):**
- the route codec must be enabled in the Registry (`mustFormat`);
- GET request type must be a struct of query-supported types with valid
  `default` tags (`query.Check`);
- body/response types must have consistent codec tags (`mustValidTags`);
- private routes (`routeAuth`): `AuthProfile`/`RequiredScopes` without
  `IsPrivate`, no jose service (jose module absent or disabled), no resolvable profile, unknown / non-JWT /
  non-consuming profile, invalid scopes, or `http.docs.enabled` with
  `http.docs.auth: none` all panic.

**Request flow (`wrap`):**
0. Private route: authentication (§7.9) runs **before** any decoding, with
   errors in the route codec; on failure the handler is not called.
1. If a request type was given: GET → decode query string (§7.5) and run
   `codec.Default().Validate`; other methods → `parse(r, routeCodec, ptr)`
   (Content-Type check + decode + validation). Failure → envelope with
   `ParseStatus(err)` and `err.Error()` as detail, in the route codec; logged
   as WARN `"invalid request"`. The handler is **not** called.
2. The decoded pointer is stored in the context. Read it with
   `nhttp.Body[T](r)` (non-GET) or `nhttp.Query[T](r)` (GET); `T` may be the
   type or a pointer to it. Wrong `T` / outside a typed route → zero value.
3. The handler receives a **`capture.Writer`**, not the real writer. `Send*`
   calls are *captured* (status + message), not written. Raw `Write` bytes
   are buffered and decoded in the route codec. Headers go straight through.
4. After the handler: status defaults to 200. For success statuses with a
   body (< 400, not 204/304) and a declared response type, the message is
   **conformed** to the response type: same type / pointer → used directly;
   anything else → encoded and decoded in the route codec (type-checked). Then
   `codec.CheckRequired` (required fields must not be nil/empty) and
   validation run. Any failure is a handler bug → **500
   `"failed to build the response"`**, logged as ERROR.
5. The final envelope is written **in the route's codec**, regardless of which
   `Send*` variant the handler called (`SendJSON` inside an XML route produces
   XML).

Consequences to remember:
- A typed handler that sends nothing returns `200` with no `data`; if a
  response type is declared, that is a 500 ("empty response").
- Error responses (>= 400) are never validated against the response type.
- `w.WriteHeader(204)` with no body is the right way for "no content".
- `capture.Writer` implements `Unwrap()`; middlewares that wrap the writer must
  also implement `Unwrap() http.ResponseWriter` so `Send*` can find it
  (`capture.Find` walks the chain).

`TypedRouter` embeds `router.Router`, so plain methods (`Get`, `Post`, `Use`,
…) remain available. Use the **TypedRouter's** `Route`, `Group` and `With` so
documented paths include the prefix. `AddDocRoute(TypedRoute{...})` documents
a non-typed route (handler ignored; for GET the `Schema` becomes query params).
On `AddDocRoute`, `IsPrivate` is **documentation only**: the raw handler must
mount `Authenticate` itself.

Path params: `router.URLParam(r, "id")` (go-ams-router). The router's typed
params `{id:int}` (`[0-9]+`) and `{id:uuid}` are shown as `{id}` in the spec
and documented as `integer` (minimum 0) / `string` `format: uuid`; untyped
params are strings. Values are always strings at runtime. chi regexp params
(`{id:[0-9]+}`) are **not supported**: the router panics at registration.

### 7.5 Typed query strings (`internal/query`)

```go
type ListUsers struct {
    Name   string    `query:"name"`                   // optional
    Page   int       `query:"page" default:"1"`       // default if absent
    Limit  int       `query:"limit" codec:"required"` // 400 if absent
    Active *bool     `query:"active"`                 // nil if absent
    Tags   []string  `query:"tag"`                    // ?tag=a&tag=b or ?tag=a,b
    Since  time.Time `query:"since"`                  // via TextUnmarshaler (RFC 3339)
    Wait   time.Duration `query:"wait"`               // "1m30s"
}
func (q *ListUsers) Validate() error { ... }         // → 422 on failure
```

- Name: `query` tag, else `json` tag, else Go field name; `"-"` skips.
  Anonymous embedded structs are flattened.
- Empty value (`?name=`) counts as absent. Default wins over required.
- A scalar parameter sent more than once (`?page=1&page=2`) is a 400
  (`query.RepeatedError`): a proxy may read another occurrence than the first,
  so it is refused, not resolved. Slices take repetition.
- Supported: scalars, pointers, slices of scalars, `time.Duration`, any
  `encoding.TextUnmarshaler`.
- `codec.strict_unknown_fields: true` also rejects unknown query parameters.
- Errors reuse codec types (`MissingFieldError`, `TypeError`,
  `UnknownFieldError` with `Format: "query"`) → 400.

### 7.6 Built-in health and readiness

`New` registers each one only when enabled (typed routes, tag
`maintenance`):
- `GET {health.path}` → 200 `{"status":"ok"}` (liveness: never probes anything).
- `GET {readiness.path}` → the application's readiness (§3.3), from
  `Dependencies.Status` (the module's `Scope.Introspect()`), within the
  request ctx + 2 s (`readinessTimeout`):
  - ready → 200 `{"status":"ok","online_since":"<RFC3339>","modules":{"<id>":"ready",...}}`
    with every enabled module (disabled ones are omitted);
  - not started yet, stopping, a failed module or a critical module not ready
    → **503**, the same body as `error.detail` with `"status":"unavailable"`
    and the `not_ready` modules, each logged as WARN with its ID only.
  - The probes are shared and reused for 1 s (`app.WithProbeInterval`), so a
    recovered Redis or PostgreSQL is ready again within a second, and a flood
    of probes costs one ping per dependency.
  - A server built with `New` without `Status` answers 200 with no `modules`.

### 7.7 Middlewares provided

- `Tracing()` — §6.1.
- `Logger(*slog.Logger)` — one `"request"` log per request with method, path,
  `pattern` (matched route, `""` on 404), status, bytes, duration, remote;
  level INFO / WARN (4xx) / ERROR (5xx). `statusWriter` implements `Unwrap`
  for `http.ResponseController`. Kept instead of the router's
  `middleware.SlogLogger`, which always logs at INFO (so 5xx would be
  filtered under stg/prd levels) and adds its own `request_id`, duplicating
  the one `logs`' context handler injects.
- go-ams-router `middleware.Recoverer`, `RequestID`, `Timeout`,
  `TimeoutWithResponse`, `StripSlashes`, `CleanPath`, `CORS`, `Security`,
  `CSPNonce`, `Logger`, `SlogLogger` are also available to apps.

### 7.8 OpenAPI / docs (`routes.go`, `internal/openapi`)

- Spec is built from `Server.TypedRoutes` and **cached per route count**
  (`speccache.go`): routes registered after server creation are included
  (the count changes), a repeat request costs a map lookup, and the response
  carries an `ETag` and answers `If-None-Match` with 304. The docs page, the
  spec and `/ready` are **public** (a browser cannot send a bearer token on a
  navigation) and list every route, auth profile and scope: keep
  `http.docs.enabled` off in production or restrict the path at the gateway
  (`env: prd` with docs, health or readiness enabled warns unless `app.internal_use: true`).
- OpenAPI **3.1.0**. Schemas use JSON names; where XML differs (name,
  attribute, root `XMLName`) an `xml` object is added. Named structs go to
  `components.schemas` (unique names, recursion-safe); success responses get
  a named envelope component per response type (`User` → `UserEnvelope`,
  `[]User` → `UserListEnvelope`, none → `SuccessEnvelope`).
- Each operation documents: path/header/query params, request body (non-GET),
  `200` success envelope, and error envelopes `400/422` (if request), `413/415`
  (body routes), `400`/`401` (`IsPrivate` with auth), `403` (with `RequiredScopes`), `500`.
- `auth: bearer` → `bearerAuth` HTTP bearer (JWT) scheme; public routes get an
  explicit empty `security`. `auth: none` → no security at all (and panics
  at registration if there are private typed routes while docs are enabled).
- Private operations also document `400` (malformed Authorization), `401`,
  `403` when `RequiredScopes` is set, and the extensions `x-auth-profile` and
  `x-required-scopes` (bearer schemes cannot carry scopes; no OAuth scheme
  is generated).
- Type mapping: `time.Time` → date-time string; `time.Duration` → int64 ns
  (string `"1m30s"` in query); `[]byte` → base64 string; maps →
  `additionalProperties`; opaque types (TextMarshaler etc.) → string;
  interfaces → any.
- `docs.html` is an embedded `html/template`; `Accent` is injected as
  `template.CSS`, which is safe only because `accent` is validated as hex.
  Keep that validation if you touch either side.
- Every render puts a fresh nonce (base64url: `html/template` escapes `+`
  in attributes) on the page's `<script>` tags and returns the matching
  policy (`pageCSP`: `script-src` is the nonce alone, jsDelivr only for the
  stylesheet, Google Fonts, `connect-src` 'self' + the `docs.hosts` origins).
  Swagger UI comes from jsDelivr **pinned to one exact version
  (`swaggerUIVersion`) with Subresource Integrity** on the script and the
  stylesheet; to update it change the version and both sha384 hashes in
  `docs.html` together (cross-check the files against the npm tarball) and the
  tests will tell you if they drift. The docs handler sends it only when
  `http.security` sends a CSP, under the same header name (report-only
  respected), replacing the API policy whose `script-src 'self'` would
  block Swagger UI. Keep `pageCSP` in sync with what `docs.html` loads.
  `validatorUrl: null` keeps Swagger UI from sending the spec URL to
  swagger.io (and from needing it in `img-src`).

### 7.9 Authentication, scopes and JWKS (`auth.go`, `internal/authn`, `internal/jwks`)

```go
nhttp.Authenticate(dps.JOSE, "access", nhttp.WithScopes("wallet:read", "wallet:write"))  // raw middleware
nhttp.Authenticate(dps.JOSE, "access", nhttp.WithScopes(required...))                     // same, slice form
nhttp.RequireScopes("a", "b")                     // after Authenticate; same scope check
p, ok := nhttp.PrincipalFromContext(r.Context())   // Subject, Issuer, Audience, TokenID, TenantID, Roles, Scopes, ExpiresAt (the token's exp)
server.Authenticate("access", opts...)             // same middleware bound to the server's logger (the free function logs through slog.Default)
nhttp.WithScopeClaims("scope", "scp")              // option: also read scopes from array/space-separated claims such as Azure AD's scp
p.HasScopes("a", "b")                             // every scope, exact and case-sensitive
```

- `Authenticate(service, profile, opts...)` accepts only `jwt_signed` /
  `jwt_nested` profiles that can validate. Options: `WithRealm` (default
  `nforge-api`), `WithScopes` (**every** scope required, exact and
  case-sensitive; variadic or `slice...`; values copied; several options
  accumulate; `WithScopes()` = no requirement), `WithPrincipalMapper`.
  Invalid arguments (nil service, unknown/non-JWT profile, invalid scope or
  realm) **panic at construction**. Raw middleware errors use the default
  codec; private typed routes use the route codec and `http.auth.realm`.
- Bearer transport: exactly one `Authorization` header, case-insensitive
  `Bearer` (the scheme is the token up to the first space or tab, so
  `Bearerx abc` is another scheme: 401), one or more spaces, one token. Never
  query/cookie/body.
- Results (static details, `WWW-Authenticate` challenge; no token data):

  | Condition | Status | Detail / challenge |
  |---|---|---|
  | no Authorization, or another scheme | 401 | `authentication required`, `Bearer realm="…"` |
  | duplicate header, empty, tab, comma, trailing space, extra field | 400 | `invalid authentication request`, `error="invalid_request"` |
  | invalid/expired/wrong-profile/oversized token, rejected claims | 401 | `invalid token`, `error="invalid_token"` |
  | missing scope | 403 | `insufficient permissions`, `error="insufficient_scope", scope="…"` |
  | validator/mapper unavailable, service closed, ctx done | 503 | `authentication unavailable` (no challenge) |
  | internal failure, Authenticate applied twice | 500 | `internal error` |

- Mapping: the token is validated with a normalized custom-claims map
  (`json.Number`); the default mapper reads optional `tenant_id` (string),
  `roles` ([]string) and `scope` (space-separated). Wrong types → 401. Mapper
  output is validated (scope syntax RFC 6749, no control chars); `Issuer`,
  `Subject`, `Audience`, `TokenID` are **always** overwritten from the
  registered claims. Only the validated identity is stored (private context
  key, defensive copies); the raw token never is. `TenantID` never comes
  from headers; roles grant nothing by themselves.
- Logging: 400/401/403 at DEBUG with a bounded reason code, 503 WARN, 500
  ERROR — never the token, claims or upstream errors.
- JWKS (`registerJWKS`): `GET`/`HEAD` at `jose.jwks.path`, raw
  `application/jwk-set+json` (no envelope, no typed capture), strong `ETag`
  (sha256 of the bytes; changes with the published keys), `If-None-Match` →
  304, `Cache-Control: public, max-age=<cache_max_age>`, `nosniff`; closed
  service → 503. A path equal to a docs/health/readiness path panics.

### 7.10 Tenant middlewares (`tenant.go`)

The tenant lives in the request context under the `database` package key
(`database.WithTenant` / `database.TenantFromContext`), so handlers call
`db.ForContext(r.Context())` directly. JWT validation itself is
`Authenticate` (§7.9); the tenant middlewares only read its `Principal`.

```go
r.With(nhttp.Authenticate(dps.JOSE, "access"), nhttp.Tenant()).Get("/x", h)   // JWT tenant_id claim (default)
nhttp.Tenant(nhttp.WithTenantResolver(nhttp.TenantFromHeader("X-Tenant-ID")))  // header
nhttp.Tenant(nhttp.WithTenantOptional())          // let tenant-less requests through
nhttp.SetTenant(func(r *http.Request) (string, error) { ... })  // custom source; always overrides
server.TenantMiddleware()                         // Tenant configured from http.tenant
api.TGet("/orders", nil, []Order{}, h, nhttp.Opts{IsPrivate: true, Tenant: true})
id, ok := nhttp.TenantID(r); r, err = nhttp.WithTenantID(r, id)
```

- `Tenant` **keeps** a tenant already in the context (set by an earlier
  middleware) and otherwise resolves one; `SetTenant` always resolves and
  **replaces**. Both validate the ID as a canonical RFC 9562 UUID, version
  1 to 8 (v4 and time-ordered v7 both work; never the nil or max UUID), stored
  lowercased.
- Results: missing tenant (or resolver returns `database.ErrNoTenant`) → 403
  `tenant required`; invalid ID or repeated header → 400 `invalid tenant`;
  claim resolver without a `Principal` → 401 with a Bearer challenge; any
  other resolver error → 500 `internal error` (logged, never sent). 400/403
  carry no `WWW-Authenticate`. The tenant value is never logged.
- `Opts.Tenant` on typed routes runs **after authentication and before
  decoding**, answers in the route codec and uses `http.tenant`. With
  `source: claim` it requires `IsPrivate` (registration panic otherwise);
  with `source: header` it also works on public routes and the header is
  documented as a required `uuid` parameter. Tenant routes document 400/403.
- A header is client-controlled: unless a trusted gateway sets it, the app
  must still verify that the caller belongs to that tenant.

### 7.11 Security headers and CORS (`security.go`)

Both are go-ams-router middlewares configured from `http.security` /
`http.cors` and mounted by `setStandardHandler` after the Recoverer (§3.5);
`config` validates them at load with the router's own rules, so building
them never panics. Apps that need dynamic options (a CSP nonce for their
own pages, `CORSDelegate`, `OriginFunc`) leave the section disabled and
add the router middleware with `Server.Use`.

- **Security** (helmet defaults): CSP `default-src 'self'; …`, HSTS
  `max-age=31536000` (`includeSubDomains` only with
  `hsts.include_subdomains: true`), `X-Frame-Options: SAMEORIGIN`,
  `Referrer-Policy: no-referrer`, COOP/CORP `same-origin`, nosniff,
  `X-XSS-Protection: 0`, … Headers are set before the handler, so a
  handler may override one (the docs page does, §7.8).
- **CORS**: runs before routing and answers **every** `OPTIONS` request (any path)
  with 204, without reaching authentication or a 405. Origins are
  compared byte for byte with `Origin` (hence lowercase, no path);
  `ReflectOrigin`/`OriginFunc` are deliberately not configurable. Actual
  requests get `Access-Control-Allow-Origin` (or none for a disallowed
  origin: the browser blocks the read, the server still ran the request)
  and `Vary: Origin`. Expose `X-Request-Id` if browser clients need it.

### 7.12 Transports: HTTP, HTTPS and HTTP/3 (`transport.go`, `config/server.go`)

Ports are independent and **optional**: `port` 8080 (tcp), `tls.port` 8443
(tcp), `http3.port` 8443 (udp). Omitted or `0` takes the default (an explicit
`0` is never an ephemeral bind); negative or > 65535 fails. Changing
`tls.port` does not move `http3.port`. TCP and UDP 8443 do not collide.
`ApplyServerDefaults` fills them on a copy in the module's Configure, in
`New` and in `Start`, so a `Config` built by hand behaves the same.

| tls | http3 | disable_insecure_http | redirect_http | Result |
|---|---|---|---|---|
| off | off | false | false | HTTP :8080 (legacy default) |
| on | off | false | false | HTTP :8080 + HTTPS :8443, same handler |
| on | off | false | true | :8080 redirects (308); HTTPS serves |
| on | on | false | false | HTTP + HTTPS + QUIC, same handler |
| on | on | false | true | :8080 redirects; HTTPS and QUIC serve |
| on | on/off | true | false | no HTTP listener |
| off | on | true | false | QUIC only (own or `tls.*` cert files) |
| off | on | false | false | HTTP :8080 + QUIC |

Rejected at load (`config.ValidateServer`, also run by `Start`):
`disable_insecure_http` with `redirect_http`; `redirect_http` without
`tls.enabled`; `disable_insecure_http` with neither tls nor http3;
`min_version` other than `"1.3"`/`"1.2"`; `allow_0rtt: true`; negative
`http3` timeouts; HTTP and HTTPS on the same tcp port; missing, partial,
unreadable or mismatched cert/key pairs (errors name the section, never key
material); `same_keycert_tls: true` with `http3.cert_file/key_file` set;
`same_keycert_tls: false` without its own pair. HTTP/3 with
`same_keycert_tls: true` needs `tls.cert_file/key_file` **even when
`tls.enabled: false`** (they are then credentials only; no TCP/TLS listener).
`min_version` is a string (`"1.3"`): the YAML decoder also accepts the
unquoted `1.3`, but `1.30` or `1.1` fail.

- **One handler.** HTTP (normal mode), HTTPS and QUIC serve `Srv.Handler`; the
  chain (§3.5) and routes exist once. HTTPS uses `ServeTLS` with only
  `Certificates`/`MinVersion` set, so `net/http` negotiates `h2` itself.
  HTTP/3 uses `quic-go/http3` with TLS 1.3 and ALPN `h3` forced,
  `quic.Config{MaxIdleTimeout, Allow0RTT: false}` and `IdleTimeout` from
  `http.server.idle_timeout`.
- **Redirect-only listener.** Its own tiny handler (not the router: no docs,
  health, JWKS, auth, tenant, app middleware): `308` to
  `https://<authority><path>?<query>`. The authority is **configuration**:
  `tls.redirect_host`, or `http.server.host` if loopback; a wildcard bind
  address or an unset host is an error, `:443` is omitted, IPv6 is
  bracketed. `Host` and `X-Forwarded-*` are never read. `MaxHeaderBytes`
  16 KiB. If HTTPS cannot start, nothing starts.
- **Alt-Svc.** Only HTTPS (tcp) responses get `Alt-Svc: h3=":<udp port>"` from
  `http3.Server.SetQUICHeaders` (`ma=2592000`), set before the handler (the
  app may override), and only while the QUIC listener is registered. Not sent
  on HTTP/3 responses, redirect responses or without `tls.enabled` (QUIC-only
  servers are not discoverable by browsers: clients must know the endpoint).
  UDP 8443 must be open in firewalls and load balancers.
- **`max_stream_timeout`** wraps the HTTP/3 handler: the request context gets
  the deadline and the stream's read/write deadlines are set to it (the
  counterpart of `read_timeout`/`write_timeout`, which only bind tcp). A
  handler that waits past it can no longer write, so the client sees a reset
  stream; a long handler extends it with `http.NewResponseController`.
- **0-RTT** is reserved: early data is replayable and nForge has no replay
  policy, so `true` fails validation.
- **Startup/shutdown.** `Start` (`prepare`) loads certificates, then binds UDP,
  HTTPS, HTTP in that order, closing everything on any error, and returns once
  every listener is bound; each is served in a goroutine the server owns. A
  serve error after that goes to `Dependencies.Failed` (the module's
  `Scope.ReportFailure`, so the application shuts down). `Stop` shuts every
  transport down in parallel (`Shutdown`, at most 10 s within the ctx, then
  `Close`) and waits for the serve goroutines, before any other module stops.
  net/http can leave a just-accepted HTTP/2 connection undrained: the result
  is the drain deadline (`context.DeadlineExceeded` in the joined error)
  followed by a forced close, never a leak.
- **Credential reload (`certs.go`).** The certificate is served through
  `tls.Config.GetCertificate` from a `certReloader`: a handshake looks at the
  cert and key files' modification times at most every 30 s and re-reads a
  changed pair, and `Server.Reload` (the module's Reload hook: **SIGHUP**
  through `app.Run`, which also reloads the JOSE keys through the jose module)
  re-reads every certificate, so ACME/cert-manager rotation needs no restart
  and drops no connection. A pair that fails to load or does
  not match keeps the previous certificate (WARN, no path or key material);
  HTTP/3 shares the reloader when it uses the same pair.
- **`Srv` customisation.** `newTCPServer` carries `MaxHeaderBytes`,
  `ErrorLog`, `ConnState`, `BaseContext`, `ConnContext`,
  `DisableGeneralOptionsHandler`, `Protocols` and `HTTP2` from the exported
  `Srv` to the HTTPS and redirect servers, and HTTP/3 gets `MaxHeaderBytes`
  and the logger, so configuring `Srv` once applies to every transport.
- **Docs URL.** Default `http.docs.hosts` is `https://…:{tls.port}` when tls
  is on and the plain listener redirects or is closed (or `https://…:{http3.port}`
  for QUIC-only), else `http://…:{port}`; explicit `hosts` win. HTTP/3 is
  still an `https://` URL.
- **Env file keys:** `HTTP_SERVER_TLS_ENABLED`, `HTTP_SERVER_TLS_PORT`,
  `HTTP_SERVER_HTTP3_ENABLED`, `HTTP_SERVER_HTTP3_PORT`,
  `HTTP_SERVER_HTTP3_ADVERTISE=false`, … (`*bool` keeps an explicit false).
- Runnable demo: `_examples/complete/config.https-http3.yaml` (§14).

### 7.13 Module graph page (`graph.go`, `internal/graph`)

Off twice. The page and its Mermaid bundle are compiled only with
`-tags nforge_graph`, so an ordinary binary does not carry them; enabling the route
in a binary built without the tag panics at `nhttp.New`, because a route that
silently answers 404 would hide the mistake. With the tag,
`http.routes.graph.enabled: false` registers nothing and does not even parse the
template, but the bundle is linked (about 2.2 MB). The structural graph exists in
every case, because the orchestrator needs it: `Application.Graph` and
`Application.Status` work without the tag.

```yaml
# go build -tags nforge_graph ./...
http:
  routes:
    graph: { enabled: true }                 # GET /graph
    # graph: { enabled: true, path: /internal/graph }
```

`GET {path}` returns one self-contained HTML page with three tabs: **Visual**
(Mermaid rendered in the browser), **Mermaid JS** (the source) and **JSON**.
`graph.Document` is the one canonical form: the JSON is its encoding and the
Mermaid source is derived from it. Layout (`schema_version` 1): `generated_at`,
`modules[]` (`id`, `enabled`, `provides`, `dependencies`,
`optional_dependencies`, `startup_order`, 0 when disabled), `edges[]` (`from`,
`to`, `capability`, `required`), `operational_status[]` (`id`, `initialization`:
the module state `pending|initialized|started|stopping|stopped|failed|disabled`,
`status`: `ready|not_ready|disabled`).
An arrow points from a consumer to its dependency; dotted means optional.

- A graph that does not resolve fails `app.New`, so the page never shows an
  invalid graph as a healthy application.
- The state is sampled at most every 5 s (`graphStatusTTL`) with a 2 s budget,
  through the application's shared probes, so a burst of requests costs one
  probe per dependency. A
  module with a `Ready` hook is probed (database and cache really ping); a
  failure is shown as `not_ready` and logged as WARN with the module ID only.
- Mermaid (`graph.MermaidVersion`, MIT) is embedded gzipped and served from
  `{path}/mermaid.min.js`: no CDN. To update it, replace
  `assets/mermaid.min.js.gz` (`gzip -9 -n` of npm `dist/mermaid.min.js`) and the
  constant together, and open the page in a browser with the console open.
- The page always gets its own policy, replacing `http.security`'s:
  `default-src 'none'`, scripts by per-render nonce only, no remote origin,
  `frame-ancestors 'none'`. `style-src 'unsafe-inline'` is needed because Mermaid
  writes styles into the SVG. `securityLevel: 'strict'`; data reaches the script
  through the DOM, never interpolated into JavaScript.
- Only module IDs, capability names and states are output: no config values,
  DSNs, addresses or error text. IDs are limited by `module.validID`, the plan to
  256 modules and each render to 1 MiB (`graph.ErrTooLarge`).
- `{path}/mermaid.min.js` is 1.5 MB per uncached response: a public page is also a
  cheap way to spend the server's bandwidth, one more reason to restrict the path.
- **Exposure**: the page is public and shows the topology. `env: prd` logs one
  warning per application (at ERROR, like every config warning there, because prd
  filters below it) unless `app.internal_use: true`, and does not stop the
  startup. **The warning is not access control**: restrict the path at the
  gateway or by network, or wrap it with a global middleware of your own.
- `config.ValidateGraphRoute` runs at load and at registration: a literal path
  only, no collision with docs, health, readiness or JWKS. A collision with an
  application route is the router's registration panic.

### 7.14 Static files and single-page applications (`static.go`)

`Server.Static`, `StaticFS`, `SPA` and `SPAFS` serve files from the same router
as the API, through the same middleware chain (tracing, logging, recovery,
body/timeouts, security headers, CORS, `Use`), on every transport. Routes are
registered **in Go, during a module's Init**; there is no `http.static` YAML
(one source of truth for routing). Each returns an `error`, so an invalid setup
fails `app.Start` before any listener opens.

```go
server := module.Require(scope, nhttp.Capability)
err := server.Static("/assets", "./public")                       // GET /assets/logo.svg -> ./public/logo.svg
err = server.SPA("/", "./frontend/dist",
    nhttp.WithSPAReserved("/api"),                                // an unknown /api/... is a 404, never HTML
    nhttp.WithSPAImmutableDir("assets"))                          // hashed bundle files: immutable for a year
sub, _ := fs.Sub(bundle, "frontend/dist")                         // //go:embed frontend/dist
err = server.SPAFS("/", sub)                                      // the same over an fs.FS (embed.FS)
```

- **Mounting.** `Static`/`SPA` under a prefix register `GET`/`HEAD` on
  `{prefix}` and `{prefix}/*` (segment boundary: `/assetsx` is not `/assets`;
  other methods get the router's 405 with `Allow`). The **root** mount (`"/"`)
  is the router's `NotFound` handler: it runs only when no registered route
  matches, so it can never shadow an API, docs, health, readiness, graph or JWKS
  route, in any registration order. Only one root mount per server; it owns
  `NotFound`. When the router answers 405 (the path exists for another method)
  the SPA is not consulted. Note that the router itself answers 404 (not 405)
  for an unknown method on a path under a root `Mount`, which the built-in
  health/readiness group is (that is the router, not the SPA).
- **Conflicts fail** (never silently overridden): two overlapping prefixes, a
  second root mount, a route (literal, parameter or catch-all) under a mounted
  prefix. It is checked at registration and again in `Start`, so a route added
  later by another module is caught. The root SPA coexists with every route.
- **SPA fallback.** A file that exists is served. Otherwise the entry point
  (`index.html`, `WithSPAIndex`) is served only for GET/HEAD navigations: the
  client lists `text/html` in `Accept` (a wildcard does not count, so `curl`
  and scripts get 404), the last path segment has **no extension**, there is no
  dotfile segment and the path is not reserved. Missing `.js`/`.css`/images and
  every non-navigation are 404. `/` serves the entry point without the `Accept`
  test. Reserved automatically: the docs, health, readiness, graph and JWKS
  paths; everything else (`/api`) is `WithSPAReserved` (an unmatched GET with
  `Accept: text/html` under an API prefix would otherwise get HTML). The
  entry point must exist and be a readable regular file at registration; if it
  disappears later it is a generic 500 (logged), never another file. Frontend
  visibility is not authorization: protect data in the API.
- **Paths and files.** The URL path is decoded by net/http, then refused (404)
  when it has control characters, NUL, a backslash, invalid UTF-8, an empty,
  `.` or `..` segment (decoded traversal included), a trailing dot or space, or
  is over 2 KiB. Dotfiles and dot directories, `*.pem|key|p12|pfx|jks|env`,
  `config.yaml|yml|toml|ini` and `id_*` keys are never served (404, not 403).
  No directory listing and no directory index (`Static` answers a directory with
  404; the SPA serves its entry point only). Read-only: GET/HEAD, no uploads or
  templates.
- **Directories** (`Static`, `SPA`) are opened once with `os.OpenRoot` (the
  kernel refuses any path that leaves the root) and `Open` additionally
  **rejects every symbolic link** on the path (`Lstat` per component), so a link
  inside the tree cannot expose a dotfile, a key or another directory. The check
  and the open are not atomic: the static root must not be writable by untrusted
  users. Relative directories resolve against the working directory when the
  route is registered. The handles are closed by `Server.Stop`. `os.DirFS` is
  **not** a sandbox, so `StaticFS`/`SPAFS` trust the `fs.FS` you pass (embed has
  no links); they still validate the request path and refuse dotfiles and
  non-regular files.
- **Headers.** Content-Type from the extension (`application/octet-stream` when
  unknown, never sniffed), `X-Content-Type-Options: nosniff` unless already set,
  and whatever `http.security` sent stays. Serving uses `http.ServeContent`:
  conditional requests (`If-Modified-Since`, `If-None-Match`), ranges and HEAD
  behave as in net/http. **Validators depend on the file's modification time**:
  a weak `ETag` (size + mtime) and `Last-Modified` exist for directory files;
  `embed.FS` files have no mtime, hence no validators (use cache lifetimes,
  hashed names or a gateway). No compression (a proxy can); no `Vary`.
  Cache-Control defaults to `no-cache` everywhere (`WithStaticCacheControl`,
  `WithSPAHTMLCacheControl` - `immutable` is refused, `WithSPAAssetCacheControl`);
  `WithSPAImmutableDir("assets")` pins only the files of that directory
  (`nhttp.ImmutableCacheControl`), so unhashed files (service workers,
  manifests) are never immutable. Error responses carry no cache policy and no
  filesystem path.
- Not in OpenAPI (documented separately). Non-seekable `fs.File`s are buffered
  (at most 32 MiB). Out of scope: directory listings, dotfile opt-in,
  compression, a custom 404 page, SSR, dev proxies, file watching.
- Runnable demo: `_examples/static` (§14).

---

## 8. Codec (`codec/`)

Business code **never imports `encoding/json`, `encoding/xml` or the CBOR
library directly** — it goes through a `*codec.Registry` (normally
the application's Registry (`codec.Capability`; `codec.FromContext` in a request), or `codec.Default()`).

### 8.1 One struct, three formats

```go
type User struct {
    XMLName xml.Name `json:"-" xml:"user"`             // XML root element
    ID      int64    `json:"id" xml:"id,attr"`         // XML attribute
    Name    string   `json:"name" codec:"required"`    // required in every format
    Email   string   `json:"email,omitempty"`          // cbor and xml inherit json
}
```

- Tag resolution: **json** → `json` tag, else Go name; **cbor** → `cbor`,
  else `json`, else Go name; **xml** → `xml`, else `json`, else Go name.
- `omitempty`/`omitzero` must be **identical in every tag present** on a field,
  otherwise `ErrInvalidTags` (`TagError`). Typed routes surface this at
  registration.
- `codec:"required"` → field must be present and non-null on decode
  (`MissingFieldError` with full path, e.g. `user.address.zip`), and present /
  non-empty on typed-route responses (`CheckRequired`).
- Embedded struct promotion and name conflicts follow `encoding/json` rules.
- Types with their own (un)marshalers (`TextMarshaler`, `json.Marshaler`,
  `time.Time`, …) are **opaque**: the codec doesn't look inside.
- Reflection results are cached per type (`sync.Map`), including errors.

### 8.2 Formats

- **JSON**: stdlib; trailing data is an error. `Decode` uses `json.Unmarshal`
  when `jsonNeedsNumber(t)` is false (no interface reachable from the target,
  cached per type), otherwise a `Decoder` with `UseNumber` (no int64
  precision loss in `any`; `Unmarshal` cannot enable it). A syntax error from
  `Unmarshal` is rerun through the `Decoder` path so messages and offsets
  stay identical (`TestJSONDecodeBytesMatchesDecoder`). `DecodeFrom` keeps
  the `Decoder`.
- **CBOR** (`fxamacker/cbor/v2`): `time.Time` as RFC 3339 with nanoseconds;
  duplicate map keys rejected; trailing data is an error.
- **XML**: custom field mapping on top of `encoding/xml` tokens (so the json-tag
  fallback, strict mode, required, namespaces and DTD blocking all work).
  Output always starts with `xml.Header`. Slices = repeated elements; maps =
  `<field><entry key="k">v</entry></field>`; `[]byte` = base64. A field
  without namespace inherits the parent's; mismatch → `ErrNamespace`.
  `<!DOCTYPE>` → `ErrDTDNotAllowed` unless allowed; external entities are
  **never** resolved. Unsupported xml options (innerxml, any, comment, `a>b`)
  are rejected.
  Element nesting is limited to 256 levels (`maxXMLDepth`, a `SyntaxError`
  past it). Locations in errors (`a.b[2].c`) are `*xpath` linked lists
  rendered only when an error is built: never concatenate path strings per
  level, that is O(depth^2) memory.

Cycles: the CBOR and XML encoders and `CheckRequired` run `checkAcyclic`
(`cycle.go`) first, because a self-referential value overflows the Go stack,
which is fatal (not a recoverable panic). It walks only types that
`mayCycle` (a recursive type graph or a reachable interface), follows only the
fields the format encodes (a `json:"-"` back-reference is fine) and flags
ancestors only, so shared pointers are not cycles. `CheckRequired` also
returns at once for types that cannot hold a required field (`hasRequired`)
or a dynamic value (`reachesInterface`).

### 8.3 Registry

```go
r, err := codec.New(codec.Config{
    Formats: []string{"json", "xml"}, Default: "json",
    StrictUnknownFields: false, MaxPayloadBytes: 5 << 20, AllowDTD: false,
    Validate: func(v any) error { return validator.Struct(v) }, // optional, runs after Validator
}, extraCodecs...)
```

`codec.Module(codec.WithCodecs(...), codec.WithValidator(fn))` owns the
`codec` section (`codec.Settings`), builds the Registry at configuration time
(so an unknown or disabled default format fails `app.New`) and publishes it
under `codec.Capability`; `Registry.MaxPayloadBytes()` and
`StrictUnknownFields()` expose its limits to the transports.

- Every enabled format must have a codec; the default must be enabled.
- Format names: short (`json`), full (`application/json`), with params
  (`application/json; charset=utf-8`), `text/xml` → XML; unknown short names
  become `application/<name>` (custom codecs).
- `EncodeAs`, `DecodeAs`, `EncodeTo` (streams if `StreamCodec`),
  `DecodeFrom` (limit-enforced reader: returns `ErrPayloadTooLarge` without
  reading the rest), `Lookup`, `Formats`, `DefaultFormat`, `Register`
  (init-time only), `Validate`.
- Post-decode validation: `Validate() error` (the `Validator` interface —
  pointer or value receiver) on **every value inside the target** that has
  it (nested structs, pointers, slice elements, map values), children before
  parents, then `Config.Validate` on the root; failures are wrapped in
  `ValidationError` (`ErrValidation` → 422). A nested failure is prefixed with
  its location (`tags[1]: label is empty`; `errors.Is` reaches the cause).
  Only the fields the format decodes are visited (`json:"-"` is skipped), a
  type graph with no validator skips the walk (`hasValidator`), and map values
  are validated as copies.
- Strict mode / required fields need to know which keys the payload had.
  JSON (`presence_json.go`) walks the payload's tokens with
  `encoding/json/jsontext` alongside the target type, skipping values whose
  type cannot fail the check, with no per-value allocation; required fields
  cost about the same as none. It falls back to the generic-tree walk
  (`presence.go`) for invalid syntax (so syntax errors keep their form),
  duplicate names, one field matched by two spellings and names with invalid
  UTF-8. The two must report identical errors: `TestPresenceJSONMatchesTree`
  and `FuzzPresenceJSON` compare them. CBOR still decodes a generic tree
  (a second pass). The walker asks per value whether to descend, so
  `isOpaque`, `shouldWalk` and the `posOf` field index are cached per type:
  an uncached `isOpaque` (eight `Implements` checks) was 36% of strict decode.
  What remains is mostly `jsontext` tokenizing the payload a second time.
  Native `encoding/json/v2` was measured and rejected (encode ties; it
  changes `time.Duration`, nil slices, key case, `any` numbers and has no
  required fields).
- **Limits beyond the byte limit.** `max_payload_bytes` (default **1 MiB**)
  bounds bytes, not the cost of decoding them (a 4.7 MB array of numbers used
  ~300 MB). `codec.max_elements` (default 250,000; JSON tokens, XML elements and
  attributes) is checked before decoding: `ErrTooComplex` → 413. CBOR keeps the
  library's own depth (32) and element limits.
- **Duplicate object members are refused** (`ErrDuplicateField`, 400), exact or
  differing only in case, at any depth, in every mode: parsers disagree about
  which duplicate wins, so a gateway that reads the first and nForge the last
  would see different documents. A linear pre-scan (`scanJSON`) does this and the
  element count; JSON decoding therefore reads the whole payload first.
- Process default: initialized in `init()` with json+cbor+xml, permissive,
  1 MiB; replaced only by the codec module of an application built with
  `app.WithProcessGlobals` (`SetDefault`). Otherwise each application uses its own.

### 8.4 Errors

Every error matches a sentinel via `errors.Is` and carries details via
`errors.As`. Third-party library errors are **converted, never leaked**
(`Unwrap` is deliberately not exposed on `SyntaxError`/`EncodeError`); errors
from a user's own `UnmarshalJSON`/`UnmarshalText` are wrapped and propagated.

| Sentinel | Concrete type |
|---|---|
| `ErrSyntax` | `*SyntaxError{Format, Offset (-1 unknown), Msg}` |
| `ErrUnknownField` | `*UnknownFieldError{Format, Field}` |
| `ErrMissingField` | `*MissingFieldError{Format, Field}` |
| `ErrTypeMismatch` | `*TypeError{Format, Field, Expected, Wire, Got}` (`Expected` is the Go type, for logs; the message uses `Wire`, the payload-level description from `codec.WireType`: object, integer, string, ... so clients never see Go type names) |
| `ErrNamespace` | `*NamespaceError` |
| `ErrInvalidTarget` | `*InvalidTargetError` (decode needs non-nil pointer) |
| `ErrValidation` | `*ValidationError{Err}` (unwraps to the cause) |
| `ErrInvalidTags` | `*TagError` |
| `ErrUnsupportedType` | `*UnsupportedTypeError` |
| `ErrEncode` | `*EncodeError` |
| `ErrUnsupportedFormat`, `ErrPayloadTooLarge`, `ErrDTDNotAllowed` | wrapped with `%w` |

`codec.Fields(format, t)` and `codec.IsOpaque(t)` expose the field model for
code outside the package (the OpenAPI generator uses them).

---

## 9. Database (`database/`)

PostgreSQL only, via `pgx/v5` (`pgxpool`) and Squirrel.

```go
type DB struct {
    Executor DBTX                    // *pgxpool.Pool, or pgx.Tx inside a transaction
    Query    sq.StatementBuilderType // Squirrel with $N placeholders — BUILD ONLY
}
```

Usage pattern — build with `Query`, run with `Executor`:
```go
sql, args, err := db.Query.Select("id", "name").From("users").Where(sq.Eq{"id": id}).ToSql()
row := db.Executor.QueryRow(ctx, sql, args...)
```
`DBTX` = `Exec`, `Query`, `QueryRow`, `CopyFrom`, `SendBatch` (satisfied by
both pool and tx; compile-time asserted). Write repository functions that take
`*database.DB` so they work both on the pool and inside a transaction.

Transactions:
```go
err := db.Transaction(ctx, func(tx *database.DB) error {
    // ALWAYS use tx here; the outer db would run on another connection, outside the tx
    return nil // commit; any error → rollback, returned unwrapped
})
db.TransactionWithOptions(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, fn)
```
- Nested transactions → `ErrNestedTransaction` (no savepoint emulation).
- Rollback is deferred on every path (post-commit `ErrTxClosed` ignored);
  panics are **not** recovered (but the deferred rollback runs).
- A transaction-scoped DB is not concurrency-safe and must not escape the
  callback. `Shutdown` on it → `ErrShutdownInTransaction`.

Construction: `Open(ctx, cfg)` (= `NewPool` + `New`); `NewPool` parses config,
creates the pool and **pings** (closes the pool on failure);
`NewPoolConfig(cfg)` lets advanced callers customize `pgxpool.Config` (tracer,
`AfterConnect`, type registration) and then call `New(pool)`. `New(nil)` panics.

Config rules (`database.Config`):
- `dsn` wins; otherwise `host`, `port`, `name`, `user` are required
  (`ErrMissingConfig` lists missing ones) and a properly escaped `postgres://`
  URL is built (`sslmode` from `ssl.mode`).
- Pool/connection values: only **positive** values override (zero keeps pgx
  defaults or DSN values); negatives are errors; `min_conns > max_conns` is an
  error.
- **Secrets**: `database.Config` redacts its password and DSN (userinfo
  password plus `password`/`sslpassword` query parameters) in `LogValue` and
  in every `fmt` verb (`Format`). Every module section redacts itself the
  same way: `cache.Config` (URL, password, CA path), `jose.Config` (secrets,
  key paths), `nhttp.Config` (TLS key paths), `logs.Settings` and
  `telemetry.Settings` (endpoint userinfo and query), through `String`,
  `GoString`, `LogValue` and `MarshalJSON` where applicable, so the modules
  log their section with any handler. `ConnectionString()` output must never
  be logged. Each package has a test that plants a secret in every such
  field: extend it when you add one.

Module: `database.Module()` (section `database`, capability
`database.Capability`) opens the pool at Init when `database.enabled` (15 s
startup bound, ping, `CheckTenancy`; a failure is fatal), its readiness is
`db.Ping` (always pings the pool, even from a tx-scoped DB) and its Stop is
`db.Shutdown`, which blocks until all connections are released: it runs
after HTTP drained and after every module that depends on the database
stopped (§3.4). Disabled, it publishes nothing.

Error idiom: `pgx.ErrNoRows` → 404; `DELETE` with `RowsAffected() == 0` is
not an error in pgx — check it; other DB errors → log + generic 500 (never
leak details to clients).

Migrations are **out of scope** of the framework (the example creates its
table at startup only to be self-contained).

### 9.1 Multi-tenancy (`tenancy.go`, `internal/rlsexec`)

Opt-in through `database.tenancy` (or `New(pool, WithTenancy(cfg))`). Tenant
IDs are canonical **UUIDs, version 1 to 8** (`ParseTenantID`, lowercased). Two modes:

| Mode | What the framework does | Isolation guaranteed by |
|---|---|---|
| `rls` | sets `set_config(setting, tenant, true)` (transaction-local) before every tenant-scoped statement | the app's PostgreSQL policies (+ `TenantEq` as defense in depth) |
| `column` | nothing on the connection | the query author adding `TenantEq` |

```go
a, err := db.ForTenant(tenantID)          // or db.ForContext(ctx) (ErrNoTenant if absent)
a.Executor.Exec(ctx, sql, args...)        // rls: one pgx batch = one implicit tx with the setting
a.Transaction(ctx, func(tx *database.DB) error { ... })  // rls: setting applied once after BEGIN
q := a.Query.Select("id").From("users").Where(a.TenantEq()).Where(userFilter)  // mandatory predicate first
a.TenantEqOn("u")                         // u.tenant_id = $N, for joins
a.Query.Insert("users").Columns(a.TenantColumn(), "name").Values(a.TenantID(), name)
```

- **Why transaction-local**: a session `SET` would ride the pooled connection
  into the next request of another tenant. In rls mode the tenant-scoped
  `Executor` (`rlsexec.Executor`) prepends `set_config` to the statement in one
  pgx batch (single implicit transaction, no extra round trip); `Query` rows
  close the batch when exhausted/closed (commit errors surface in
  `rows.Err()`); `QueryRow` must be `Scan`ned (as with pgx); `SendBatch`
  prepends the setting and hides its result; `CopyFrom` uses an explicit
  transaction — but **PostgreSQL rejects `COPY FROM` into RLS tables**: use
  INSERT/`SendBatch` there.
- `TenantEq` is a `sq.Sqlizer` that **fails `ToSql`** with `ErrNoTenant` on a
  non-scoped DB (and `ErrTenancyDisabled` when off), so an unscoped query
  cannot be built by accident. User filters go in further `Where` calls
  (AND-ed: they only narrow).
- The root (unscoped) DB keeps working: in rls mode it simply sees **no**
  tenant rows. Cross-tenant work (jobs, admin) uses a **second DB outside
  the database module** opened by the app with another role that has `BYPASSRLS` and
  tenancy disabled: `database.Open(ctx, adminCfg)` (the app owns its
  `Shutdown`).
- `ForTenant` on a transaction-scoped DB → `ErrTenantInTransaction` (scope
  first, then begin). A tenant-scoped DB shares the pool, is safe for
  concurrent use, and `Shutdown` on it → `ErrShutdownTenantScoped`.
- **Startup check** (`CheckTenancy`, run by `Open`; failure closes the pool
  and returns `ErrTenancyCheck` listing every problem): rls mode rejects a
  connection role that is superuser or `BYPASSRLS` (they ignore policies) and,
  for each `tables` entry, requires the table, the tenant column, RLS
  enabled, ≥ 1 policy, and `FORCE ROW LEVEL SECURITY` when the role owns the
  table (owner membership via `pg_has_role`). Column mode checks table and
  column only. Config errors (`ErrInvalidTenancy`) fail before dialing.
- Policies are the app's migrations. Reference shape (fails closed when the
  setting is absent; the DEFAULT lets inserts omit the column):

```sql
ALTER TABLE users ADD COLUMN tenant_id uuid NOT NULL
  DEFAULT NULLIF(current_setting('app.tenant_id', true), '')::uuid;
ALTER TABLE users ENABLE ROW LEVEL SECURITY;
ALTER TABLE users FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON users
  USING      (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
  WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);
```

---

## 10. JOSE (`jose/`)

Package with no internal imports but `module` (its descriptor `jose.Module()`, section `jose`, capability `jose.Capability`), no globals; several `*jose.Service`
can coexist). Crypto is delegated to `github.com/go-jose/go-jose/v4`
(pinned in `go.mod`, `govulncheck` clean at adoption) behind unexported adapters:
**no go-jose type appears in the public API**. Protocol JSON (headers,
claims, JWKs) uses `encoding/json` directly — the one deliberate exception to
the codec rule — so JOSE works even when the app codec is XML/CBOR only.

### 10.1 Profiles

| Kind | Methods | Wire |
|---|---|---|
| `jws` | `Sign` / `Verify` | compact JWS (3 segments), arbitrary bytes |
| `jwe` | `Encrypt` / `Decrypt` | compact JWE (5 segments), arbitrary bytes |
| `jwt_signed` | `IssueJWT` / `ValidateJWT` | claims in a JWS |
| `jwt_nested` | `IssueJWT` / `ValidateJWT` | claims → JWS → JWE (`cty: JWT`) |

JWS/JWE profiles are optional; JWT-only apps configure just a `jwt_signed`
profile. The caller always names the profile; wrong kind → `ErrProfileKind`;
missing producer/consumer capability → `ErrOperationUnavailable` (a
verify-only profile needs no private key). `Profile(name)` returns
`ProfileInfo{Kind, Type, CanProduce, CanConsume}`.

### 10.2 Keys and algorithms

- Algorithms: `EdDSA` (Ed25519), `ES256` (P-256), `PS256`/`RS256` (RSA
  2048–8192), `HS256` (≥ 32-byte secret), `RSA-OAEP-256`, `dir` (32 bytes),
  content `A256GCM`. Everything else (`none`, `RSA1_5`, PBES2, CBC, zip…) is
  rejected. A key is bound to **one** algorithm and its operations
  (`sign`/`verify` or `encrypt`/`decrypt`); `HS256` cannot be mixed with
  asymmetric algorithms in an allowlist.
- Defaults (`ApplyDefaults`): a key without `algorithm` is **HS256**; without
  `operations` it gets every operation of its algorithm; a profile without
  `signing_algorithm` / `encryption_algorithm` takes the algorithm of its key,
  and empty `verification_algorithms` / `decryption_algorithms` become the
  distinct algorithms of its candidate keys. Allowlists therefore still come
  only from configuration, never from tokens. `dir` must be explicit.
- Inline secret: `secret: ${JWT_SECRET}` (raw string bytes) for HS256 (≥ 32
  bytes) or `dir` (exactly 32 bytes); mutually exclusive with `source`; an
  empty value (unset `${VAR}`) fails validation with `secret or source is
  required`. Redacted like every key field. Trade-off: with HS256 every
  validator can also issue tokens and nothing goes in the JWKS — prefer
  asymmetric keys in files when other services must only verify.
- File sources: `pem` (one unencrypted PKCS#8 / PKCS#1 / SEC1 / PKIX block, no
  trailing data), `jwk` (one strict JWK object; metadata must match config),
  `oct` (canonical unpadded base64url, optional single trailing LF/CRLF).
  Files: regular file (symlinks followed), bounded size, and private/secret
  files must have **no group/other bits**. The same material under two kids
  is rejected.
- `publish: true` exports only public members of asymmetric keys
  (`PublicJWKS`, sorted by kid, stable bytes); symmetric keys can never be
  published.

### 10.3 Parsing and validation pipeline

Size bound → base64url alphabet / segment count / canonical encoding →
bounded protected header with strict JSON (no duplicate members at any
depth, valid UTF-8, depth limit) → allowed headers only (`alg`, `kid`, `typ`,
+`enc` for JWE, +`cty` for the nested outer layer) → exact `typ`, algorithm
allowlist, `kid` resolved only among the profile's candidates → length
prechecks (signature, IV, tag, encrypted key) → crypto → header cross-check.
`kid` and `typ` are mandatory on incoming tokens.

JWT claims: `iss`, `sub`, `aud`, `exp`, `iat`, `jti` required, `nbf`
optional; `NumericDate` is integral seconds in years 0001–9999 (fractions,
strings, null rejected); `Audience` decodes a string or array, encodes an
array. Checks with one clock snapshot: issuer exact, ≥1 accepted audience,
`now < exp + skew`, `now + skew >= nbf`, `iat <= now + skew`,
`iat <= nbf < exp`, `0 < exp - iat <= max_ttl`. `IssueJWT` fills
`iss`/`aud`/`iat`/`nbf`/`exp`/`jti` (128-bit random) from the profile;
custom claims (struct or string-keyed map) cannot use registered names.
`ValidateJWT(ctx, profile, token, target)` assigns `target` only on full
success.

`WithClaimsValidator(profile, fn)` runs app checks on issue and validate;
return errors wrapping `ErrClaimsRejected` (→ HTTP 401) or
`ErrValidationUnavailable` (→ 503); anything else is `ErrInternal`. On issue
`custom` is a normalized `map[string]any`; on validate it is the decoded
target pointer (or the map when no target). `WithClock` is for tests.

### 10.4 Errors and redaction

Sentinels (`ErrDisabled`, `ErrClosed`, `ErrInvalidConfig`, `ErrUnknownProfile`,
`ErrProfileKind`, `ErrOperationUnavailable`, `ErrInvalidKey`, `ErrUnknownKey`,
`ErrUnsupportedAlgorithm`, `ErrMalformedToken`, `ErrPayloadTooLarge`,
`ErrInvalidSignature`, `ErrDecryption`, `ErrInvalidClaims`, `ErrExpired`,
`ErrNotYetValid`, `ErrInvalidIssuer`, `ErrInvalidAudience`, `ErrInvalidType`,
`ErrInvalidTarget`, `ErrClaimsRejected`, `ErrValidationUnavailable`,
`ErrInternal`) via `errors.Is`; details via `*jose.Error{Code, Operation,
Profile, Field, Reason}`. Messages never contain tokens, claim values, key
bytes, key paths or go-jose text; only context errors are unwrappable.
`Config`, `KeyConfig` and `KeySourceConfig` redact paths in `%v`/`%+v`/`%#v`
and slog.

### 10.5 Lifecycle, reload and rotation

- Every operation is admitted against one immutable snapshot (nested tokens
  use the same snapshot for both layers). `Shutdown(ctx)` is idempotent:
  new operations get `ErrClosed`, admitted ones finish (ctx bounds the
  wait; a later call keeps waiting); secrets are cleared best-effort after
  the drain. `Check(ctx)` is the readiness probe.
- `Reload(ctx)` rereads the key files of the **same** config and swaps the
  snapshot atomically only if all keys validate (failure keeps the old one;
  identical material is a no-op). It never reloads YAML or profiles, starts
  no watcher and is never triggered by tokens. The jose module's Reload hook
  calls it: `app.Run` triggers it on **SIGHUP** (together with the TLS
  certificates of nhttp); an application with another trigger calls
  `Application.Reload(ctx)` (or `Service.Reload`) itself. The
  replaced snapshot is wiped (best effort) as soon as the operations admitted
  before the swap have finished; each snapshot counts its users.
- Rotation: deploy config with old **and** new kids, switch the producer
  key, keep the old consumer key until tokens expire + skew + JWKS cache
  age; publish a new public key before signing with it. Replacing material
  under the same kid invalidates outstanding tokens.
- Stateless: `jti` does not prevent replay; revocation, disabled accounts
  and refresh tokens (a distinct profile with its own `typ`/audience) are
  application concerns (`WithClaimsValidator`).

---

## 11. Cache (`cache/`)

Package with few imports: `codec`, `module` (its descriptor) and `database`
(only the tenant context of the module's tenant isolation); no `config`,
`app`, `nhttp`, `logs` or `telemetry`. One **non-generic** `*cache.Cache` per
instance, one driver per instance, no globals (several can coexist). Redis
goes through `github.com/redis/go-redis/v9` (pinned in `go.mod`) behind
unexported adapters: no go-redis type appears in the public API. The
memory driver is stdlib only (`container/list` + `container/heap`).

```
cache/
├── cache.go       # Open, Cache facade: lifecycle (begin/end, Shutdown), raw ops, objects, decodeInto
├── config.go      # Config, ApplyDefaults, ValidateConfig, Redis URL parsing, redaction, ByteSize
├── options.go     # WithCodecRegistry, WithNamespace, WithTenantResolver, WithLogger (+ private withClock)
├── keys.go        # keyspace: prefix/tenant resolution, injective escaping, max_key_bytes
├── load.go        # GetOrLoad and the per-instance flight coordinator
├── expiration.go  # Expiration, normalizeTTL, the private backend interface
├── redis.go       # go-redis adapter: options, TLS, read retries, error classification
├── memory.go      # map + LRU list + deadline heap, byte/entry accounting, cleanup worker
└── errors.go      # sentinels and the sanitized *Error
```

### 11.1 API

```go
c, err := cache.Open(ctx, cfg, cache.WithNamespace(env, app), cache.WithCodecRegistry(reg),
    cache.WithTenantResolver(fn), cache.WithLogger(logger))   // cache.Module() does this for you
c.Get(ctx, key, &target) / c.Set(ctx, key, value, ttl) / c.SetIfAbsent(ctx, key, value, ttl) (bool, error)
c.GetBytes(ctx, key) ([]byte, error) / c.SetBytes(ctx, key, b, ttl) / c.SetBytesIfAbsent(ctx, key, b, ttl) (bool, error)
c.Delete(ctx, keys...) (int64, error) / c.Exists(ctx, key) (bool, error)
c.Expire(ctx, key, ttl) (bool, error) / c.TTL(ctx, key) (cache.Expiration, error)
c.Increment(ctx, key, delta) (int64, error)
c.IncrementWithTTL(ctx, key, delta, ttl) (int64, error)   // ttl > 0, applied only when the call creates the counter
c.GetOrLoad(ctx, key, &target, ttl, func(ctx context.Context) (any, error))
c.Ping(ctx) / c.Shutdown(ctx) / c.Driver()
cache.ApplyDefaults(&cfg); cache.ValidateConfig(cfg)
```

- `Open` deep-copies cfg, applies defaults, validates, checks the codec is
  in the registry (`ErrUnsupportedCodec`), builds the driver and **pings**
  with ctx; on failure it closes what it created. Disabled cfg →
  `ErrDisabled`. It never panics or exits (the module makes it a fatal Init error).
- Without `WithCodecRegistry`, `codec.Default()` is captured **once**.
  Without `WithLogger`, a discarding logger. Nil options and conflicting
  duplicates are `ErrInvalidConfig`. `WithNamespace` is required unless
  `cache.prefix` is set.
- Common contract: ctx checked first; after `Shutdown` → `ErrClosed`;
  empty key or full key > `max_key_bytes` → `ErrInvalidKey`; keys are never
  trimmed and wildcards are literal (`Delete("*")` deletes the key `*`).
- **TTL**: `0` = persistent, negative → `ErrInvalidTTL`; positive values are
  rounded **up** to whole ms (min 1 ms, overflow-safe) so Redis `PX` and
  memory agree. `Set*` replaces value **and** TTL. `Expire` needs ttl > 0
  (to make a key persistent, write it again with 0) and returns false for a
  missing key. `TTL` → `{Exists:false}` / `{Exists, Persistent}` /
  `{Exists, Remaining}` (never Redis's -1/-2; Remaining ≥ 0, may be 0 while
  live). Memory expires when `now >= deadline`.
- **Bytes**: `GetBytes` returns a fresh slice (non-nil empty for an empty
  value); `SetBytes` copies the input; nil input = empty value, not delete.
- **Objects**: encoded with `registry.EncodeAs(cache.codec, v)`, decoded with
  `DecodeAs` into a **fresh value** of the target's type, assigned only on
  full success (codec limits, `codec:"required"`, `Validate()` and the
  registry validator included). A miss, backend error or decode error leaves
  the target untouched. Decode failure is `ErrDecode` (cause unwrappable,
  e.g. `codec.ErrValidation`), never a miss. No envelope is stored: JSON
  values are inspectable, and **changing `cache.codec` needs the old entries
  to expire or a new `cache.prefix`** (`my-app:cache:v2`).
- **NX** (`SetIfAbsent`/`SetBytesIfAbsent`): one atomic `SET NX PX`
  (memory: under the lock); false, nil when a live entry exists (value and
  TTL untouched); an expired memory entry counts as absent. A lost Redis
  reply is `ErrOutcomeUnknown`, never false.
- **Delete** dedupes the resolved keys, validates all of them before any
  mutation, returns the number of distinct existing keys removed; no keys →
  0 (lifecycle still checked).
- **Increment**: decimal ASCII int64 (`INCRBY` grammar: no `+`, spaces or
  leading zeros; `-0` rejected). Missing → starts at 0, persistent; the
  existing TTL is preserved; bad content → `ErrNotInteger`, overflow →
  `ErrCounterOverflow`, both without changing the entry. Never mix counter
  and object keys (CBOR objects are not integers). `IncrementWithTTL` is for
  counters that must expire (rate-limit windows): the creating call sets the
  TTL atomically with the increment (Redis: one Lua script via EVALSHA, so
  the user needs EVAL permission; memory: under the lock), later calls keep
  the remaining TTL, `ttl <= 0` is `ErrInvalidTTL`. Do not build this from
  `Increment` + `Expire`: a crash between them leaves a counter that never
  resets.
- `Ping` needs no tenant; memory only checks that the cache is open.
- `max_value_bytes` applies to encoded and raw writes and to values read
  back (Redis: checked after go-redis has read the bulk reply — it does not
  stop the client from allocating a huge reply).

### 11.2 Drivers

**Redis** (`redis.go`): GET, SET [PX], SET NX [PX], DEL, EXISTS, PEXPIRE,
PTTL, INCRBY, PING, plus one Lua script (EVALSHA, EVAL only after NOSCRIPT)
for `IncrementWithTTL`; never EXISTS+SET or GET+SET emulation, never
`CONFIG SET`/`FLUSHDB`/`FLUSHALL`. Client options: `MaxRetries: -1` (no
automatic replay), `DialerRetries: 1`, `ContextTimeoutEnabled: true`,
`DisableIdentity: true`, maintenance notifications disabled, pool/timeouts
from config. Every operation runs under `context.WithTimeout(ctx,
timeouts.operation)` (an earlier caller deadline wins). **Pure reads**
(GET, EXISTS, PTTL, PING) are retried `read_retries` times with jittered
exponential backoff inside that deadline, only on transport reasons (not
`auth`/`server`). **Mutations are never retried.** `classify` maps errors:

| go-redis error | cache error |
|---|---|
| `redis.Nil` | `ErrNotFound` |
| server reply `NOAUTH`/`WRONGPASS`/`NOPERM` | `ErrUnavailable` reason `auth` (not retried) |
| `… not an integer …` / `WRONGTYPE` / `… would overflow` / `invalid expire time` | `ErrNotInteger` / `ErrNotInteger` / `ErrCounterOverflow` / `ErrInvalidTTL` |
| other server reply | `ErrUnavailable` reason `server` |
| pool timeout/exhausted, closed client, dial error (never sent) | `ErrUnavailable` (`pool_timeout`, `closed`, `connect`) |
| anything else (read timeout, EOF, ctx done) on a **mutation** | `ErrOutcomeUnknown` (+ matches `ErrUnavailable`; ctx error unwrappable) |
| the same on a read | `ErrUnavailable` (`timeout`/`connection`) |

Server replies are authoritative (never "unknown"). Recovery: go-redis
replaces broken connections and dials again on later operations, so the
**same** `*Cache` works again once Redis is back (no custom reconnect loop,
no driver fallback). The Redis text is never kept (it may echo
credentials). go-redis's own global logger still prints dial failures with
the address (no credentials) to stderr. Redis eviction of a key is a normal
miss; `maxmemory`/eviction policy belong to the operator.

URL: `redis://[user[:password]@]host[:port][/db]` or `rediss://`; query
options, fragments, other schemes, missing host, bad port/db and control
characters are rejected; the URL wins for address, credentials, database
and TLS (URL credentials never fall back to the structured ones); a
`redis://` URL with `tls.enabled: true` is a config error. TLS: min 1.2,
`ServerName` = `server_name` or the host, `RootCAs` from `ca_file` (PEM,
≤ 1 MiB) or system roots.

**Memory** (`memory.go`): one mutex guards a map (full key → entry), an LRU
list and a min-heap of deadlines. `Get`/`GetBytes` and successful writes
mark an entry most recently used; `Exists`, `TTL`, `Expire` and `Ping` do
not. Accounting = `len(full key) + len(value)` per entry (map/list/heap and
allocator overhead are **not** counted: `max_bytes` is a payload budget, not
an RSS cap). A write first rejects values > `max_value_bytes` or
key+value > `max_bytes` (`ErrValueTooLarge`, nothing evicted or replaced),
then removes the old entry, reclaims expired entries from the heap top and
evicts LRU entries until both `max_entries` and `max_bytes` hold. Expired
entries are never returned; one cleanup goroutine per instance removes at
most 256 expired entries per lock acquisition every `cleanup_interval` and
stops on `Shutdown`. Data is lost on restart. The memory driver has no
retries and no "unknown outcome".

### 11.3 GetOrLoad

1. Validate target, loader (nil → `ErrInvalidConfig`), TTL, key/tenant.
2. Read: hit → decode, done. Any error other than `ErrNotFound` (Redis
   down) is returned and **the loader is not called**. A hit that does not
   decode (`ErrDecode`: corrupt bytes, a newer `codec:"required"` field, a
   stricter `Validate`, strict mode vs a newer writer, another `cache.codec`)
   is treated as a **miss**: WARN `[CacheModule] GetOrLoad found an
   undecodable entry and reloads it` (no key, tenant or value), then steps
   3-5, whose store overwrites the entry (the flight's recheck also ignores
   bytes its leader's target cannot decode). Plain `Get` stays strict and
   returns `ErrDecode`.
3. Join or start the flight of the **full resolved key** (tenant included)
   in this instance. At `max_inflight_loads` distinct flights a new key gets
   `ErrLoadCapacity`; joining is always allowed.
4. The leader goroutine runs with `context.WithoutCancel(first caller ctx)`
   (values kept) + `load_timeout`, canceled when **all** callers left or on
   `Shutdown`. It rechecks the cache, calls the loader once (panic →
   `ErrLoaderPanic`, panic value never exposed), encodes once, and stores
   with the leader's TTL via the backend `set` (best effort: a write
   failure, a full memory cache or a value over `max_value_bytes` only logs
   WARN `[CacheModule] GetOrLoad could not store the loaded value` with
   op/driver/reason). An abandoned flight keeps its slot until its loader
   returns, so loads of one key never overlap.
5. Each caller decodes the shared bytes into its **own** target
   (`ErrDecode` per caller); a caller whose ctx ends gets its ctx error, on
   `Shutdown` `ErrClosed`; targets are only assigned on success.

Loader errors are returned **unchanged** (branchable with `errors.Is/As`,
never cached; don't send them blindly to clients). **A shared load belongs to
its first caller**: the loader runs with that caller's context *values*
(principal, tenant, request id; cancellation is detached) and every
concurrent caller of the same key gets the same bytes and, on failure, the
same error value. So a loader must not authorize by what it finds in `ctx`
(the result may be handed to a caller who was not checked), and a loader error
must not carry one caller's secrets; put the identity that matters in the
cache key. Not a distributed lock:
other instances may load the same key; callers of one key must use
compatible loaders/TTLs (the leader's win); a concurrent unrelated `Set`
can be overwritten by the loaded value.

### 11.4 Namespace and tenants

```
default, single tenant: nforge:<env>:<app.name>:<key>
default, multitenant:   nforge:<env>:<tenant>:<app.name>:<key>
custom,  single tenant: <prefix>:<key>            (one ':' appended if missing)
custom,  multitenant:   <prefix>:<tenant>:<key>
```

env, app and tenant are escaped with an injective percent-encoding of `%`,
`:` and control bytes (UUIDs and ordinary names are unchanged); the prefix
is a trusted literal (no control characters); the key is appended verbatim.
Tenant isolation is on when a resolver is given; the cache module gives one
exactly when the registered database module has
**`database.tenancy.enabled`** (regardless of `database.enabled`; read in its
`Check` through `module.ConfigOf`), bound to **`database.TenantFromContext`**
(`cache.tenantFromContext` returns `database.ErrNoTenant` when absent). An
application that registers the cache without the database module has no
tenant isolation in the cache. That context value is set by
the existing `nhttp.Tenant()` / `nhttp.SetTenant` / `Opts.Tenant` (or
`database.WithTenant`). It is resolved once per operation, **before** any
backend access, flight or loader; missing → `ErrTenantRequired`, never a
fall back to the shared namespace. `Ping` and `Shutdown` need no tenant. A
tenant namespace isolates data, it does not authorize: include user or
resource identity in keys when callers of one tenant must not share.

### 11.5 Lifecycle and errors

Operations are admitted under a mutex (`active` counter). `Shutdown(ctx)`:
closes admission permanently (`ErrClosed`), cancels `closing` (stops
loaders and waiting GetOrLoad callers), stops the memory worker, waits for
active operations and loaders, then closes the driver once (Redis pool /
clear memory). If ctx ends first it returns the ctx error with admission
still closed; a later call keeps waiting. Idempotent and concurrency-safe.

Sentinels: `ErrDisabled`, `ErrClosed`, `ErrInvalidConfig`,
`ErrUnsupportedDriver`, `ErrUnsupportedCodec`, `ErrInvalidKey`,
`ErrTenantRequired`, `ErrInvalidTTL`, `ErrInvalidTarget`, `ErrNotFound`,
`ErrEncode`, `ErrDecode`, `ErrValueTooLarge`, `ErrNotInteger`,
`ErrCounterOverflow`, `ErrUnavailable`, `ErrOutcomeUnknown`,
`ErrLoadCapacity`, `ErrLoaderPanic`; context errors are their own category
(`errors.Is(err, context.Canceled)`). `*cache.Error{Op, Driver, Kind,
Reason}`: the message has no key, tenant, value, credential, URL, CA path or
Redis text; `Unwrap` exposes only codec causes and context errors. Config errors never echo the
URL. `Config`/`RedisConfig` redact URL, password and CA path in every `fmt`
verb, and `Config.LogValue` logs only the shape. The cache maps nothing to
HTTP; readiness owns its 503 (§7.6) and the cache example maps the rest
(`_examples/cache` `sendCacheError`).

---

## 11A. Messaging (`message/`)

RabbitMQ AMQP 0-9-1 only, behind the `message` module (`message.Module()`,
section `message`, capability `message.Capability` = `*message.Client`). It
imports `module` and `codec` (payloads go through the application's Registry in
`message.payload_format`) and no AMQP type appears in its public API. Disabled
(`message.enabled: false`, the default), it publishes nothing and never
connects; a module that `Requires` it fails `app.New` (`ErrDisabledDependency`),
one that declares it `Optional` gets `ok == false` from `module.Lookup`.

```
message/
├── doc.go       # package doc
├── config.go    # Config, RabbitMQConfig, queues/retry config, defaults, validation, redaction
├── queue.go     # QueueSpec builder: Queue, QuorumQueue, MaxPriority, DeadLetter, WithRetry
├── module.go    # Module (connection resource), Consumers (ingress), consumer options, Delivery
├── client.go    # Client: connect/supervise/reconnect, confirmed Publish, Total, Reprocess, Status
└── errors.go    # sentinels and the sanitized *Error
```

```go
message.Module(message.WithQueues(message.Queue("audit").QuorumQueue().DeadLetter()))   // the connection
message.Consumers()                                                                      // ingress: consumers start last, drain first
cl := module.Require(scope, message.Capability)
cl.AddConsumer("notifications", func(ctx context.Context, d *message.Delivery) error {
    var n Notification
    if err := d.Decode(&n); err != nil { return err }
    return handle(ctx, n)                       // nil = ack; error = nack (retry/DLQ when configured)
}, message.WithConcurrency(4), message.WithPrefetch(8), message.WithTimeout(30*time.Second))
res, err := cl.Publish(ctx, "notifications", n, message.WithPriority(5), message.WithMessageID(id),
    message.WithHeaders(map[string]string{"tenant": t}), message.WithContentType("application/json"))
total, err := cl.Total(ctx, "emails")                      // pending / retrying / failed (+ Known flags, IsEstimate)
out, err := cl.Reprocess(ctx, "emails", 100)               // DLQ -> main queue, bounded
st := cl.Status()                                         // sanitized local connection snapshot
```

- **Two modules, one lifecycle rule.** `message.Module()` owns the connection
  (Init opens it within `dial_timeout`; a failure is fatal; Ready = the
  connection and publisher channel are usable; Stop closes it). `message.Consumers()`
  is an **ingress** module (`Optional` on the capability): its Start begins
  consuming after every Init and Start, and its Stop stops fetching and waits for
  running handlers, so the broker connection is never closed under a handler
  (§3.4). Register consumers with `Client.AddConsumer` **in an Init** (after
  start it is `ErrStarted`); one consumer per queue; the queue must be declared.
- **Queues** come from YAML (`message.queues`) and `message.WithQueues`; the two
  merge by name and must be identical when duplicated (`ErrQueueConflict`).
  Defaults: durable **Classic**; `type: quorum` / `QuorumQueue()`; `max_priority`
  1..10 is Classic-only (`ErrInvalidPriority`); `dead_letter: true` declares the
  managed `<name>_dlq` queue and the `<namespace>.dlx` exchange; `retry:
  {delay, max_attempts}` (needs `dead_letter`) declares `<name>_retry` (TTL =
  `delay`, dead-lettering back to the queue). Names ending in `_dlq`/`_retry`,
  starting with `amq.` or longer than 255 bytes are invalid. Declarations are
  **immutable**: type, priority and dead-letter arguments must match an existing
  queue, nForge never deletes or converts one.
- **Publishing** is confirmed (`publisher.confirm_timeout`), persistent and
  `mandatory`. Each publish waits for **its own** confirmation (amqp091 deferred
  confirms; a basic.return is attributed through the internal `x-nforge-pubid`
  header), never a shared channel, so a confirmation that arrives after a publish
  gave up cannot satisfy the next one. The client lock is held only to read the
  channel, so publishers **pipeline** (≈10 k confirmed msg/s at 2 ms RTT with 32
  publishers; it used to be `1/RTT`) and `Status()` never waits for a publish.
  When the channel closes while waiting the outcome is `ErrPublishUncertain`, not
  a nack. Errors: `ErrUnroutable` (returned by the broker), `ErrNacked`,
  `ErrPublishUncertain` (the reply was lost: the message may or may not be
  queued; do not blindly retry non-idempotent work), `ErrTransportUnavailable`,
  `ErrPayloadTooLarge` (`publisher.max_payload_bytes`, at most the codec limit).
  A `[]byte` payload is sent as is (`application/octet-stream`), anything else
  is encoded with `payload_format`; `WithPriority` needs a queue with
  `max_priority` (`ErrInvalidPriority`). The message ID defaults to a UUIDv7. User headers are validated (a bounded
  token syntax, string values, no `x-` prefix); nForge's own headers
  (`x-nforge-format`, `x-nforge-attempt`, `x-nforge-reprocessed`) are never
  visible in `Delivery.Headers`. At-least-once: **no exactly-once guarantee**.
- **Consuming.** `consumer.concurrency` workers (1..1024), `prefetch` (0 = the
  concurrency), a per-delivery `timeout` and `ack_mode: automatic|manual`
  (`WithManualAck`: the handler calls `Delivery.Ack`/`Nack(requeue)`; a second
  settle is `ErrAlreadySettled`, a delivery of an older connection
  `ErrStaleDelivery`). Automatic mode acks on a nil return and nacks on an
  error; with `dead_letter`/`retry` a failing message goes to the retry queue
  until `max_attempts` handler invocations in total (3 = the handler runs at
  most 3 times), then to the DLQ. **Without `dead_letter` a failed message is
  discarded** (one Nack, no requeue); the module warns at consumer registration
  unless the queue says `on_failure: drop` (`QueueSpec.DropOnFailure()`). `Delivery.Attempt` comes from a
  producer-writable header: best effort, not a business retry counter.
  Recovery: the supervisor reconnects with `reconnect` (initial delay,
  multiplier, jitter, cap) and restarts the consumers; readiness is `not_ready`
  meanwhile and `Generation` increments.
- **Totals and reprocessing.** `Total` uses passive AMQP declarations: `Pending`,
  `Retrying` and `Failed` are ready counts of the main, retry and DLQ queues;
  `Processing` (unacknowledged) is **unknown** (`nil`: no Management API is
  called), and `IsEstimate` is always true. `Reprocess(queue, limit)` moves up to
  `limit` DLQ messages back (republished with a confirm, then acked from the
  DLQ). A republish that is not confirmed or is unroutable puts the message back
  in the DLQ (`Nack` requeue) and **stops the run**: a message is never acked
  from the DLQ without a confirmation that is provably its own; one run at a time per client (`ErrReprocessRunning`); a queue without
  `dead_letter` is `ErrUnsupportedFeature`.
- **Routed (best effort).** `Client.SubscribeRouted` / `PublishRouted` are the
  direct-exchange form of the fanout pair: the ephemeral queue starts unbound,
  `Subscription.Sync(key, want)` binds or unbinds a key from the truth `want()`
  reports (evaluated under the subscription's lock), and the wanted keys are bound
  again on the new queue after an outage. Used by `sse` `routing: user`.
- **Fanout (best effort).** `Client.PublishFanout(ctx, exchange, body)` confirms a
  transient publish to a durable fanout exchange it declares per connection
  generation (no `mandatory`: no bound queue is a successful drop), and
  `Client.SubscribeFanout(ctx, exchange, handler, opts...)` returns a
  `*Subscription` on an exclusive, auto-delete, non-durable, server-named queue
  that re-attaches by itself after an outage (the client's backoff), one worker,
  `Ack` on nil and `Nack` without requeue on error or an oversized message.
  A publisher channel the broker closed makes the client reconnect. Used by
  `sse`; no durable queue, retry or DLQ applies.
- **Trace context.** With the tracing module present the client opens a
  producer span per `Publish` and a consumer span per delivery; the W3C
  `traceparent`/`tracestate` travel in the reserved `x-nforge-traceparent` /
  `x-nforge-tracestate` headers (kept across retry and reprocess). The producer
  may be outside nForge, so unless `tracing.otel.trust_incoming` is true the
  consumer starts a new trace and attaches the producer's context as a **link**,
  not the parent (a producer cannot pick the trace ID or sampling). Trace
  context is never used for authorization.
- **Metrics.** With the metrics module: `nforge_message_publishes_total{outcome}`,
  deliveries, handler failures, retries, nacks, connection readiness and generation.
- **Security.** `message.Config` redacts the URL in every `fmt` verb, slog and
  JSON; `amqps://` verifies the hostname (TLS 1.2+, optional `tls.ca_file`); a
  remote `amqp://` URL with credentials warns (`prd_exceptions.message_plaintext`,
  an error in `prd` otherwise) and so does HTTPS/HTTP3 in front of it. Errors
  never carry the URL, credentials or broker text.
- Runnable demos: `_examples/message` (worker-only producer/consumer/reprocess)
  and the optional events of `_examples/complete` (§14). Integration tests need
  RabbitMQ (`NFORGE_TEST_RABBITMQ_URL`, §15).

## 11B. Server-Sent Events (`sse/`)

`sse.Module()` (ID `sse`, section `sse`, capability `sse.Capability` =
`*sse.Manager`) is an explicitly registered module: nothing enables SSE just
because HTTP is on, and omitting it costs nothing. It requires `nhttp` and
`codec` (with `json` enabled) and, for `driver: rabbitmq`, the existing
`message.Module()` (a disabled one is `ErrDisabledDependency`). With the section
absent the defaults of §4.2 apply. It imports `module`, `codec`, `nhttp` and
`message`.

```
sse/
├── doc.go, errors.go  # sentinels: ErrInvalidEvent, ErrTransportUnavailable, ErrClosed, ErrUnauthorized, ...
├── config.go          # Config, defaults, validation, default exchange name
├── event.go           # Event, PublishResult, wire form, validation, frame encoding
├── manager.go         # Manager: user -> connection set, bounded queues, limits, Stats, close
├── handler.go         # Handler, IdentityResolver, origin policy, the writer loop
├── memory.go          # memory driver: local dispatch only
├── rabbitmq.go        # rabbitmq driver over message.Client.PublishFanout/SubscribeFanout
└── module.go          # Module: graph, Check, hooks
```

```go
mgr := module.Require(scope, sse.Capability)
server.Router().With(nhttp.Authenticate(j, "access")).Get("/notifications/stream", mgr.Handler().ServeHTTP)
res, err := mgr.PublishToUser(ctx, userID, sse.Event{Name: "note", Data: payload}) // res.Accepted: driver took it, NOT browser receipt
st := mgr.Stats()                                                                   // metrics snapshot; no user IDs
```

- **Identity.** The default resolver is `sse.UserKey(principal)`: issuer, tenant
  and subject, length-delimited, of the principal that `nhttp.Authenticate`
  validated, so tenants or issuers that share a subject ID never share a stream.
  **`PublishToUser` takes that key**: publishers holding the principal call
  `PublishToUser(ctx, sse.UserKey(p), ev)`, others `sse.UserKeyOf(issuer, tenant, subject)`; with no principal the stream answers 401 before
  any SSE header (never anonymous). `WithIdentityResolver` replaces it (return
  errors wrapping `ErrUnauthorized` → 401 or `ErrForbidden` → 403). A user ID the
  module cannot carry (empty, control characters, over `max_user_id_bytes`) is
  403. `nhttp.Authenticate` reads only the `Authorization` header, so a browser
  `EventSource` (no custom headers) needs a same-site HttpOnly cookie turned into
  that header by a small middleware (`_examples/sse`), or a short-lived stream
  ticket; never a JWT in the query string. A stream ends at
  `min(token exp, max_lifetime)` (`Principal.ExpiresAt`, `WithExpiry` for custom
  resolvers) after a final `event: reauth` whose data is `expired` or `lifetime`,
  so the page can refresh its credential and reconnect instead of retrying with
  one the server will refuse for good (a 401 stops `EventSource` permanently).
  Revocation takes effect at the next reconnect.
- **Protocol.** 200, `text/event-stream; charset=utf-8`, `Cache-Control:
  no-cache, no-transform`, `X-Accel-Buffering: no`, `Connection: keep-alive`
  only on HTTP/1.x. Frames are `id:`, `event:`, one `data:` line per payload
  line (CR, LF, CRLF), blank line, then flush; a first `retry:` + comment frame
  flushes at once; `: heartbeat` every `heartbeat`. `Data` is a string/`[]byte`
  as is, anything else JSON through the application's codec. IDs (printable, no
  control characters) are generated per event when absent; an ID does **not**
  imply replay. `Last-Event-ID` is validated (size, no control characters, else
  400) and ignored: no history exists, so gaps and duplicates are possible.
- **Connections.** One goroutine writes each stream (frames and heartbeats
  serialized), the queue holds `buffer_size` frames and a client that overflows
  it is disconnected, never partially fed. Locks guard only map membership; no
  lock is held across a write or broker call. `sse.send_buffer_bytes` caps
  SO_SNDBUF of HTTP/1.1 streams (through `ConnContext`, installed by the module):
  without it the kernel auto-tunes the buffer to megabytes and a client that never
  reads absorbs everything sent for a long time without ever triggering
  `write_timeout`. Limits answer: per-user 429,
  global 503, rate 429 (all with `Retry-After` and the same static text, so
  nothing discloses another user's presence), not started or closing 503, a
  response chain that cannot flush 500. Every write has `write_timeout`.
- **Lifecycle.** The module is **ingress**: it accepts streams only after the
  driver is active (503 before), and stops *before* HTTP drains (ingress modules
  stop in reverse dependency order), closing every stream, then the driver, and
  before `message.Module()` closes the connection. HTTP therefore never waits for
  endless streams. `Publish` after close is `ErrClosed`. `Ready` is the driver's.
- **Memory driver.** Local dispatch to this instance's streams; no persistence,
  replay, retry or cross-process fanout. Accepted = local dispatch ran.
- **RabbitMQ driver.** One fanout exchange per application and environment
  (`<namespace>.sse.<env>.<app.name>`: apps sharing a broker never see each
  other's events) and, per instance, an exclusive auto-delete non-durable
  server-named queue bound to it: every instance receives every event and filters
  by user locally. The exchange publish and ephemeral subscription live in the
  Messaging module (`Client.PublishFanout`, `Client.SubscribeFanout`, §11A), on
  its one connection. Accepted = publisher confirm. No subscriber = a successful
  no-op; broker down = `ErrTransportUnavailable`; lost confirm =
  `ErrPublishUncertain`. A message is acked once it entered the local dispatch
  path; an invalid one is rejected without requeue (counted in
  `Stats.MalformedMessages`), so nothing loops. No durable queue, retry or DLQ:
  events during an outage or a rebind are lost by design.
- **Broker outage.** Open streams and their heartbeats stay up; publishes fail
  with `ErrTransportUnavailable`; the module is `not_ready` (so `/ready` is 503
  while the broker is down); the subscription re-creates its queue and binding
  with the client's backoff and restores delivery (`Stats.BrokerReconnects`).
- **Metrics** are `Manager.Stats()` (active connections, connections-per-user
  histogram, published/accepted/failed, slow-client disconnects, heartbeat and
  write errors, malformed messages, lifetime closes, reconnects); nForge has no
  metrics exporter, the application exports them.
- **Routing (`sse.rabbitmq.routing`).** `fanout` (default): every instance receives
  every event of every user and filters locally, so per-instance traffic grows with
  instances × events. `user`: a **direct** exchange (`<exchange>.user`) where each
  instance binds, on its ephemeral queue, the SHA-256 of every user key it has a
  stream for, and `PublishToUser` publishes with that key: an instance receives only
  the events of its own users (`Stats.DistributionReceived`,
  `nforge_sse_distribution_received_total`). The bind happens when a user's first
  local stream opens (before the 200 is written) and the unbind when the last one
  closes; both read the manager's current state under a lock, so racing joins and
  leaves converge. After an outage the new queue is bound again with every key still
  connected. Events published between a user connecting on an instance and the bind
  completing, or while the broker is down, are lost (best effort, as ever); a user
  with no stream anywhere is dropped by the broker. Choose `user` when instances ×
  event rate is high; `fanout` costs no bindings and suits small fleets.
- **Proxies.** Disable buffering and keep the read timeout above `heartbeat`:
  nginx `proxy_buffering off; proxy_read_timeout 1h; proxy_http_version 1.1;`
  (`X-Accel-Buffering: no` already asks nginx to stream). Validated by hand with
  nginx 1.30 (default buffering, `proxy_buffering off`, HTTP/1.0 upstream) and over
  real QUIC/HTTP/2 with curl; there is no automated proxy test.
- Tests: unit and `httptest` coverage in `sse/` (HTTP/1.1, HTTP/2, `-race`), the
  app-level tests through the real chain (`sse/app_test.go`, including a stream
  past 30 s), `BeginStream` on HTTP/1.1, HTTP/2 and a real QUIC client
  (`http/stream_test.go`), and `-tags integration` RabbitMQ tests
  (`sse/rabbitmq_integration_test.go`: two instances, isolation, auto-delete,
  outage through a TCP proxy, broker restart with `NFORGE_TEST_RABBITMQ_CONTAINER`).

---

## 11C. Metrics (`metrics/`)

`metrics.Module()` (ID `metrics`, section `metrics`, capability
`metrics.Capability` = `*metrics.Registry`) is optional infrastructure with no
dependencies (imports `module` only; no third-party library). The registry holds
counters, gauges and histograms with label sets and *collector functions* read at
scrape time, and renders the Prometheus text format (0.0.4).

```go
reg := module.Require(scope, metrics.Capability)
hits := reg.Counter("myapp_orders_total", "Orders by result.", "result")
hits.Inc("ok")
reg.Value("myapp_queue_depth", "Jobs waiting.", "gauge", func() float64 { return float64(depth()) })
h := reg.Histogram("myapp_job_seconds", "Job duration.", nil, "kind"); h.Observe(0.2, "email")
```

- **Opt-in and wiring.** Registering the module is the opt-in. nhttp, sse, message,
  database and cache declare the capability as *optional* and register their
  instruments when it is present. `http.routes.metrics.enabled` serves the
  registry at `path` (default `/metrics`); it needs the module (`app.New` fails
  otherwise) and is unauthenticated like `/health`: keep it on an internal
  listener or restrict it at the gateway (`env: prd` warns unless `internal_use`).
- **Built-in instruments.** HTTP: `nforge_http_requests_total{method,route,code}`
  (route is the matched *pattern*, `unmatched` otherwise, never the raw path),
  `nforge_http_request_duration_seconds{method,route}` and
  `nforge_http_requests_in_flight`; a stream that called `BeginStream` is neither
  in flight nor in the histogram. SSE: connections, users, users-by-connection-count,
  accepted/rejected, events published/accepted/failed, slow-client, heartbeat and
  write errors, malformed, lifetime closes, broker reconnects. Messaging: publishes
  by outcome, deliveries, handler failures, retries, nacks, connection readiness
  and generation. Database: pool connections by state, max, acquire counters and
  time. Cache: hits, misses and errors by driver. User IDs and tenants never appear
  in a label.
- **Cardinality.** Each instrument keeps at most 5000 label sets and drops (and
  counts, in `nforge_metrics_dropped_series_total`) any beyond it; label values
  are the caller's responsibility.
- Tests: `metrics/registry_test.go` (format, escaping, cap, concurrency),
  `http/metrics_test.go` and the end-to-end scrape in `sse/app_test.go`.

---

## 11D. Scheduler (`scheduler/`)

`scheduler.Module()` (ID `scheduler`, section `scheduler`, capability
`scheduler.Capability` = `*scheduler.Scheduler`) decides **when** a handler runs;
the handler owns the business logic, the period it covers and its idempotency.
It imports `module` and `metrics` only: no HTTP, listener, broker, database or
cache is needed, so a scheduler-only application (`logs` + `database` + `scheduler`
+ a domain module) is a valid application and `app.Run` keeps it alive until a
signal, exactly like a worker-only one. Registering the module is the opt-in; a
disabled one publishes nothing and a module that `Requires` it fails `app.New`.

```
scheduler/
├── doc.go        # package doc: expressions, DST, execution model, replicas
├── config.go     # Config, defaults, Validate (mode, timezone, max_concurrency)
├── cron.go       # parse (five fields), schedule.next (wall clock of the zone, DST rules)
├── clock.go      # the injectable clock (tests use a manual one: no sleeps)
├── scheduler.go  # Scheduler: AddJob, the single timer loop, dispatch/run, stop
└── module.go     # Module: graph, hooks, metrics
```

```go
s := module.Require(scope, scheduler.Capability)   // in the domain module's Init
err := s.AddJob("0 7 * * *", func(ctx context.Context) error {
    return countStock(ctx, db)                     // db and logger resolved through DI
})                                                 // err: ErrInvalidSchedule, ErrNeverFires, ErrNilHandler, ErrStarted, ErrClosed
s.AddJob("*/5 * * * *", h, scheduler.WithMaxConcurrency(1))   // optional per-job cap
```

- **Lifecycle.** The module is **ingress**: its Start (after every Init and every
  ordinary Start) launches the timer loop; its Stop runs before the modules the
  handlers use, so the database, cache and logger are closed only after the
  handlers returned (§3.4). Jobs are registered in Init; `AddJob` after Start is
  `ErrStarted`, after Stop `ErrClosed`, and an error returned from Init stops the
  application (a wrong expression never surfaces after the start). Stop is
  idempotent: it ends the schedule (no new firing), **cancels the context of every
  running execution** and waits for the handlers until the shutdown deadline; a
  handler that ignores its context past it yields `ErrStopTimeout`, and the runtime
  leaves the modules below it open (§3.4). Readiness: running and not stopped.
- **Expressions.** Exactly five fields (minute hour day-of-month month
  day-of-week); `*`, values, ranges `a-b`, lists, steps `*/n` and `a-b/n`; names
  `JAN-DEC` / `SUN-SAT`; weekday 7 = Sunday. Seconds, `@every` and `@daily` are
  rejected, as are `5/15` (ambiguous), leading zeros and reversed ranges. When both
  day fields are restricted a day matches if either does (Vixie); with one `*` both
  must. An expression that matches no date in 8 years (`0 0 31 2 *`) is
  `ErrNeverFires`. The parser is in-tree (about 200 lines, stdlib only): the
  repository prefers stdlib, and the DST rules and the injectable clock need
  control of `next`.
- **Time zone.** `scheduler.timezone` (IANA, default **UTC**, `"Local"` refused) is
  loaded with `time.LoadLocation` in Configure (reads the tz database, no network);
  the database's zone is never used. A minimal container needs the zone data:
  `import _ "time/tzdata"` in `main`. DST: a nonexistent wall time (spring forward)
  fires once, shifted by the gap (02:30 runs at 03:30); a repeated wall time (fall
  back) fires once, at its first occurrence, for a fixed-time job (minute and hour
  both restricted), while a job with a wildcard minute or hour keeps firing on every
  matching instant of the repeated hour.
- **Execution.** One timer loop for all jobs (no goroutine per job); each firing
  runs the handler in its own goroutine with its own context (child of the
  lifecycle, canceled by Stop). Runs are **parallel**: a slow run does not delay or
  cancel the next. No queue, no backlog, no retry, no persistence, no catch-up: a
  firing missed because the process was down, suspended or saturated is **not**
  recovered (after a suspension the next firing is simply the next matching instant
  after "now"). A returned error is logged at ERROR (`[SchedulerModule] Job failed`)
  a panic is recovered and logged with its stack, and a `context.Canceled` returned
  because the scheduler is stopping is logged at WARN (`canceled`); none of them stops
  the schedule nor the application. The handler gets no planned time: it reads the clock and picks
  its own reference period.
- **Concurrency cap.** `scheduler.max_concurrency` (0 = unlimited) is the default
  cap **per job**, `WithMaxConcurrency(n)` overrides it for one job. At the cap the
  firing is skipped, logged at WARN and counted (`outcome="skipped"`), never queued
  and never replayed, so one slow job cannot starve the others.
- **Job identity.** Each job gets a local ID `job-N` (registration order) used in
  logs and metrics; the expression is not an identity (two jobs may share it). It is
  not stable across instances: a future distributed mode needs stable identities.
- **Modes and replicas.** `mode: local` is the only mode: every instance runs every
  job, so N replicas run each job N times, with **no coordination and no
  exactly-once guarantee**. `mode: distributed` is reserved: it fails `app.New`
  (`ErrUnsupportedMode`) and never falls back to local. There is no database or
  Redis lock. Handlers must be idempotent (or run one instance), and own the
  reference period of their work.
- **Metrics.** With the metrics module: `nforge_scheduler_runs_total{job,outcome}`
  (`ok`, `error`, `panic`, `skipped`, `canceled`) and `nforge_scheduler_job_duration_seconds{job}`.
- Tests: `scheduler/cron_test.go` (parser, `0 7 * * *`, DST, monotonic over years),
  `scheduler_test.go` (a manual clock: no sleeps), `lifecycle_test.go` (shutdown
  order against a resource) and `module_test.go` (graph, config errors, a
  scheduler-only application under `app.Run`). Integration against PostgreSQL:
  `_examples/scheduler` (§14).

---

## 11E. Email (`email/`)

`email.Module()` (ID `email`, section `email`, capability `email.Capability` =
`*email.Client`) sends one message at a time over SMTP, **synchronously**. It
imports `module`, `metrics` and `telemetry` (both optional) and needs no HTTP,
database, broker or scheduler: an application with `logs` + `email` + a domain
module is valid and works under `app.Run`. It is **not** ingress. Registering the
module is the opt-in; a disabled one publishes nothing and a module that
`Requires` it fails `app.New`. `email.RenderMJML` is independent of the module.

```
email/
├── doc.go       # package doc
├── config.go    # Config, defaults, Validate, redaction (SMTPConfig Format/LogValue/MarshalJSON)
├── message.go   # Message, validation, limits, MIME encoding (build)
├── client.go    # Client: Send, lifecycle (start/ready/stop), metrics, tracing
├── smtp.go      # the unexported driver interface, newDriver, the SMTP driver
├── errors.go    # sentinels and the sanitized *Error
├── mjml.go      # RenderMJML (mjmlgo), limits
└── module.go    # Module: graph, Init, hooks
```

```go
c := module.Require(scope, email.Capability)        // in a module that declared Requires
err := c.Send(ctx, email.Message{To: "customer@example.com", Subject: "Welcome", Text: txt, HTML: html})
html, err := email.RenderMJML(ctx, mjmlSource)       // no module needed
```

- **Semantics.** `Send` returns nil only after the server accepted the message
  (positive answer to the end of DATA). That is **not delivery**: bounces and spam
  filtering happen later and nothing here observes them. No queue, retry,
  attachments, tracking or persistence. A send is allowed only after the
  application started (a `Send` in Init gets `ErrClosed`); sending is application
  work, never a side effect of Init.
- **Message.** `To` is exactly one bare ASCII mailbox (no display name, spaces or
  control characters; SMTPUTF8 is not implemented, so a non-ASCII address is
  refused, IDN domains must be punycode). `Subject` is required, UTF-8 without
  control characters, RFC 2047 encoded. `Text` only → `text/plain`; `HTML` only →
  `text/html`; both → `multipart/alternative`, text first, random boundary. Bodies
  are quoted-printable with CRLF; dot-stuffing is the SMTP writer's. Limits:
  subject 512 bytes, 4 MiB per body, 10 MiB for the encoded message
  (`MaxSubjectBytes`, `MaxPartBytes`, `MaxMessageBytes`). The sender is
  `email.from` (the name is encoded with `net/mail`), never per message.
- **SMTP transport (`smtp.go`).** One connection per send (no pool, no reuse, so
  concurrent sends need no coordination). `net/smtp` has no context, so the driver
  dials with `DialContext`, sets the connection deadline from the context and runs
  one watcher that expires the deadline on cancellation: no protocol step outlives
  ctx and the watcher always ends. `starttls` requires the server to offer
  STARTTLS and upgrades with `ServerName` = `smtp.host`, TLS 1.2+ and verification
  before AUTH or data (a server that does not offer it is `ErrUnavailable`, never
  clear text); `tls` is implicit TLS. There is no plaintext or skip-verify option.
  AUTH PLAIN only with credentials, and `net/smtp` itself refuses it without TLS.
  Without credentials no AUTH is attempted (a relay trusted by network; logged at
  INFO once).
- **Errors.** Wrap `ErrInvalidMessage` (nothing attempted), `ErrClosed`,
  `ErrUnavailable` (dial, TLS, STARTTLS, AUTH, canceled), `ErrTransient` (4xx),
  `ErrRejected` (5xx) or `ErrOutcomeUnknown` (the connection or deadline failed
  after the body was sent and before the answer: the message **may** have been
  accepted, do not resend blindly), plus the context error. `*email.Error` has the
  phase and the SMTP code; messages never carry the server's text, the recipient,
  the subject, a body or a credential. There is **never an automatic retry**.
- **Timeout.** `email.timeout` bounds the whole send; a shorter caller deadline
  wins. Cancelling or timing out ends the connection at once.
- **Lifecycle.** Init builds the client and publishes it; Start marks it
  operational (the SMTP server is first contacted by the first `Send`, so a wrong
  host fails that send, not the start); `Ready` reflects local state only (it never
  dials and never sends); `Stop` is idempotent, refuses new sends and waits for the
  ones in flight until the shutdown deadline, then cancels them (`ErrClosed` for
  them, the context error from Stop).
- **Observability.** `[EmailModule]` logs carry driver, outcome and phase only
  (never recipient or content; success at DEBUG, failure at WARN). With the metrics
  module: `nforge_email_sends_total{driver,outcome}` (`accepted`, `rejected`,
  `transient`, `unknown`, `closed`, `failed`, `invalid`) and
  `nforge_email_send_duration_seconds{driver}`; "accepted" is the only success, there
  is no "delivered". With the tracing module a span `email.send` has no message data.
- **MJML.** `RenderMJML(ctx, source)` uses `github.com/julez-dev/mjmlgo/v2` (MIT,
  pure Go, no Node.js, MJML 5): the one dependency added for it. Parity with the
  JavaScript compiler is not promised for every feature. No variable substitution or
  template storage: the caller builds the MJML and escapes untrusted values. `mj-include`
  is not resolved (it is an error: nothing reads the filesystem or the network).
  Source limit 1 MiB, output 4 MiB (`ErrMJMLTooLarge`). Rendering is CPU work that cannot
  be interrupted midway: a canceled ctx returns at once and the render finishes in the
  background, bounded by the source limit. Invalid markup is `ErrMJML`.
- **Drivers.** The transport is the unexported `driver` interface chosen by
  `newDriver`; `smtp` and `noop` exist (`noop` discards after validation and encoding, for tests and local runs; it logs a WARN at Init and its "accepted" means nothing was sent). A future provider adds `email.ses` or
  `email.sendpulse` beside `email.smtp` and a case there, without touching `Message`.
- Tests: `email/message_test.go` (MIME, injection, limits, `FuzzBuild`),
  `smtp_test.go` against `smtpserver_test.go` (an in-process server with real TLS:
  STARTTLS, implicit TLS, AUTH only over TLS, certificate mismatch, RCPT/DATA
  rejections, ambiguous completion, cancellation mid-dial and mid-protocol,
  concurrency), `client_test.go` (a fake driver, shutdown, no retry, log privacy),
  `module_test.go`, `mjml_test.go` (`FuzzRenderMJML`) and `_examples/email`.

---

## 12. Crypto (`crypto/`, package `ncrypto`)

Standalone toolbox (no internal imports, no globals, no config section, not
a module): the application builds what it needs from its own keys. The
only consumer today is the `basic` example's `crypto.go` demo, which
`main.go` leaves disabled and `crypto_test.go` runs. Dependencies:
`golang.org/x/crypto` (chacha20poly1305, argon2, bcrypt),
`github.com/zeebo/blake3`, `github.com/oklog/ulid/v2`.

```
crypto/
├── doc.go      # package doc: scope, naming, (max, min) range convention
├── crypto.go   # Crypto/MAC interfaces, ChaCha20 (XChaCha20-Poly1305), HMAC256, Hash (bcrypt, Argon2id, BLAKE3, SHA-256)
├── aes.go      # AES (AES-256-GCM with random nonces)
└── random.go   # Bytes, Key, Uint64/Int64/Int/Float64/Float32 ranges, String, ULID
```

### 12.1 Encryption and MAC

```go
key, err := ncrypto.Key()                       // 32 random bytes; keep raw, base64/hex only to store
var c ncrypto.Crypto
c, err = ncrypto.NewChaCha20(key)               // or ncrypto.NewAES(key)
ct, err := c.Encrypt(plain, aad)                // aad: authenticated, not encrypted; nil if unused
pt, err := c.Decrypt(ct, aad)                   // the same aad is required
mac, err := ncrypto.NewHMAC256(otherKey)        // dedicated key, >= 32 bytes (ErrKey); the key is copied
sig := mac.Sign(data); ok := mac.Verify(data, sig)   // constant-time comparison
```

- `Crypto` is the shared interface. The output is self-contained
  (`nonce || ciphertext || tag`), with a fresh random nonce per call.
- **ChaCha20** is **XChaCha20-Poly1305** (24-byte nonce): safe with random
  nonces at any volume, but **not** wire-compatible with the 12-byte-nonce
  ChaCha20-Poly1305. Prefer it for new data.
- **AES** is AES-256-GCM via `cipher.NewGCMWithRandomNonce` (12-byte nonce,
  16-byte tag). It needs an exact 32-byte key (`ErrAESKey`); messages over
  the GCM limit → `ErrMessageTooLarge`. Random 96-bit nonces cap a key at
  **2^32 encryptions across every instance sharing it**. Nothing counts
  them, so rotate keys before that.
- Errors are sentinels prefixed `ncrypto:` (`ErrCiphertext`, `ErrKey`,
  `ErrAESKey`, `ErrMessageTooLarge`, `ErrHash`, `ErrConfig`, `ErrBounds`,
  `ErrSize`). Short input → `ErrCiphertext`; wrong key, tampering or wrong aad → the
  AEAD's opaque `Open` error. The zero values of `ChaCha20`, `AES` and
  `HMAC256` are unusable: always use the constructors. All are safe for
  concurrent use.
- No key management: no derivation, rotation, key IDs or versioned
  ciphertexts. Use `aad` to bind a ciphertext to its context (tenant, row
  ID) so it cannot be moved between records.

### 12.2 Hashing

```go
h, err := ncrypto.NewHash(ncrypto.HashConfig{})       // zero fields → defaults; invalid → ErrConfig
enc, err := h.Argon2id(pw); ok, err := h.VerifyArgon2id(pw, enc)
enc, err := h.Bcrypt(pw);   ok, err := h.VerifyBcrypt(pw, enc)
sum := h.Blake3(data);      ok, err := h.VerifyBlake3(data, sum)   // also SHA256 / VerifySHA256
```

- Defaults: bcrypt cost 12 (allowed 10–16); Argon2id 64 MiB
  (`ArgonMemory` is in **KiB**), 3 passes, 4 threads (limits: memory from
  8·threads KiB to 256 MiB, passes 1–10, threads 1–16).
- `Argon2id` emits a PHC string (`$argon2id$v=19$m=…,t=…,p=…$salt$hash`,
  16-byte salt, 32-byte key). `VerifyArgon2id` parses strictly and checks
  the **encoded** parameters against the same ceilings before allocating,
  so a stored hash cannot request unbounded memory (salt and hash 16–64
  bytes, input ≤ 512 chars). Verification uses the hash's own parameters,
  so old hashes keep verifying after a config change (rehash on login if
  needed).
- bcrypt rejects passwords over **72 bytes** (it never truncates); verify
  also rejects costs > 16 and hashes that are not 60 chars long.
- Wrong password → `false, nil`; malformed or out-of-limit hash → `ErrHash`
  (or the bcrypt error). Password hashing is expensive by design: bound
  concurrent calls in the application (e.g. a semaphore on login).
- BLAKE3 and SHA-256 return 64 hex chars. They are **unkeyed** data hashes,
  never for passwords or authentication (use `HMAC256`).

### 12.3 Secure random

Every value comes from `crypto/rand`. `Bytes(n)` needs n ≥ 0 (else
`ErrSize`); `Key()` returns 32 bytes. The range functions take
**`(max, min)`**, the reverse of the usual order. Integers are inclusive
`[min, max]` and unbiased (rejection sampling, full int64 range); floats
are `[min, max)` from 53 random bits (finite bounds only). Equal bounds
return the bound; `min > max` → `ErrBounds`. `String(n)` returns n URL-safe
chars (`A–Z a–z 0–9 - _`, 6 unbiased bits each). `ULID()` returns 26 chars
with 80 random bits; its timestamp is public and ordering within one
millisecond is random, so it is an identifier, not a secret.

---

## 13. Helpers (`helper/`)

Small, standalone utilities that do not justify a module: each lives in
`helper/<name>/`, imports no nForge package (stdlib only unless there is a
strong reason) and has no config or module wiring.

### 13.1 `safecalc` (money)

```go
price := safecalc.New(1999, nil)                     // 19.99 as cents; nil opts → BRL()
total := safecalc.New(price.Value*3, nil)            // arithmetic on the int64 cents
m, err := safecalc.NewFromFloat(12.5, nil)           // float at the edge only; ErrInvalidAmount on NaN/Inf/overflow
usd := safecalc.MoneyOpt{Prefix: "US$", DecimalSep: ".", ThousandsSep: ","}
safecalc.New(123456789, &usd).Format()               // "US$ 1,234,567.89"
safecalc.Round(0.125, 2)                             // 0.12 (float display helper, half to even)
```

- `Money{Opt, Value}`: `Value` is **cents** (int64). Build with `New` (exact,
  preferred). Once built, an amount never goes through float64.
- `NewFromFloat` multiplies by 100 and rounds **half to even**; NaN, ±Inf
  and results outside int64 → `ErrInvalidAmount`. The float may already be
  inexact (1.005 is 1.00499… → 100 cents): take cents or decimal strings
  from the source whenever possible.
- `Format` is pure integer arithmetic, exact over the whole int64 range
  (including `MinInt64`): `Prefix`, one space (only when the prefix is
  non-empty), `-` for negatives, the integer part grouped in thousands by
  `ThousandsSep` (`""` disables grouping), `DecimalSep`, two digits,
  `Suffix` verbatim (`" €"`). `BRL()` gives `R$ 1.234,56`; it is a function so callers cannot
  change the default for everyone. `Value` counts the currency's minor units:
  hundredths by default, or `MoneyOpt.Decimals: safecalc.Places(0)` (JPY) /
  `Places(3)` (KWD, BHD), 0..18 (clamped). With 0 decimals there is no
  `DecimalSep`; `NewFromFloat` scales by 10^Decimals.
- `Round(float64, places)` is a float helper for display (half to even,
  float imprecision included: 1.005 → 1), never for money arithmetic.
- No currency arithmetic helpers (overflow-checked add, allocation/split,
  percentages) yet: callers add and multiply the int64 cents themselves and
  own overflow at their magnitudes.

### 13.2 `qrcode` (QR Codes)

```go
qr, err := qrcode.New("https://example.com/pay",       // UTF-8 text; NewBytes for binary
    qrcode.WithSize(512), qrcode.WithMargin(4),         // defaults 512 px, 4 modules
    qrcode.WithErrorCorrection(qrcode.High),            // default Medium; High when a logo is set
    qrcode.WithColors(fg, bg),                          // default black on white
    qrcode.WithRoundedModules(0.25), qrcode.WithRoundedFinders(0.15),
    qrcode.WithLogo(logoBytes, qrcode.LogoSize(0.15), qrcode.LogoPadding(1),
        qrcode.LogoBorderRadius(0.2), qrcode.LogoBackgroundRadius(0.2), qrcode.LogoBackground(true)))
png, err := qr.PNG(); svg, err := qr.SVG()
b64, err := qr.Base64(qrcode.FormatPNG); uri, err := qr.DataURI(qrcode.FormatSVG)
m := qr.Modules()                                       // [][]bool, no quiet zone: what the terminal example draws
```

- **Standalone**: no config, no module, no `app`/`nhttp`; no files, network or
  globals; bytes in, bytes out. An immutable `*QRCode` is safe for concurrent
  use; the logo bytes are copied once. Third-party imports: `golang.org/x/image`
  (CatmullRom scaling and the `vector` rasterizer of SVG logos). Test-only:
  `github.com/makiuchi-d/gozxing` (an independent decoder) and
  `github.com/boombuler/barcode` (a reference encoder the matrices are compared
  with).
- **Pipeline** (`encode.go` → `qrcode.go` layout → `render.go`): payload → own
  Model 2 encoder (numeric/alphanumeric/byte, versions 1-40, L/M/Q/H, 8 masks,
  `tables.go` block table) → `symbol{dark, fn}` (the function-module mask is
  the single source of truth for both renderers) → layout in **pixels** →
  PNG or SVG. SVG uses the same pixel units as the PNG (`viewBox="0 0 W W"`),
  so both share geometry. PNG side = `scale * (modules + 2*margin)`, `scale`
  an integer ≥ 1 (the largest that fits `WithSize`); the quiet zone defaults to
  4 modules (fewer is accepted but documented as risky).
- **Mode**: single segment (numeric if all digits, alphanumeric if the QR
  charset, else byte); no mixed-mode optimization and no ECI (UTF-8 in byte
  mode, the de facto convention).
- **Rounding** applies to data/EC modules only (`WithRoundedModules` 0..0.5, a
  fraction of the module; 0.5 is a circle). Finders are drawn as shapes with
  their exact ring / gap / core proportions (`WithRoundedFinders` 0..0.2 of the
  7-module side, capped so every finder module keeps a dark center); timing,
  alignment, format and version modules stay square. PNG draws rounded
  shapes with a signed-distance edge (one antialiased pixel, no blur inside);
  SVG uses arcs, not raster.
- **Logo**: PNG/JPEG/SVG detected by content, size ≤ 1 MiB, ≤ 4096 px per side
  and 4 Mpx; raster logos are decoded, downscaled to 512 px and re-encoded as
  PNG (EXIF orientation is **not** applied). **SVG logos** use a strict allowlist
  parser (`parseSVGLogo`): elements `svg/g/path/rect/circle/ellipse/polygon`,
  fills only (hex or basic names, `fill-opacity`/`opacity` multiplied), no
  stroke, transform, style, class, use, image, text, gradients, arcs, DOCTYPE
  or namespaced attributes; limits on elements, depth, segments and
  coordinates. The result is a shape model that both renderers redraw: **the
  source text never reaches the output**. Anything else is `ErrUnsupportedLogo`
  (errors never echo the input). The plate is drawn in the background color
  (so it needs an opaque one). The logo is laid out in pixels, centered, aspect
  preserved, `LogoSize` ≤ 0.20 of the module width, and **never covers a
  function module**: when it would, `New` moves to the next larger version (the
  alignment pattern at the center of versions 7-13, 21-27… makes this
  frequent), and returns `ErrLogoOverlap` only past version 40. Setting a logo
  selects level H unless `WithErrorCorrection` was used; a fixed level is
  never lowered (`ErrTooLarge`).
- **Tests**: `TestEncoderAllVersions` compares the matrix of every
  version/level with boombuler's and decodes samples with gozxing; style
  matrices decode at 256/512/1024 px; fuzzers `FuzzPayload` and `FuzzLogo`;
  `go test ./helper/qrcode -run '^$' -bench . -benchmem`. gozxing occasionally
  fails to *find* a valid symbol (the reference matrix fails the same way):
  treat a decoder miss as inconclusive, a wrong decoded text as a bug.
- A QR Code styled or carrying a logo is **not guaranteed to scan** everywhere:
  H recovers ~30% of codewords under ideal conditions, not 30% of the image.
  Test what you publish. No Pix/BR Code logic belongs here.

---

## 14. Examples and local stack (`_examples/`)

Eleven independent `package main` applications, each focused on one part of
the framework, each with its own `config.yaml` (only the keys it uses,
commented), `README.md` and tests. They share one pattern: `main` loads the
config, calls a `newApp(cfg, opts...)` that registers the built-in modules it
needs plus one application module (routes registered in its Init) and runs
it with `Application.Run`; the tests reuse `newApp` with `config.LoadFlags`
and an env file (a free port), `Start` it, take the server with
`app.Value(a, nhttp.Capability)` and drive the real middleware chain through
`Srv.Handler`. All listen on `127.0.0.1:8080`; app
names are `<name>-example`.

| Example | Modules | Files | Tests |
|---|---|---|---|
| `basic` | HTTPS + HTTP/3 opt-in (`config.https-http3.yaml`, `certs/`), jose (one HS256 `access` profile, demo secret inline in its config.yaml), cache (memory driver), database (PostgreSQL on; `standalone.env` turns it off), optional RabbitMQ events (`rabbitmq.env`); `http.security` + `http.cors` on | `docker-compose.yml` + `.env.example` own PostgreSQL, RabbitMQ and observability; `main.go` registers the `message` and `message-consumers` modules; `message.go` logs `user.created` and Quorum `audit` events; `POST /api/v1/users` publishes both only when enabled | previous basic coverage plus the optional RabbitMQ-enabled startup |
| `jose` | jose | `auth.go` (demo login `admin`/`admin` with `profile` + `scope`, `/public`, typed `/me`, raw `/raw/wallets` + `/raw/wallets-slice` with variadic/slice `WithScopes`, typed `/api/v1/wallets`, nested-only `/api/v1/confidential`, `/api/v1/jose/demo` JWS/JWE round trip; `protectedCalls` counter), `cmd/keygen` (key files for the commented asymmetric setup; 0700/0600, `-force`) | 200/401/403 matrix, handlers not invoked on failure, nested vs access, missing secret fails at load |
| `cache` | cache (redis; `standalone.env` → memory) | `docker-compose.yml` + `.env.example` own Redis; `routes.go` (`/api/v1/items/{key}` Set/Get/claim=SetIfAbsent/ttl/expire/Delete, `/api/v1/counters/{key}`, raw `/raw/{key}` bytes, `/api/v1/products/{id}` `GetOrLoad` over a slow `catalog`; `sendCacheError`) | shared scenarios run on memory (`main_test.go`) and on Redis (`redis_integration_test.go`, unique prefix, deletes its keys); CBOR; readiness |
| `database` | database (+ tenancy column, `http.tenant.source: header`) | `docker-compose.yml` + `.env.example` own PostgreSQL; `users.go` (Squirrel/pgx CRUD, filters, batch in `Transaction`, `sendDBError`), `notes.go` (typed `Opts{Tenant}` routes and raw `server.TenantMiddleware()`, `ForContext`/`TenantEq`/tenant `Transaction`; `ensureNotesSchema` builds RLS when `mode: rls`), `rls.env` + `rls-setup.sql` (role `app_tenant`) | config + rls overlay load (unit); CRUD and tenant isolation 403/400/404 (integration) |
| `qrcode` | none (`helper/qrcode` only; not an `app`) | `main.go` flags (`-payload` default `teste123`, `-out`, `-size`, `-logo`, `-terminal`, `-terminal-only`, `-invert`), `terminal.go` (half-block text QR, 4-module quiet zone, no ANSI), `gallery.go` (PNG, SVG, `base64.txt`, `datauri.txt`, static `index.html`), `assets/logo.svg` | terminal geometry and decode round trip, `-terminal-only` writes no files, empty payload fails, gallery PNGs decode |
| `static` | http (`Static`, `SPA`, `StaticFS`, `SPAFS`), codec, logs | `main.go` (`-embed`, `-dist`, `-public`; `frontend` module: typed `/api/health`, `Static("/assets")`, `SPA("/")` with `WithSPAReserved("/api")` and `WithSPAImmutableDir("build")`), `frontend/dist` (a tiny hand-written build, no Node), `public/` | `/`, `/dashboard`, `/assets`, `/api/health`, 404s, cache headers, directory and embed modes, a missing entry point fails the start |
| `sse` | http, jose, sse (memory; `config.rabbitmq.yaml` for RabbitMQ) | `main.go` (cookie login, cookie→bearer middleware, `GET /notifications/stream`, `POST /notify`, `/stats`, `/slow` with `nhttp.Timeout`), `web/index.html` (`EventSource`, `reauth` handling), `docker-compose.yml` + `.env.example` own RabbitMQ | two tabs of one user receive, another user does not, 401/403, route timeout opt-in, the RabbitMQ config builds |
| `email` | logger, email + a `welcome` module; no HTTP, no database, no broker | `main.go` (`-to`/`$EMAIL_TO`; start, render `welcome.mjml`, one `Send`, shutdown; exit 0/1/2), `config.yaml` (`${SMTP_*}` placeholders), `.env.example` | graph without HTTP, the fixture renders, missing/invalid recipient sends nothing |
| `scheduler` | logger, database, scheduler + a `stock` domain module; no HTTP, no listener, no broker | `docker-compose.yml` (PostgreSQL, loads `schema.sql`); `main.go` (`-cron` overrides the expression for a quick local check), `stock.go` (`AddJob("0 7 * * *")`, `SELECT COUNT(*) FROM stock` through `db.Query`/`db.Executor`) | job and DI unit tests; `-tags integration` against PostgreSQL |
| `message` | message (RabbitMQ, worker-only) | `docker-compose.yml` + `.env.example` own RabbitMQ; `-mode producer|consumer|reprocess`; Classic priority, Quorum, automatic/manual ACK, DLQ and retry | unit validation plus RabbitMQ integration tests |

Each example keeps its environment examples locally; examples with Docker
dependencies have a per-folder Compose file (JOSE needs no services). Local
`.env` files are ignored. The complete example defines PostgreSQL, RabbitMQ and
the local observability stack; cache defines Redis; database defines
PostgreSQL; message defines RabbitMQ. Observability configs live in
`_examples/observability/`.

Commands (repository root):
```bash
cp _examples/complete/.env.example _examples/complete/.env
docker compose --env-file _examples/complete/.env -f _examples/complete/docker-compose.yml up -d
go run ./_examples/complete -config _examples/complete/config.yaml [-envfile _examples/complete/standalone.env]
# HTTPS :8443/tcp + HTTP/3 :8443/udp + 308 redirect from :8080 (run from the repo root: cert paths are relative to the CWD)
go run ./_examples/complete -config _examples/complete/config.https-http3.yaml -envfile _examples/complete/standalone.env
export JWT_SECRET=$(openssl rand -hex 32) JWT_ENCRYPTION_SECRET=$(openssl rand -hex 16)
go run ./_examples/jose -config _examples/jose/config.yaml
go run ./_examples/cache -config _examples/cache/config.yaml [-envfile _examples/cache/standalone.env]
go run ./_examples/database -config _examples/database/config.yaml [-envfile _examples/database/rls.env]
cp _examples/message/.env.example _examples/message/.env
docker compose --env-file _examples/message/.env -f _examples/message/docker-compose.yml up -d
go run ./_examples/message -config _examples/message/config.yaml -mode consumer|producer|reprocess
docker compose -f _examples/scheduler/docker-compose.yml up -d    # PostgreSQL with the stock table
go run ./_examples/scheduler -config _examples/scheduler/config.yaml [-cron "* * * * *"]   # scheduler-only app: no HTTP
set -a; . _examples/email/.env; set +a    # copy .env.example first; placeholders only, never commit real credentials
go run ./_examples/email -config _examples/email/config.yaml -to you@example.com   # renders MJML, one SMTP send (accepted, not delivered)
make dev                                                  # air: hot reload of _examples/complete (needs PostgreSQL)
make run-graph                                            # complete with -tags nforge_graph and /graph on (needs PostgreSQL)
make run-graph-standalone                                 # the same without Docker
go run ./_examples/sse -config _examples/sse/config.yaml              # SSE, memory driver (export JWT_SECRET first); config.rabbitmq.yaml needs the compose RabbitMQ
go run ./_examples/static -config _examples/static/config.yaml [-embed]   # API + SPA + public files on :8080
go run ./_examples/qrcode [-terminal-only] [-payload https://example.com]   # QR in the terminal + gallery in ./qrcode-output
```
Then `http://localhost:8080/docs`; Grafana at `http://localhost:3000`.

---

## 15. Building and testing

```bash
go build ./...
go vet ./...
go test -tags nforge_graph ./http/...   # the module graph page, compiled only with this tag
go test ./...                    # unit tests (app, cache, codec, config, crypto, database, helper (safecalc, qrcode), http, jose, logs, module, telemetry, architecture)
go test -race ./...

# the examples (`./...` skips `_` directories, so name each one):
EXAMPLES='./_examples/basic/... ./_examples/complete/... ./_examples/jose/... ./_examples/cache/... ./_examples/database/... ./_examples/message/... ./_examples/qrcode/... ./_examples/static/... ./_examples/sse/... ./_examples/scheduler/... ./_examples/email/...'
go vet $EXAMPLES
go test -race $EXAMPLES

# jose fuzzers (smoke runs) and benchmarks:
for f in FuzzValidateJWT FuzzStrictJSON FuzzParseCompact FuzzKeyImport; do
  go test ./jose -run '^$' -fuzz "^$f\$" -fuzztime 15s; done
# codec: streaming JSON presence check vs the tree reference (run after any
# change to codec/presence*.go):
go test ./codec -run '^$' -fuzz '^FuzzPresenceJSON$' -fuzztime 60s
# codec: hostile XML and CBOR through a strict registry:
go test ./codec -run '^$' -fuzz '^FuzzDecodeXML$' -fuzztime 60s
go test ./codec -run '^$' -fuzz '^FuzzDecodeCBOR$' -fuzztime 60s
go test ./jose -run '^$' -bench . -benchmem

govulncheck ./...                # required before changing security dependencies
gosec ./...                      # must be built with this Go: an older build cannot read the export data,
                                 # reports "could not import" and silently analyzes much less

# golangci-lint v2.15.0+ with the repo's .golangci.yml; older releases
# (v2.13.x) cannot read go1.27.2 export data and fail every package with
# typecheck "export data version" errors, which GOTOOLCHAIN=local or
# rebuilding the same release does not fix:
golangci-lint run ./... $EXAMPLES

# integration tests against a real PostgreSQL (build tag `integration`); the
# tenancy tests create/drop a role, so the URL's user must be able to CREATE ROLE:
NFORGE_TEST_DATABASE_URL='postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable' \
  go test -tags integration ./database/...
# application lifecycle against the real services (rollback, drain and shutdown
# are checked from pg_stat_activity); needs both URLs below:
NFORGE_TEST_DATABASE_URL=... NFORGE_TEST_REDIS_URL=... go test -race -tags integration ./app/

# cache integration tests against a DISPOSABLE Redis (unique prefixes, only
# their own keys are deleted, never FLUSHDB):
NFORGE_TEST_REDIS_URL='redis://localhost:6379/15' go test -race -tags integration ./cache/

# RabbitMQ integration tests use a dedicated local prefix and delete only their own queues:
NFORGE_TEST_RABBITMQ_URL='amqp://nforge:nforge@localhost:5672/' go test -race -tags integration ./message/ ./sse/ ./_examples/message/...

# the examples against the compose stack (§14):
NFORGE_TEST_DATABASE_URL='postgres://app:app@localhost:5432/app?sslmode=disable' \
NFORGE_TEST_REDIS_URL='redis://localhost:6379/15' \
  go test -race -tags integration $EXAMPLES
```

Integration tests skip when `NFORGE_TEST_DATABASE_URL` /
`NFORGE_TEST_REDIS_URL` is unset. Redis failure modes that a real server
cannot produce on demand (lost replies, pool timeouts, outage + recovery
of the same client) are unit-tested with a minimal RESP fake in
`cache/redis_test.go`; the integration file also drops a real `INCRBY`
reply through a proxy and asserts it ran exactly once. Memory expiration
tests use the private injected clock (`withClock`), never sleeps. Tests are
standard `testing` (table-driven where it fits), in the same package as the
code (white-box), no assertion library. HTTP tests use `httptest`. Every
behavior change needs a test; every bug fix needs a regression test.

CI (`.github/workflows/ci.yml`) runs on every pull request and on `main`: gofmt,
build and vet with and without `nforge_graph`, golangci-lint, the race tests, the
examples, the integration tests against real PostgreSQL and Redis, and
`govulncheck`, which fails on a reachable vulnerability only. The standalone
`gosec` binary is not a CI step: it ignores `//nolint` and reports the reviewed
false positives, so gosec gates through golangci-lint and its line-level
justifications. Run the binary by hand in an audit.

Before finishing any change: `gofmt` clean, `go vet ./...` and golangci-lint clean,
`go test ./...` green, and the examples build and their tests pass
(commands above). JOSE security behavior needs negative tests (tampering, wrong
algorithm/kid/typ, boundaries), not only round trips.

---

## 16. Conventions and rules for agents

### Language
- **Everything is in English**: code comments, log messages, error strings,
  API response texts, test names/messages, docs, config comments, Grafana
  dashboard labels, commit messages. Never add non-English text (test
  fixture *data* such as `"São Paulo"` is fine).
- Communicate with the user in the language they write in; the code stays English.

### Code style
- Idiomatic, modern Go; `gofmt`. Doc comments on every exported identifier,
  written as full sentences explaining **why/constraints**, not restating the
  code. Match the existing comment density.
- Lint findings that are safe on purpose get a line-level
  `//nolint:<linter> // <why>`; never a bare `//nolint`, never file-wide,
  and `.golangci.yml` is not loosened to silence one case.
- Errors: wrap with `%w` and context (`"parse postgres pool config: %w"`);
  exported sentinel errors + typed errors with `Is` methods for anything a
  caller needs to branch on. Lowercase messages, no trailing punctuation,
  package prefix for library errors (`codec: …`, `database: …`, `nhttp: …`).
- Programmer errors detected at startup/registration **panic** with a clear
  message (`nhttp: GET /x: …`); runtime/request errors are returned.
- Startup failures of enabled modules are fatal: `Application.Start` (and
  `Run`) returns the error after logging `[Fatal ERROR] …` and stopping what
  had been initialized (so OTel batches are flushed). Library code never
  calls `os.Exit`: only the `main` package chooses the exit code (the
  examples print to **stderr**, whatever the logging configuration, and exit
  1). The bootstrap logger (`app.WithLogger`, default WARN to stderr) reports
  what happens before or after the configured logger exists.
- Keep the hot path allocation-conscious; cache reflection per type with
  `sync.Map`.
- Inside `nhttp`, import `net/http` as `stdhttp`; the router as `router`.
- Configuration is YAML with `snake_case` keys; document the default in a
  trailing comment on the struct field.
- Security defaults are on and must stay on: HTTP server transport timeouts
  (never zero; `handler_timeout` is the one opt-in deadline, streams release
  theirs only through `BeginStream`), SSE streams only for a validated identity
  with bounded queues, limits and a per-write deadline, DTD blocked, payload limit,
  no `WithProcess()` resource, redacted DB config, validated docs accent,
  per-render docs CSP nonce, anchored CORS patterns and no credentials with
  a wildcard origin,
  no DB error details to clients; in JOSE: restricted suite (default HS256,
  never `none`), allowlists only from config, mandatory `kid`/`typ`, strict
  JSON, minimum secret sizes, owner-only key files, no key
  generation at startup, sanitized errors, static auth responses, tokens
  never logged or stored in the context; in tenancy: transaction-local
  tenant setting only, fail-closed `TenantEq`/`ForContext`, startup
  `CheckTenancy`, tenant values never logged; in the cache: no mutation
  replay, no driver fallback, tenant required before any I/O when tenancy
  is on, injective namespace escaping, bounded memory and loads, TLS 1.2+
  with verification (no skip option), redacted URL/password/CA path,
  sanitized errors (no keys, tenants, values or Redis text); in crypto:
  AEAD only with a fresh random nonce per message, minimum key sizes,
  password hashes verified against bounded parameters before allocating,
  bcrypt input never truncated, constant-time comparisons.
- `jose` uses `encoding/json` for protocol JSON on purpose (headers, claims,
  JWKs are JSON whatever the app codec is); business code still goes through
  the codec.

### Process
- Don't add dependencies without a strong reason; prefer stdlib.
- Keep every example working and representative when public APIs change;
  each one stays focused on its module (§14) — don't grow `basic` again
  (its minimal login, cache and database routes are the exception: one
  small file each; anything beyond Set/Get/Delete or a plain CRUD stays in
  the module's own example).
- Don't edit generated/ignored artifacts (`_examples/*/tmp/`).
- Files `database.md`, `cache.md`, `encoder.md` (if present) are temporary
  design specs slated for deletion: don't update or rely on them; the code
  and this file are the reference.
- Never compile a regexp or parse a template at package init in a package that
  every binary links, for a feature that is off by default: it was measured as
  the main cost of the bootstrap. Parse on first use.
- Update this AGENTS.md whenever you change architecture, config schema,
  public API, or a behavior described here, and the affected `README.md` files
  in the same change. This file is the only reference: `llms.txt` is an index and
  changes only when a package or an example is added or removed.
- Do not cite a dependency's version in prose: `go.mod` is the source, and
  `docs_test.go` fails on any `vX.Y.Z` that it does not list.
  `docs_config_test.go` fails when a key of a module section is missing from §4.2.

---

## 17. Known gotchas (read before debugging)

1. **CWD matters**: default `config.yaml` and the overlays
   (`config.<env>.yaml`) are resolved from the working directory.
2. **Overlay beats env file**; `${VAR}` reads the real environment, the env
   file does not feed `${VAR}`.
3. **`env` casing**: overlay selection is case-insensitive, log level is not
   (`PRD` → overlay applied but DEBUG log level). Use lowercase.
4. **A disabled module publishes nothing**: with `database.enabled: false` a
   module that `Requires` the database fails `app.New`
   (`ErrDisabledDependency`); one that declares it `Optional` gets
   `ok == false` from `module.Lookup` and must not use it. Readiness lists
   only the enabled modules.
   If Docker/Postgres is down while the database is *enabled*, the app does
   not start at all (fatal startup ping).
5. **No request_id without tracing** (§6.1).
6. **Typed handler wrote JSON but the route is XML**: output is still XML —
   the route codec decides. Raw `w.Write` bytes must be in the route codec.
7. **Response validation 500s**: a typed route returning a value that doesn't
   match the declared response type, misses a `codec:"required"` field, or
   fails `Validate()` responds 500 `"failed to build the response"`; look for
   the ERROR log `"invalid handler response"`.
8. **OpenAPI documents success as `200`** even if the handler sends 201/204.
9. **Middleware or routes after serving** panic in go-ams-router; register
   everything before `Start` (tests that need another route config need a
   new server).
10. **`Application` and `Server` are used through a pointer** (they hold
    mutexes); a `Server` built with `nhttp.New` without a Logger discards.
11. **Codec default registry** is json+cbor+xml permissive and is replaced only
    with `app.WithProcessGlobals`; code outside a request that needs the
    application's formats and limits declares `codec.Capability`. Tests that
    install a default restore it with `SetDefault` afterwards.
12. **Tracing is its own module**: registering `telemetry.Module()` exports
    spans unless `tracing.otel.enabled: false` (default true), whatever
    `logging.output.otel.enabled` says (that switch is the OTel *log* export
    now). An app that runs without a collector disables both
    (`standalone.env` in the complete example).
13. **`IsPrivate` is enforced** on typed routes: registering one with jose
    disabled, without `AuthProfile`/`http.auth.default_profile`, or with
    `http.docs.auth: none` panics at startup. On `AddDocRoute` it is still
    documentation only — raw routes need `Authenticate` explicitly.
14. **No jose service when `jose.enabled: false`**: `Authenticate(nil, …)`
    panics. Unknown YAML keys fail at `app.New` in every section.
15. **JOSE defaults to HS256**: a key without `algorithm` is HS256 (so a
    `dir` key must say `algorithm: dir`, and an EdDSA/RSA file must declare
    its algorithm). A token whose `typ`, `kid` or `alg` does not match the
    chosen profile is a 401 even if its signature is valid (e.g. an access
    token on a nested route).
16. **Key file permissions**: private/secret key files readable by group or
    others make startup fail (`ErrInvalidKey`); keep them `0600`.
17. **Realm differs between raw and typed auth**: typed routes use
    `http.auth.realm`; raw `Authenticate` uses `nforge-api` unless
    `WithRealm` is passed.
18. **RLS app sees no rows**: in rls mode the unscoped `*database.DB` sees nothing
    in tenant tables — use `ForTenant`/`ForContext`. If *everything* is
    visible instead, the role is superuser/BYPASSRLS or owns the table
    without `FORCE` (list the table in `tenancy.tables` so startup catches it).
19. **`COPY FROM` fails on RLS tables** (`0A000`): a PostgreSQL limitation,
    not a framework bug.
20. **No cache when `cache.enabled: false`** (see 4). With the cache enabled and
    Redis down **at startup**, the app does not start (like the database);
    later outages return errors and readiness 503 until Redis is back.
21. **Cache needs a tenant when `database.tenancy.enabled`** (even with
    `database.enabled: false`): code paths without `nhttp.Tenant()` /
    `Opts.Tenant` / `database.WithTenant` get `ErrTenantRequired` (jobs
    must put a tenant in the ctx). `Ping` is the only tenant-free call.
22. **`ErrOutcomeUnknown`** (Redis) means the write may have been applied:
    do not blindly retry non-idempotent operations such as `Increment`.
23. **Changing `cache.codec`** (or tightening a cached type) makes old
    object entries fail `Get` with `ErrDecode`; `GetOrLoad` reloads them
    (gotcha-free, one WARN each), but `Get`/`SetIfAbsent` callers should bump
    `cache.prefix` or let them expire. Counter
    keys are decimal text: `Get` on them decodes JSON numbers, but CBOR
    objects are never integers.
24. **`GetOrLoad` does not cache loader errors** and returns them unchanged;
    a Redis read failure does not call the loader (by design).
25. **`ncrypto` ranges are `(max, min)`**: `ncrypto.Int(10, 1)` is 1..10;
    `Int(1, 10)` returns `ErrBounds`. `ChaCha20` is the X variant (24-byte
    nonce), so other ChaCha20-Poly1305 implementations cannot read its output.
26. **Long or streaming responses cut at 30 s**: `http.server.write_timeout`
    (default 30s) bounds every ordinary response, and `read_timeout` and the HTTP/3
    `max_stream_timeout` bound the request. A long-lived response calls
    `nhttp.BeginStream(w, r)` first (what `sse.Handler` does) and then sets a
    short write deadline before every write. The transport timeouts cannot be
    disabled (0 means the default); `handler_timeout` is **off** unless positive.
27. **`http.server.port` is optional** (default 8080), like `tls.port` and
    `http3.port` (8443). A config with no `http.server` section loads.
28. **Redirect needs a trusted host**: `tls.redirect_http: true` with a
    non-loopback `http.server.host` fails unless `tls.redirect_host` is set;
    the redirect never uses the request's `Host`.
29. **QUIC is UDP**: opening only TCP/8443 in a firewall leaves HTTP/3 dead
    (clients fall back to HTTPS through `Alt-Svc`). An HTTP/3-only server is
    not discoverable by browsers. `curl --http3-only` needs a curl build with
    HTTP/3; `-k` is for the demo certificate only.
30. **The example certificate is public**: `_examples/complete/certs/localhost.key`
    is committed on purpose. Never deploy or trust it; the framework has no
    default certificate.
31. **A module that needs a disabled one fails `app.New`** with
    `module.ErrDisabledDependency`; it is never switched on for you.
    `http.auth.default_profile` with jose off fails the same way. A section of
    a module the application did not register fails too: YAML never activates
    a module.
32. **`module.Lookup` panics on an undeclared capability** (and on a handle of
    another type): add it to `Requires` or `Optional`, or the graph would not
    show the dependency. In Init the runtime turns the panic into a start
    error with rollback.
33. **`/graph` needs `-tags nforge_graph` and an application**: without the
    tag, enabling the route panics at `nhttp.New`; a server built with
    `nhttp.New` without `Dependencies.Status` panics too.
34. **Init must not accept work, Start must not block**: a module that starts
    consuming in Init could run a job before a handler it needs is registered;
    a Start that serves inline never returns and the start never completes.
    Serve in a goroutine the module owns and report its end with
    `Scope.ReportFailure`.
35. **`codec` needs the `jsonv2` Go experiment**: it imports
    `encoding/json/jsontext` (the presence walker). Go 1.27 enables it by
    default; `GOEXPERIMENT=nojsonv2` makes `go build ./codec` fail with
    "build constraints exclude all Go files".
36. **RabbitMQ confirms do not mean exactly-once**: publisher confirms and
    consumer ACKs provide at-least-once behavior; consumers of side effects
    must be idempotent. A confirmed publish may still duplicate if a process
    loses the reply before it records the outcome (`ErrPublishUncertain`).
37. **RabbitMQ queue declarations are immutable**: Classic/Quorum type,
    priority and dead-letter arguments must match the existing queue. nForge
    never deletes or converts a queue; perform a manual migration before
    changing its declaration.
38. **Priority is Classic-only and does not guarantee ordering**: priorities
    are 1..10, do not preempt already delivered messages, and consumer
    concurrency/prefetch changes observed order. Quorum queues reject priority.
39. **`message.Total().Processing` is unknown**: AMQP passive declarations
    report ready counts only. nForge does not call RabbitMQ's Management API.
40. **Message transport retry is best effort**: `Delivery.Attempt` comes from
    a producer-writable header and is not a durable business retry count. A
    failure after confirm but before ACK can duplicate a retry delivery.
41. **Message startup needs a live broker only when enabled**:
    `message.enabled: false` publishes nothing and does not connect; enabled
    modules fail startup if RabbitMQ or topology declaration is unavailable.
42. **Shutdown shares the app deadline**: a slow consumer handler can consume
    the default 15 s global shutdown window; increase the app shutdown timeout
    when handlers legitimately need longer to finish.
43. **The root SPA owns the router's `NotFound`**: `Server.SPA("/", ...)` (or
    `Static("/", ...)`) replaces the NotFound handler, once per server, and
    answers only GET/HEAD navigations (`Accept: text/html`, no file extension).
    Reserve every API prefix with `WithSPAReserved` (the built-in routes are
    reserved for you): an unmatched `GET /api/x` with `Accept: text/html` gets the
    entry point otherwise. `Static`/`SPA` return errors (bad prefix, missing
    directory or `index.html`, an overlapping mount or route) and, like routes,
    must run before the server serves; a route added under a mounted prefix after
    the mount fails `Start` (§7.14).
44. **SSE has no replay**: an event published while the user is offline, on
    another instance during a broker outage, or while an instance re-binds its
    queue is gone. `Last-Event-ID` is only validated; clients reload state on
    reconnect. `PublishResult.Accepted` is never browser receipt.
45. **`EventSource` cannot send `Authorization`**: authenticate it with an HttpOnly
    SameSite cookie that a middleware lifts into the bearer header (see
    `_examples/sse`), never a token in the URL. An expired or revoked token only
    stops a stream at its next reconnect (`sse.max_lifetime`), and a 401 on the
    reconnect makes the browser stop retrying: the page must sign in again.
46. **Behind a proxy the SSE origin check sees the proxy's host**: with no
    `sse.allowed_origins` only the request's own origin passes (`Origin` host
    equals `Host`), so list the public origins when a gateway rewrites `Host`.
    CORS headers for credentialed cross-origin streams come from `http.cors`
    (explicit origins, never `*`), not from `sse`.
47. **`sse` with the rabbitmq driver makes `/ready` 503 during a broker outage**
    (the module is critical), while open streams stay up. Probe accordingly.
48. **The SSE user key is not the bare subject**: `PublishToUser` takes
    `sse.UserKey(principal)` / `sse.UserKeyOf(issuer, tenant, subject)`. A
    publisher that passes a plain subject reaches nobody.
49. **CORS must name its origins**: `http.cors.enabled` with no `origins` and no
    `origin_patterns` fails `app.New`; every website is allowed only with
    `origins: ["*"]` or `allow_any_origin: true`.
50. **Defaults that were tightened**: `codec.max_payload_bytes` is 1 MiB (was 5),
    duplicate JSON members are refused, `handler_timeout` is off, tenant IDs are any
    RFC 9562 UUID. `message` queue `max_attempts` counts handler invocations.
51. **A message without a dead-letter queue is dropped when its handler fails**:
    declare `dead_letter: true` (and `retry`) or say `on_failure: drop`.
52. **Graceful deploys need `http.server.shutdown_delay`**: without it the
    listener closes at SIGTERM and a load balancer that has not yet removed the
    instance gets connection refused. Set `app.WithShutdownTimeout` above
    `shutdown_delay` plus the 10 s drain.
53. **The scheduler runs every job on every replica and never catches up**: local
    mode has no coordination, no queue, no retry and no persistence, so N
    instances run a job N times and a firing lost to downtime, suspension or the
    concurrency cap is gone. Make handlers idempotent and let them compute their own
    reference period. `scheduler.mode: distributed` fails `app.New`.
54. **Scheduler jobs are registered in Init and need the zone data**: `AddJob`
    after Start is `ErrStarted`; a bad expression returned from Init fails the
    start. `scheduler.timezone` needs the tz database: add `import _ "time/tzdata"`
    to `main` for distroless/scratch images. Expressions have exactly five fields;
    `@every`, `@daily` and seconds are rejected. A handler must honor its `ctx`: Stop
    cancels it and waits only until the shutdown deadline.
55. **Email success is "accepted", not "delivered"**: `Send` returning nil means the
    SMTP server took the message. `ErrOutcomeUnknown` means it may have been accepted
    (the connection or deadline failed after the body): resending can duplicate the
    mail, and nForge never retries. A `Send` in Init is `ErrClosed`; send after the start.
56. **Email has no plaintext mode and a strict recipient**: `starttls` fails
    (`ErrUnavailable`) when the server does not offer STARTTLS or its certificate does not
    verify against `smtp.host` (use the name on the certificate, not an IP, unless the
    certificate has that IP); there is no skip-verify option. `To` is one bare ASCII
    address: `Name <a@b>`, two recipients and non-ASCII local parts are
    `ErrInvalidMessage`. `email.RenderMJML` does not substitute variables or resolve
    `mj-include`; escape untrusted values before building the MJML.
