# AGENTS.md — nForge (Nano Forge)

This file is the single source of truth for any AI agent (or human) working on
this repository. Read it fully before changing code. It explains **what nForge
is, how every package works, how the pieces fit together at runtime, the
conventions the code follows, and the non-obvious traps**. If something here
disagrees with the code, the code wins — fix this file in the same change.

---

## 1. What nForge is

nForge (module `github.com/ams-soft/nforge`, by AMS Software e Consultoria
LTDA) is a small, opinionated **Go framework for building HTTP APIs**. An
application gets, from one `config.yaml`:

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
  - built-in health/readiness routes and graceful shutdown.
- **PostgreSQL access** via `pgx/v5` pool + Squirrel query builder, with a
  transaction helper and optional **multi-tenant scoping** (Row-Level
  Security or tenant column) plus tenant middlewares in `nhttp`.
- **JOSE** (`jose/`): compact JWS/JWE and JWT issuance/validation through
  explicitly configured profiles, plus **bearer authentication** in `nhttp`
  (`Authenticate`, scopes, `Principal`, enforced `IsPrivate` typed routes)
  and a public **JWKS** endpoint.
- **Cache** (`cache/`): one non-generic `*cache.Cache` (`deps.Cache`) on
  Redis (go-redis) or a bounded in-process LRU, chosen by config: codec
  objects, raw bytes, counters, TTLs, NX, cache-aside `GetOrLoad`, tenant
  namespacing through the framework tenant context, readiness and LIFO
  shutdown.
- **Crypto** (`crypto/`, package `ncrypto`): standalone primitives with no
  config or `Deps` wiring: AEAD encryption (XChaCha20-Poly1305, AES-256-GCM),
  HMAC-SHA256, password hashing (bcrypt, Argon2id), data hashing (BLAKE3,
  SHA-256) and secure random values (bytes, keys, ranges, strings, ULIDs).
- **Transports** (`http.server.tls`, `http.server.http3`): plain HTTP by default
  (`:8080`), opt-in HTTPS over TCP (`:8443`, TLS 1.3 minimum) and HTTP/3 over
  QUIC/UDP (`:8443`), one shared router, an optional redirect-only HTTP
  listener (§7.12).
- **Helpers** (`helper/`): small standalone utilities; today `safecalc`,
  monetary amounts in integer cents with exact display formatting.

Framework version: `version` in the **module root** `config.yaml`
(currently `0.1.2`), embedded at build time and exposed as `nforge.Version`
(`version.go`). This file is *not* the application's config. Releases are
git tags `vX.Y.Z` on `main` (published on pkg.go.dev through the Go module
proxy); bump this `version` in the commit that gets tagged, so
`nforge.Version` matches the tag, and add the release to `CHANGELOG.md`.
Never move or recreate a published tag: the module proxy keeps the first
content it saw for a version forever; fix forward with a new patch release.

Toolchain: **Go 1.27**, pinned to `toolchain go1.27.3` in `go.mod` (stdlib
security fixes; `GOTOOLCHAIN=auto` downloads it; go-ams-router v1.1.0 itself
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
├── config/             # app configuration: types, loading, env file, defaults, validation, ByteSize
├── deps/               # dependency container (Deps) + bootstrap (NewDeps) + LIFO shutdown
├── logs/               # slog setup: terminal (tint) + OTel handlers, multi/leveled/context handlers
├── telemetry/          # OTel resource, TracerProvider setup, custom trace-id generator
├── codec/              # multi-format encode/decode (JSON, CBOR, XML) + Registry
├── database/           # PostgreSQL: Config, pool, DB (Executor + Squirrel), transactions, tenancy (RLS/column)
│   └── internal/rlsexec/   # tenant-scoped Executor of rls mode (set_config batches) + SetTenantSQL
├── jose/               # standalone JOSE: config, keys/JWK/JWKS, compact JWS/JWE, JWT, Service lifecycle
│   └── internal/strictjson/  # strict protocol JSON: UTF-8, depth, duplicate members, trailing data
├── cache/              # standalone cache: Config, Open, Redis + memory drivers, keyspace/tenant, GetOrLoad flights
├── crypto/             # package ncrypto: AEAD (XChaCha20, AES-GCM), HMAC, password/data hashes, secure random
├── helper/
│   └── safecalc/       # money in integer cents: New, NewFromFloat (half-even), Format, BRL
├── http/               # package nhttp: only the public surface at the root
│   ├── server.go       # ServerHttp, NewHTTPServer, middleware loading, Start
│   ├── transport.go    # StartContext: HTTP/HTTPS/HTTP3 listeners, redirect-only handler, Alt-Svc, shutdown
│   ├── routes.go       # built-in routes: health/readiness, docs + OpenAPI spec, JWKS (collision check)
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
│       ├── authn/      # bearer transport, WWW-Authenticate challenge, scope/realm/header syntax
│       ├── jwks/       # JWKS handler (GET/HEAD, ETag, 304)
│       ├── tracing/    # Tracing middleware + request_id = trace_id
│       └── accesslog/  # Logger middleware + statusWriter
├── _examples/          # runnable examples (§14): basic, jose, cache, database + the local stack
│   ├── docker-compose.yml  # PostgreSQL, Redis, OTel Collector, Tempo, Loki, Grafana
│   └── observability/      # collector/tempo/loki configs + Grafana provisioning
├── README.md / LICENSE # project overview; MIT, AMS Software e Consultoria LTDA
├── llms.txt            # llmstxt.org index + condensed API reference for LLMs (kept in sync with this file)
├── .air.toml           # hot reload of _examples/basic (gitignored)
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
- `.gitignore` excludes `.air.toml`, `_examples/*/tmp/`,
  `_examples/jose/keys/`, `.idea/`, `Makefile`, `sandbox*` and `./basic`
  (the binary of `go build ./_examples/basic`).

### Package dependency graph (imports)

```
nforge (root) ───────────────────────────────┐
database  (no internal deps)                 │
codec     (no internal deps)                 │
jose      (no internal deps; go-jose/v4 behind unexported adapters)
cache ──► codec (go-redis/v9 behind unexported adapters)
ncrypto   (no internal deps; x/crypto, blake3, ulid)
helper/*  (no internal deps; stdlib only)
config ──► database, jose, cache (their Config types are embedded in config.Config)
telemetry ──► config
logs ──► config, telemetry, go-ams-router/middleware (GetRequestID)
deps ──► nforge, config, codec, database, jose, cache, logs, telemetry
nhttp ──► config, database, deps, codec, jose, cache (tests), telemetry, go-ams-router, quic-go (http3)
```

Keep it acyclic. `database`, `codec`, `jose`, `ncrypto` and `helper/*` must stay free of internal
imports (they are reusable standalone); `cache` may only import `codec`.
`config` may only depend on `database`, `jose` and `cache` (for their
standalone config types). A package may import its own `internal/`
subpackages, which follow the parent's rules. `jose/imports_test.go`
enforces these rules (internal subpackages included).

---

## 3. Runtime lifecycle (how an app boots and stops)

Canonical `main` (see `_examples/basic/main.go`):

```go
cfg, err := config.Load()                 // 1. flags -config / -envfile, layered load
dps := deps.NewDeps(*cfg)                 // 2. logger → tracing → banner → codec → database → jose
server := nhttp.NewHTTPServer(dps)        // 3. router + essential middlewares + docs + jwks + health
server.AddGlobalMiddleware(mw)            // 4. optional app middlewares...
server.LoadGlobalMiddleware()             //    ...applied with r.Use
server.Typed().Route("/api/v1", func(api *nhttp.TypedRouter) { ... }) // 5. routes
server.Router().Get("/raw", handler)      //    plain routes also allowed
if err := server.Start(); err != nil {    // 6. blocks until SIGINT/SIGTERM
    server.Shutdown(); os.Exit(1)
}
```

### 3.1 `deps.NewDeps(cfg)` — order matters

1. **Logger** (`setupLogger`): if terminal or OTel logging is enabled,
   `logs.Setup` builds the logger and registers its shutdown; then, if
   `cfg.IsTracingEnabled()`, `telemetry.SetupTracing` registers the global
   TracerProvider and its shutdown. Otherwise a **no-op logger**
   (`slog.DiscardHandler`). Logger or tracing failure → startup error
   (see below).
2. **Banner**: if `http.server.header: true`, logs
   `***** AMS SOFT nForge v<ver>: <APP> v<appver> *****`.
3. **Codec** (`setupCodec`): builds a `codec.Registry` from `cfg.Codec` and
   installs it as the process default with `codec.SetDefault` (the HTTP
   helpers always use `codec.Default()`). Failure → startup error.
4. **Database** (`setupDatabase`): only if `database.enabled: true`. Opens the
   pool with a 15 s startup timeout and a ping; failure → startup error (the app
   never starts without its database). With `database.tenancy.enabled` the
   startup also runs `CheckTenancy` (§9.1), whose failure is fatal too.
   Registers `db.Shutdown`.
5. **JOSE** (`setupJOSE`): only if `jose.enabled: true`. `jose.New` with a
   15 s startup timeout reads and validates every key source; failure →
   `unable to setup jose` startup error (the error is already
   sanitized: no key paths or material). Logs `[JOSEModule] JOSE setup
   completed` with `Config.LogValue` (counts and profile names only).
   Registers `service.Shutdown`.
6. **Cache** (`setupCache`): only if `cache.enabled: true`. `cache.Open`
   with a 15 s startup timeout, `WithCodecRegistry(d.Codec)`,
   `WithNamespace(cfg.Env, cfg.App.Name)`, `WithLogger(&d.Logger)` and, when
   `database.tenancy.enabled`, `WithTenantResolver` bound to
   `database.TenantFromContext` (§11.4). The startup ping must pass (Redis
   down → `unable to setup cache` startup error; `Open` has
   already released its resources). Logs `[CacheModule] Cache setup
   completed` with `cache.Config.LogValue`. Registers `Cache.Shutdown`.

`Deps` fields: `Context` (background), `Config` (value copy), `Logger`
(`slog.Logger` **value**, take `&deps.Logger`), `Codec`, `DB` (**nil when the
database is disabled**), `JOSE` (**nil when jose is disabled**), `Cache`
(**nil when the cache is disabled**), `Aux` (shutdown funcs).
`Deps.CheckJOSE(ctx)` returns `deps.ErrJOSEDisabled` when nil, else
`Service.Check` (no I/O, no crypto). `Deps.PingCache(ctx)` returns
`deps.ErrCacheDisabled` when nil, else `Cache.Ping`. `Deps.ClearAux()`
drops every registered shutdown function (a later `Deps.Shutdown` then
closes nothing); it exists for tests that tear modules down themselves.

### 3.2 Shutdown

- `ServerHttp.Start()` is `StartContext` with a context canceled by
  SIGINT/SIGTERM (`signal.NotifyContext`, so the subscription is released).
  Startup is transactional (§7.12): certificates are loaded and **every**
  TCP/UDP listener is bound before the first request; any failure closes
  what was bound and returns the error. On a signal or a runtime listener
  error every transport is shut down in parallel with a 10 s timeout (then
  forcibly closed), and `Deps.Shutdown` runs exactly once — also when
  startup failed, and always after the listeners are released.
- `Deps.Shutdown` runs registered functions **LIFO** (like `defer`): the
  cache closes first, then JOSE, then the database, then tracing, then logs
  (so they can still log). All run even
  if one fails; errors are joined. Logs emitted after this point may be lost.
- `ServerHttp.ShutdownFunc` / `AddShutdownFunc` / `Shutdown()` are a separate,
  app-level list of `func()` that the **application** calls itself (the
  example calls it only when `Start` returns an error). `Start` does not call
  them.

### 3.3 Middleware chain (fixed order, set in `setStandardHandler`)

1. `Tracing()` — only if `IsTracingEnabled()`. Must be first.
2. `Logger(&deps.Logger)` — only if any logging output is enabled.
3. `middleware.Recoverer` (from go-ams-router) — always.
4. Body limit — always: `http.MaxBytesReader` at `http.server.max_body_bytes`
   (default: `codec.max_payload_bytes`) + 1 byte, so raw routes that read
   `r.Body` are bounded and typed routes still get their 413 from the codec
   first (`ParseStatus` also maps `*http.MaxBytesError` to 413).
5. Handler timeout — always: the request context gets a deadline of
   `http.server.handler_timeout` (default 30s), so handlers, queries and
   loaders that honor `ctx` stop (the write timeout only closes the connection).
6. `middleware.Security` (go-ams-router) — unless `http.security.enabled: false` (§7.11).
7. `middleware.CORS` (go-ams-router) — only if `http.cors.enabled` (§7.11).
8. App global middlewares — whatever `LoadGlobalMiddleware` adds.

go-ams-router builds its middleware chain lazily on the first request, so
`Use` (and `LoadGlobalMiddleware`) is valid any time **before the server
serves its first request**, even after routes were registered. Calling `Use`
**or registering a route** after serving starts panics (router v1.1.0). For inline routers (`With`/`Group`), middlewares
must be added before routes on that inline router.

Built-in routes registered by `NewHTTPServer`:
- Docs (`http.docs.enabled`): `GET {path}`, `{path}/swagger.json`,
  `{path}/swagger.yaml`.
- JWKS (`jose.enabled` and `jose.jwks.enabled`): `GET`/`HEAD`
  `{jose.jwks.path}`; see §7.9.
- Health/readiness (if either is enabled): see §7.6.

---

## 4. Configuration (`config/`)

### 4.1 Loading pipeline (`LoadFlags(configPath, envFilePath)`)

`config.Load()` parses the flags `-config` and `-envfile` (it calls
`flag.Parse()` — define your own flags before calling it) and delegates to
`LoadFlags`. Order of precedence, **later wins**:

1. Start with `Config{Env: "local"}`.
2. **Base YAML**: `-config` path, or `./config.yaml` in the **current working
   directory**. A `-config` path that does not exist is an error; only the
   default `./config.yaml` may be absent (it just yields empty config, which
   then fails validation on `app.name`). **Unknown keys are errors** in every
   section (`KnownFields`): a typo such as `http.cors.origns` would otherwise
   leave `origins` empty, which means "any origin". Top-level keys starting
   with `x-` are ignored (room for application data in the same file).
   - `${VAR}` references in any YAML scalar are expanded from the **process
     environment** (an **unset** variable is a load error naming every missing one;
     `${VAR:-default}` supplies a value when VAR is unset or empty, and
     `${VAR:-}` declares it optional). Expansion is per YAML node, not
     on raw text, so values with YAML special characters are safe. For
     unquoted scalars the tag is reset so `port: ${DB_PORT}` still decodes as
     an int; quoted scalars stay strings.
3. **Env file** (`-envfile`, parsed with godotenv but with every value taken
   literally: `$` is an ordinary character, nothing is expanded, so a
   password like `pa$SWORD` is kept intact): each leaf field is set from a
   key made of its YAML path, upper-cased, joined by `_`:
   `http.server.port` → `HTTP_SERVER_PORT`, `database.pool.max_conns` →
   `DATABASE_POOL_MAX_CONNS`, `codec.max_payload_bytes` →
   `CODEC_MAX_PAYLOAD_BYTES`. Supported: string, ints, uints (overflow
   checked), bool, floats, `time.Duration` (`"5s"`), pointers, `[]string`
   (comma-separated), and any `encoding.TextUnmarshaler` (e.g. `ByteSize`).
   Note: only the env **file** is read this way — real process env vars are
   only used via `${VAR}` in YAML. A key that matches no field is an error
   (`unknown keys ...`), as are lower-case keys; `Warnings` and `OnlineSince`
   are load-time values and cannot be set. Sequences of structs (`jose.keys`,
   `jose.profiles`) are YAML-only: they are skipped, and an env key naming
   one (e.g. `JOSE_KEYS`) is an explicit error; use `${VAR}` inside the YAML
   entries instead. Scalar JOSE leaves work (`JOSE_LIMITS_MAX_TOKEN_BYTES`,
   `JOSE_JWKS_CACHE_MAX_AGE`, `HTTP_AUTH_REALM`).
4. **Overlay**: based on the (possibly overridden) `env`, which must be one of
   `local`, `dev`, `stg`, `prd` (case-insensitive; stored lower-case; anything
   else is a load error): `prd` → `config.prd.yaml`, `stg` →
   `config.stg.yaml`, `dev` → `config.dev.yaml`; `local` has none. Path is
   relative to the **CWD**, not to the base config's directory. A missing file
   is skipped; any other `Stat` or read error is a load error. It overrides
   the env file too.
5. **Defaults** (`feedDefaults`).
6. **Validation** (`validateRequiredConfig`), then `transportWarnings`
   (`config/warnings.go`) checks: a TLS private key readable by group or
   others, Redis credentials without TLS to a non-local host, a PostgreSQL
   password with `sslmode` disable/allow/prefer (the pgx default) to a
   non-local host, HTTPS or HTTP/3 served while `cache.redis` or the database
   does not use TLS (a Unix socket is exempt). Outside `prd` they are
   `Config.Warnings`; with `env: prd` each one is a **load error** unless its
   `prd_exceptions` key is true (`tls_key_permissions`, `redis_plaintext`,
   `database_plaintext`), in which case it stays a warning. In `prd`,
   `http.docs`, `http.routes.health` or `http.routes.readiness` enabled is
   only a warning (unauthenticated endpoints), silenced by
   `app.internal_use: true`. `deps` logs config warnings at ERROR level in
   `prd`, because that environment filters the log below ERROR.
   `codec.max_payload_bytes` must be at
   most 256 MiB.

### 4.2 Full schema, defaults and validation

```yaml
env: local                    # local (default) | dev | stg | prd — drives log level and overlay

app:
  name: my-app                # REQUIRED
  version: 1.0.0              # REQUIRED
  internal_use: false         # true = not reachable from the internet; silences the prd warning about docs/health/readiness
  # OnlineSince is set at load time (RFC 3339), not configurable

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
      max_stream_timeout: 30s # one HTTP/3 request: context deadline + stream read/write deadlines
      allow_0rtt: false       # reserved: true is rejected at load (0-RTT requests are replayable)
    read_header_timeout: 10s  # http.Server timeouts: 0 = default, never "none"
    read_timeout: 30s         #   (Slowloris); negative → error;
    write_timeout: 30s        #   read_header_timeout <= read_timeout
    idle_timeout: 120s        #   (config/server.go, also applied by NewHTTPServer)
    handler_timeout: 30s      # deadline of every request context (handlers, queries, loaders)
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
    origins: []               # exact lowercase origins; [] or ["*"] = any origin
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
      enabled: false          # MASTER SWITCH for all OTel (logs AND traces)
      endpoint: ""            # "host:port" or URL; "" = OTel default localhost:4317
      insecure: false
    terminal:
      enabled: false

tracing:
  otel:
    enabled: <true>           # *bool; nil = true. Only effective if logging.output.otel.enabled
    endpoint: ""
    insecure: false
    sample_ratio: <1>         # *float64, 0..1; nil = 1 (every request); ParentBased(TraceIDRatioBased)
    trust_incoming: <false>   # *bool; true: continue the caller's traceparent, adopt X-Request-Id, keep baggage (behind a gateway)

codec:
  formats: { enabled: [json] }      # json | cbor | xml
  default: json                     # must be one of the enabled formats
  strict_unknown_fields: false
  max_payload_bytes: 5mb            # ByteSize; at most 256mb
  xml: { disallow_dtd: true }       # *bool; nil = true

jose:                         # jose.Config — see §10 (unknown keys inside jose are rejected)
  enabled: false              # false: nothing is read, deps.JOSE is nil
  limits: { max_token_bytes: 16384, max_header_bytes: 2048, max_payload_bytes: 8192,
            max_json_depth: 16, max_key_file_bytes: 65536, max_profiles: 32, max_keys: 64,
            max_token_lifetime: 720h (≤ 8760h; caps every jwt ttl/max_ttl) }
  keys: [ { kid, algorithm: HS256, operations: <all of the algorithm>,
            secret: ${ENV} | source: { format: pem|jwk|oct, file }, publish: false } ]
  profiles: [ { name, kind, typ, signing_*, verification_*, encryption_*, decryption_*,
                content_encryption: A256GCM, jwt: { issuer, audiences, ttl: 15m, max_ttl: 15m, clock_skew: 30s } } ]
  jwks: { enabled: false, path: /.well-known/jwks.json, cache_max_age: 60s }

prd_exceptions:               # insecure settings env prd accepts on purpose (default false: load error); see §4.1
  tls_key_permissions: false  # TLS key readable by group or others
  redis_plaintext: false      # Redis without TLS (credentials, or behind HTTPS/HTTP/3)
  database_plaintext: false   # PostgreSQL sslmode that can fall back to clear text

cache:                        # cache.Config — see §11; validated only when enabled, and only the selected driver
  enabled: false              # false: nothing is opened, deps.Cache is nil
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
```

`jose` defaults and structural validation are delegated to
`jose.ApplyDefaults` / `jose.ValidateConfig` (applied only when enabled);
key files are read later, by `jose.New` in `deps`. `validateAuth` checks
`http.auth` (realm syntax; `default_profile` must be a consuming jwt
profile and requires `jose.enabled`). `cache` defaults come from
`cache.ApplyDefaults` (always applied; zero caps/timeouts take the default,
`read_retries` is a `*int` so an explicit `0` disables retries) and
`validateCache` runs `cache.ValidateConfig` plus the `codec.formats.enabled`
check when enabled. Sizes use `cache.ByteSize` (same syntax as
`config.ByteSize`; `cache` cannot import `config`). The CA file is read only
by `cache.Open`, and only for an enabled Redis TLS.

Booleans have **no implicit `true` defaults** except the `*bool` fields
(`tracing.otel.enabled`, `http.security.enabled`, `codec.xml.disallow_dtd`,
`http.security.hsts.include_subdomains`, whose default is `false`). Map fields
(`http.security.csp.directives`) cannot come from the env file (explicit
error). In particular
`http.routes.health.enabled`, `readiness.enabled`, `http.server.header` and
`http.docs.enabled` are `false` unless set.

Helpers:
- `cfg.IsLoggingEnabled()` — terminal or OTel output on.
- `cfg.IsTracingEnabled()` — `logging.output.otel.enabled && tracing.otel.enabled != false`.

### 4.3 `ByteSize`

`config.ByteSize` (int64) parses `"5mb"`, `"512kb"`, `"1.5gb"`, `"100b"`,
`"1048576"`, `"5 MiB"`; base 1024, case-insensitive. Implements
`UnmarshalText` and `UnmarshalYAML`. Constants `B, KB, MB, GB`. The parser is
`cache.ParseByteSize` (the only implementation; `config` imports `cache`):
whole numbers are exact (no float64 rounding above 2^53) and a value over
`math.MaxInt64` is an error, never a negative number.

### 4.4 Adding a config field — checklist

1. Add the field to the right struct in `config/types.go` with a `yaml:"snake_case"`
   tag and a trailing comment stating values and default.
2. If it needs a default, add it to `feedDefaults` (or a `feedXDefaults`
   helper). Use a pointer (`*bool`) when "unset" must differ from the zero value.
3. Validate in `validateRequiredConfig` if invalid values must stop startup.
4. The env-file key is derived automatically from the YAML path; make sure the
   type is supported by `setFieldValue` (or implement `TextUnmarshaler`).
5. Add tests in `config/*_test.go` and update the `config.yaml` of the
   example that covers the module (§14).

---

## 5. Logging (`logs/`)

`logs.Setup(ctx, cfg)` returns `(*slog.Logger, shutdown, error)` and calls
`slog.SetDefault`.

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
- OTel internal errors are routed via `otel.SetErrorHandler` to the
  **terminal only**, as WARN, to avoid feeding a failing exporter.
- `leveledHandler.WithAttrs/WithGroup` re-wrap the handler — keep this when
  editing, or `logger.With(...)` loses its level filter. Same for
  `contextHandler` and `multiHandler`.
- `logs.NewNoop()` returns a discarding logger.

Always log with the context-aware methods (`InfoContext`, `ErrorContext`,
`LogAttrs(ctx, ...)`) inside requests so `request_id` and trace correlation
are attached. Module log messages use a bracketed prefix:
`[LoggerModule]`, `[CodecModule]`, `[DatabaseModule]`, `[HTTPModule]`;
fatal startup errors use `[Fatal ERROR] ...`.

---

## 6. Telemetry (`telemetry/`)

- `NewResource` — shared by logs and traces so both reach Grafana with the same
  `service.name` (app.name), `service.version`, `deployment.environment` (env),
  host name, process pid/executable/runtime, plus `OTEL_RESOURCE_ATTRIBUTES`.
  **`WithProcess()` is deliberately not used**: it includes command-line
  args, which may leak secrets passed via flags. Do not add it.
- `SetupTracing` — always sets the W3C propagator (traceparent + baggage). If
  tracing is enabled, registers a `TracerProvider` with OTLP/gRPC batch
  exporter, the shared resource and the custom `idGenerator`. If disabled, OTel
  stays on its no-op provider (instrumented code still works).
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

### 7.1 `ServerHttp`

```go
type ServerHttp struct {
    Deps         deps.Deps
    Srv          *http.Server         // Srv.Handler is the go-ams-router Router
    ShutdownFunc []func()
    TypedRoutes  []TypedRoute         // source of the OpenAPI spec
}
```
Methods: `NewHTTPServer(deps)`, `Router()`, `Typed()`,
`AddGlobalMiddleware`, `LoadGlobalMiddleware`, `AddDocRoute`,
`TenantMiddleware(opts...)` (§7.10), `AddShutdownFunc`, `Shutdown`, `Start`,
`StartContext(ctx)` (what `Start` calls; tests and embedders cancel the ctx).
`Srv` stays the application's plain HTTP `*http.Server` (never started when
`disable_insecure_http` or `redirect_http` replaces it; its `Handler` is the
one router shared by every transport, so `Router()`/`Typed()` keep working).

`NewHTTPServer` sets `Srv.ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout`
and `IdleTimeout` from `http.server` (defaults 10s/30s/30s/120s). It applies
`config.ApplyServerDefaults` to a copy of the section, so a `Config` built
by hand (tests, embedding) also gets them while `Deps.Config` is unchanged.
A handler that streams or runs longer than `write_timeout` extends its own
deadline with `http.NewResponseController(w).SetWriteDeadline(...)`;
`Srv` stays exported for anything else, as long as it is changed before
`Start`.

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
  `IsPrivate`, nil `deps.JOSE`, no resolvable profile, unknown / non-JWT /
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

`NewHTTPServer` registers each one only when enabled (typed routes, tag
`maintenance`):
- `GET {health.path}` → 200 `{"status":"ok"}` (liveness: never touches dependencies).
- `GET {readiness.path}`:
  - database disabled (`deps.DB == nil`) → 200
    `{"status":"ok","online_since":"<RFC3339>"}` (no `database` key);
  - database enabled and the ping (request ctx + 2 s `readinessTimeout`, via
    `Deps.PingDatabase`) succeeds → 200 with `"database":{"health":true}`;
  - jose enabled → `"jose":{"health":<Deps.CheckJOSE ok>}` (no key I/O);
    omitted when disabled;
  - cache enabled → `"cache":{"health":<Deps.PingCache ok>}` (request ctx +
    the same 2 s budget; Redis PING, memory: open), omitted when disabled.
    It is pinged on every probe, so a recovered Redis is healthy at once;
  - any dependency fails → **503**, the same body as `error.detail` with
    `"status":"unavailable"` and the failing `health:false`, logged as WARN.

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

- Spec is built from `ServerHttp.TypedRoutes` and **cached per route count**
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
p, ok := nhttp.PrincipalFromContext(r.Context())   // Subject, Issuer, Audience, TokenID, TenantID, Roles, Scopes
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
`deps.DB.ForContext(r.Context())` directly. JWT validation itself is
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
  **replaces**. Both validate the ID as a canonical UUID v4 (stored
  lowercased).
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
`http.cors` and mounted by `setStandardHandler` after the Recoverer (§3.3);
`config` validates them at load with the router's own rules, so building
them never panics. Apps that need dynamic options (a CSP nonce for their
own pages, `CORSDelegate`, `OriginFunc`) leave the section disabled and
add the router middleware with `AddGlobalMiddleware`.

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
`config.ApplyServerDefaults` fills them on a copy in `config.Load`, in
`NewHTTPServer` and in `Start`, so a `Config` built by hand behaves the same.

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
  chain (§3.3) and routes exist once. HTTPS uses `ServeTLS` with only
  `Certificates`/`MinVersion` set, so `net/http` negotiates `h2` itself.
  HTTP/3 uses `quic-go/http3` (v0.64.1) with TLS 1.3 and ALPN `h3` forced,
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
- **Startup/shutdown.** `prepare` loads certificates, then binds UDP, HTTPS,
  HTTP in that order, closing everything on any error. `StartContext` serves
  each in a goroutine; the first non-closed error or ctx cancel stops all
  transports (parallel `Shutdown`, 10 s, then `Close`), waits for the serve
  goroutines, then runs `Deps.Shutdown` once. net/http can leave a just-
  accepted HTTP/2 connection undrained: the result is the drain deadline
  (`context.DeadlineExceeded` in the joined error) followed by a forced close,
  never a leak.
- **Credential reload (`certs.go`).** The certificate is served through
  `tls.Config.GetCertificate` from a `certReloader`: a handshake looks at the
  cert and key files' modification times at most every 30 s and re-reads a
  changed pair, and **SIGHUP** reloads every certificate and, with jose
  enabled, the JOSE keys (`Deps.ReloadJOSE`), so ACME/cert-manager rotation
  needs no restart and drops no connection. A pair that fails to load or does
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
- Runnable demo: `_examples/basic/config.https-http3.yaml` (§14).

---

## 8. Codec (`codec/`)

Business code **never imports `encoding/json`, `encoding/xml` or the CBOR
library directly** — it goes through a `*codec.Registry` (normally
`codec.Default()`, or `deps.Codec`).

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
- Process default: initialized in `init()` with json+cbor+xml, permissive,
  5 MiB; replaced once by `deps.NewDeps` via `SetDefault`.

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
  in every `fmt` verb (`Format`), and `config.Config` prints a redacted copy
  (`Config.Redacted()`: database, Redis, JOSE keys and key paths, TLS key
  paths, OTel endpoint userinfo) through `String`, `GoString`, `LogValue`,
  `MarshalJSON` and `MarshalYAML`, so a whole `Deps.Config` can be logged with
  any handler. `ConnectionString()` output must never be logged.
  `TestConfigNeverPrintsSecrets` plants a secret in every such field: extend
  it when you add one.

Health: `deps.PingDatabase(ctx)` returns `deps.ErrDatabaseDisabled` when the
DB is off; `db.Ping` always pings the pool (even from a tx-scoped DB).
`db.Shutdown` blocks until all connections are released — call it after the
HTTP server stops (`Deps.Shutdown` already does, LIFO).

Error idiom: `pgx.ErrNoRows` → 404; `DELETE` with `RowsAffected() == 0` is
not an error in pgx — check it; other DB errors → log + generic 500 (never
leak details to clients).

Migrations are **out of scope** of the framework (the example creates its
table at startup only to be self-contained).

### 9.1 Multi-tenancy (`tenancy.go`, `internal/rlsexec`)

Opt-in through `database.tenancy` (or `New(pool, WithTenancy(cfg))`). Tenant
IDs are canonical **UUID v4** (`ParseTenantID`, lowercased). Two modes:

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
  `Deps`** opened by the app with another role that has `BYPASSRLS` and
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

Standalone package (no internal imports, no globals; several `*jose.Service`
can coexist). Crypto is delegated to `github.com/go-jose/go-jose/v4`
(pinned v4.1.5, `govulncheck` clean at adoption) behind unexported adapters:
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
  no watcher and is never triggered by tokens. `nhttp` triggers it on
  **SIGHUP** (`Deps.ReloadJOSE`, together with the TLS certificates); an
  application with another trigger calls `Deps.ReloadJOSE(ctx)` itself. The
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

Standalone package (imports only `codec`; no `config`, `deps`, `nhttp`,
`database`, `logs` or `telemetry`). One **non-generic** `*cache.Cache` per
instance, one driver per instance, no globals (several can coexist). Redis
goes through `github.com/redis/go-redis/v9` (pinned **v9.22.0**) behind
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
    cache.WithTenantResolver(fn), cache.WithLogger(logger))   // deps does this for you
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
  `ErrDisabled`. It never panics or exits (deps decides that it is fatal).
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
Tenant isolation is on when a resolver is given; `deps` gives one exactly
when **`database.tenancy.enabled`** (regardless of `database.enabled`),
bound to **`database.TenantFromContext`** (`deps.tenantFromContext`
returns `database.ErrNoTenant` when absent). That context value is set by
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

## 12. Crypto (`crypto/`, package `ncrypto`)

Standalone toolbox (no internal imports, no globals, no config section, not
in `Deps`): the application builds what it needs from its own keys. The
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
strong reason) and has no config or `Deps` wiring.

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

---

## 14. Examples and local stack (`_examples/`)

Four independent `package main` applications, each focused on one part of
the framework, each with its own `config.yaml` (only the keys it uses,
commented), `README.md` and tests. They share one pattern: `main` loads the
config, builds `deps` and calls a `newServer(dps)` that the tests reuse with
`config.LoadFlags` and an env file, so tests drive the real middleware chain
through `Srv.Handler` without a port. All listen on `127.0.0.1:8080`; app
names are `<name>-example`.

| Example | Modules | Files | Tests |
|---|---|---|---|
| `basic` | HTTPS + HTTP/3 opt-in (`config.https-http3.yaml`, `certs/`), jose (one HS256 `access` profile, demo secret inline in its config.yaml), cache (memory driver), database (PostgreSQL on; `standalone.env` turns it off, no Docker); `http.security` + `http.cors` on | `main.go` (bootstrap, `clacksOverhead` global middleware), `auth.go` (demo login `admin@admin.com`/`admin123` → `IssueJWT`, private typed `/auth/me`), `cache.go` (`/cache/{key}` Set/Get/Delete), `config.yaml` has OTel preconfigured for the local collector but off, `database.go` (`/db/notes` CRUD on table `basic_notes`, not registered when `deps.DB` is nil), `https_http3_test.go` (starts the example on free ports with the committed certificate and talks real HTTPS, HTTP/3 and the 308), `certs/` (**local-development-only, intentionally public** ECDSA pair, SAN localhost/127.0.0.1/::1; never reused by anything else, regenerate with `certs/generate.sh`), `plain.go` (`ParseJSON/XML/CBOR` + `Send*` echo routes, `AddDocRoute`), `users.go` (typed CRUD over an in-memory store, `ListQuery` typed query with default/required/`Validate`, an XML-codec route), `crypto.go` (`runCrypto` ncrypto demo: XChaCha20, HMAC, bcrypt/Argon2id, BLAKE3, random; disabled in `main.go`), `safecalc_test.go` (cents arithmetic + `Format`, `ExampleMoney_Format`) | formats, status mapping 400/415/422, typed CRUD + paging, docs/health/ready, security headers + docs CSP + CORS preflight, login 400/401 + `/auth/me` 401/200, cache hit/miss/delete, the crypto demo, safecalc; `/db/notes` CRUD + 404 (integration) |
| `jose` | jose | `auth.go` (demo login `admin`/`admin` with `profile` + `scope`, `/public`, typed `/me`, raw `/raw/wallets` + `/raw/wallets-slice` with variadic/slice `WithScopes`, typed `/api/v1/wallets`, nested-only `/api/v1/confidential`, `/api/v1/jose/demo` JWS/JWE round trip; `protectedCalls` counter), `cmd/keygen` (key files for the commented asymmetric setup; 0700/0600, `-force`) | 200/401/403 matrix, handlers not invoked on failure, nested vs access, missing secret fails at load |
| `cache` | cache (redis; `standalone.env` → memory) | `routes.go` (`/api/v1/items/{key}` Set/Get/claim=SetIfAbsent/ttl/expire/Delete, `/api/v1/counters/{key}`, raw `/raw/{key}` bytes, `/api/v1/products/{id}` `GetOrLoad` over a slow `catalog`; `sendCacheError`) | shared scenarios run on memory (`main_test.go`) and on Redis (`redis_integration_test.go`, unique prefix, deletes its keys); CBOR; readiness |
| `database` | database (+ tenancy column, `http.tenant.source: header`) | `users.go` (Squirrel/pgx CRUD, filters, batch in `Transaction`, `sendDBError`), `notes.go` (typed `Opts{Tenant}` routes and raw `server.TenantMiddleware()`, `ForContext`/`TenantEq`/tenant `Transaction`; `ensureNotesSchema` builds RLS when `mode: rls`), `rls.env` + `rls-setup.sql` (role `app_tenant`) | config + rls overlay load (unit); CRUD and tenant isolation 403/400/404 (integration) |

Local stack — `_examples/docker-compose.yml` (project `nforge-examples`):
PostgreSQL 18 (5432, `app`/`app`/`app`), Redis 8 (6379), OTel Collector
(4317 gRPC, 4318 HTTP), Tempo (3200), Loki (3100), Grafana (3000, anonymous
admin, provisioned datasources and the *nForge · Logs* dashboard with a
`service` variable defaulting to `basic-example`). Configs live in
`_examples/observability/`.

Commands (repository root):
```bash
docker compose -f _examples/docker-compose.yml up -d     # or: make container-up
go run ./_examples/basic -config _examples/basic/config.yaml [-envfile _examples/basic/standalone.env]
# HTTPS :8443/tcp + HTTP/3 :8443/udp + 308 redirect from :8080 (run from the repo root: cert paths are relative to the CWD)
go run ./_examples/basic -config _examples/basic/config.https-http3.yaml -envfile _examples/basic/standalone.env
export JWT_SECRET=$(openssl rand -hex 32) JWT_ENCRYPTION_SECRET=$(openssl rand -hex 16)
go run ./_examples/jose -config _examples/jose/config.yaml
go run ./_examples/cache -config _examples/cache/config.yaml [-envfile _examples/cache/standalone.env]
go run ./_examples/database -config _examples/database/config.yaml [-envfile _examples/database/rls.env]
make dev                                                  # air: hot reload of _examples/basic (needs PostgreSQL)
```
Then `http://localhost:8080/docs`; Grafana at `http://localhost:3000`.

---

## 15. Building and testing

```bash
go build ./...
go vet ./...
go test ./...                    # unit tests (cache, codec, config, crypto, database, deps, helper, http, jose)
go test -race ./...

# the examples (`./...` skips `_` directories, so name each one):
EXAMPLES='./_examples/basic/... ./_examples/jose/... ./_examples/cache/... ./_examples/database/...'
go vet $EXAMPLES
go test -race $EXAMPLES

# jose fuzzers (smoke runs) and benchmarks:
for f in FuzzValidateJWT FuzzStrictJSON FuzzParseCompact FuzzKeyImport; do
  go test ./jose -run '^$' -fuzz "^$f\$" -fuzztime 15s; done
# codec: streaming JSON presence check vs the tree reference (run after any
# change to codec/presence*.go):
go test ./codec -run '^$' -fuzz '^FuzzPresenceJSON$' -fuzztime 60s
go test ./jose -run '^$' -bench . -benchmem

govulncheck ./...                # required before changing security dependencies

# golangci-lint v2.15.0+ with the repo's .golangci.yml; older releases
# (v2.13.x) cannot read go1.27.2 export data and fail every package with
# typecheck "export data version" errors, which GOTOOLCHAIN=local or
# rebuilding the same release does not fix:
golangci-lint run ./... $EXAMPLES

# integration tests against a real PostgreSQL (build tag `integration`); the
# tenancy tests create/drop a role, so the URL's user must be able to CREATE ROLE:
NFORGE_TEST_DATABASE_URL='postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable' \
  go test -tags integration ./database/...

# cache integration tests against a DISPOSABLE Redis (unique prefixes, only
# their own keys are deleted, never FLUSHDB):
NFORGE_TEST_REDIS_URL='redis://localhost:6379/15' go test -race -tags integration ./cache/

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
- Startup failures of required modules are fatal: `deps.NewDepsE` returns the
  error (after logging `[Fatal ERROR] …` and shutting down what had started,
  so OTel batches are flushed) and `deps.NewDeps` prints it to **stderr**,
  whatever the logging configuration, and exits with status 1.
- Keep the hot path allocation-conscious; cache reflection per type with
  `sync.Map`.
- Inside `nhttp`, import `net/http` as `stdhttp`; the router as `router`.
- Configuration is YAML with `snake_case` keys; document the default in a
  trailing comment on the struct field.
- Security defaults are on and must stay on: HTTP server timeouts (never
  zero), DTD blocked, payload limit,
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
- Update this AGENTS.md whenever you change architecture, config schema,
  public API, or a behavior described here — and `llms.txt` and the
  affected `README.md` files in the same change.

---

## 17. Known gotchas (read before debugging)

1. **CWD matters**: default `config.yaml` and the overlays
   (`config.<env>.yaml`) are resolved from the working directory.
2. **Overlay beats env file**; `${VAR}` reads the real environment, the env
   file does not feed `${VAR}`.
3. **`env` casing**: overlay selection is case-insensitive, log level is not
   (`PRD` → overlay applied but DEBUG log level). Use lowercase.
4. **`deps.DB` is nil when `database.enabled: false`**: any code that touches
   the database must check for nil (or use `Deps.PingDatabase`, which returns
   `ErrDatabaseDisabled`). The built-in readiness route used to panic here;
   it now omits the database check (regression tests in `http/routes_test.go`).
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
10. **`Deps.Logger` is a value**: pass `&deps.Logger`; don't copy `Deps`
    expecting shared mutations to `Aux` beyond the pointer.
11. **Codec default registry** before `NewDeps` is json+cbor+xml permissive;
    tests that rely on config-driven behavior must build their own Registry
    (and restore the default with `SetDefault` afterwards).
12. **Tracing needs both switches**: `logging.output.otel.enabled: true` *and*
    `tracing.otel.enabled` not false. OTel logging off = no traces either.
13. **`IsPrivate` is enforced** on typed routes: registering one with jose
    disabled, without `AuthProfile`/`http.auth.default_profile`, or with
    `http.docs.auth: none` panics at startup. On `AddDocRoute` it is still
    documentation only — raw routes need `Authenticate` explicitly.
14. **`deps.JOSE` is nil when `jose.enabled: false`**; `Authenticate(nil, …)`
    panics. Unknown YAML keys fail at load time in every section.
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
18. **RLS app sees no rows**: in rls mode the unscoped `deps.DB` sees nothing
    in tenant tables — use `ForTenant`/`ForContext`. If *everything* is
    visible instead, the role is superuser/BYPASSRLS or owns the table
    without `FORCE` (list the table in `tenancy.tables` so startup catches it).
19. **`COPY FROM` fails on RLS tables** (`0A000`): a PostgreSQL limitation,
    not a framework bug.
20. **`deps.Cache` is nil when `cache.enabled: false`**; check before use or
    call `Deps.PingCache` (`ErrCacheDisabled`). With the cache enabled and
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
    (default 30s) bounds every response. Raise it in the config, or extend the
    deadline per request with `http.NewResponseController(w).SetWriteDeadline`.
    A timeout cannot be disabled (0 means the default).
27. **`http.server.port` is optional** (default 8080), like `tls.port` and
    `http3.port` (8443). A config with no `http.server` section loads.
28. **Redirect needs a trusted host**: `tls.redirect_http: true` with a
    non-loopback `http.server.host` fails unless `tls.redirect_host` is set;
    the redirect never uses the request's `Host`.
29. **QUIC is UDP**: opening only TCP/8443 in a firewall leaves HTTP/3 dead
    (clients fall back to HTTPS through `Alt-Svc`). An HTTP/3-only server is
    not discoverable by browsers. `curl --http3-only` needs a curl build with
    HTTP/3; `-k` is for the demo certificate only.
30. **The example certificate is public**: `_examples/basic/certs/localhost.key`
    is committed on purpose. Never deploy or trust it; the framework has no
    default certificate.
31. **`codec` needs the `jsonv2` Go experiment**: it imports
    `encoding/json/jsontext` (the presence walker). Go 1.27 enables it by
    default; `GOEXPERIMENT=nojsonv2` makes `go build ./codec` fail with
    "build constraints exclude all Go files".
