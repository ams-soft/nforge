# AMS nForge

**AMS nForge** (Nano Forge) is a small, opinionated Go application runtime,
built first for HTTP APIs, by AMS Software e Consultoria LTDA. An application
is a set of explicitly registered modules: configuration, structured logs,
distributed tracing, a multi-format codec, an HTTP server with typed routes
and generated OpenAPI docs, PostgreSQL access, JOSE/JWT authentication, a
cache, RabbitMQ messaging, Server-Sent Events, and your own. One orchestrator validates the whole configuration and
the dependency graph before anything opens, initializes every module before
any starts, opens the listeners last, drains them first on shutdown and
closes the rest in reverse dependency order. HTTP is optional: worker-only
applications get the same lifecycle. Standalone cryptographic primitives and
a money helper complete it.

```go
cfg, err := config.Load() // one YAML: ${ENV}, .env file, per-environment overlay
if err != nil { /* ... */ }

api := module.New("api", module.Definition{
    Requires: []module.Handle{nhttp.Capability},
    Init: func(_ context.Context, s *module.Scope) (module.Hooks, error) {
        server := module.Require(s, nhttp.Capability) // routes are registered before any listener opens
        nhttp.Post(server.Typed(), "/users", func(r *http.Request, u User) (User, error) {
            return u, nil // already decoded and validated; the response is validated too
        }, nhttp.Opts{IsPrivate: true, RequiredScopes: []string{"users:write"}, Status: http.StatusCreated})
        return module.Hooks{}, nil
    },
})

a, err := app.New(cfg, app.Modules( // explicit registration; every section validated, no I/O
    logs.Module(), telemetry.Module(), codec.Module(), jose.Module(), nhttp.Module(), api))
if err != nil { /* ... */ }
err = a.Run(context.Background()) // start, serve, graceful shutdown on SIGINT/SIGTERM, reload on SIGHUP
```

## Features

| Area | Package | What you get |
|---|---|---|
| Application | `app`, `module` | Explicit module registration, typed capabilities checked at startup, a deterministic dependency graph resolved before any I/O, Init-then-Start, ingress last, readiness, runtime failures, `Run` with signals, one graceful shutdown under a deadline |
| Configuration | `config` | One YAML read once: `${VAR}` expansion, `.env` overrides, `config.<env>.yaml` overlays; each module owns, defaults and validates its section, unknown keys and sections are errors |
| Logging | `logs` | `log/slog` to the terminal (tint) and/or OpenTelemetry (OTLP), `request_id` on every record |
| Tracing | `telemetry` | OpenTelemetry spans per request, with `request_id == trace_id` |
| Codec | `codec` | JSON, CBOR and XML from **one struct**: required fields, strict mode, payload limits, XXE protection, `Validate()` |
| HTTP | `http` (`nhttp`) | Plain HTTP, opt-in **HTTPS** (TLS 1.3) and **HTTP/3**, a standard response envelope, **typed routes** checked by the compiler and validated before and after the handler, typed query strings, **OpenAPI 3.1** docs, health/readiness, **rate limiting** and load shedding, a graceful-shutdown delay, an optional module graph page, **static files and SPAs** (`Static`, `SPA`, from a directory or `embed.FS`) |
| Database | `database` | PostgreSQL with pgx + Squirrel, transactions, **multi-tenancy** by tenant column or Row-Level Security |
| JOSE | `jose` | JWT, JWS and JWE through explicit profiles, bearer authentication with scopes, JWKS, key rotation |
| Cache | `cache` | Redis or a bounded in-process LRU behind one API: objects, bytes, counters, TTLs, NX, cache-aside `GetOrLoad`, tenant namespaces |
| Messaging | `message` | RabbitMQ publishing confirmed **per message** and pipelined, concurrent consumers, Classic priority, Quorum queues, optional dead-letter/retry handling and DLQ reprocessing, W3C trace context across the broker |
| Metrics | `metrics` | A dependency-free registry in the Prometheus text format, with ready-made instruments for HTTP, SSE, Messaging, the database pool and the cache |
| Server-Sent Events | `sse` | Explicit module with **user-targeted** delivery only: authenticated streams (the validated JWT subject), `PublishToUser`, memory or RabbitMQ driver (a fanout exchange, one ephemeral queue per instance), heartbeats, bounded queues and limits, graceful shutdown; best effort, no replay |
| Crypto | `crypto` (`ncrypto`) | Standalone primitives, no config: XChaCha20-Poly1305 and AES-256-GCM encryption, HMAC-SHA256, bcrypt/Argon2id password hashing, BLAKE3/SHA-256, secure random values and ULIDs |
| Helpers | `helper/safecalc` | Money as integer cents: exact formatting (BRL by default, any separators), float input rounded half-to-even |
| Helpers | `helper/qrcode` | QR Codes as PNG, SVG, Base64 and Data URI: rounded modules and finders, centered PNG/JPEG/SVG logo, level L/M/Q/H, a terminal-printable matrix |

Security defaults stay on: HTTP transport timeouts (no Slowloris; the per-request handler timeout is opt-in), DTD/XXE
blocked, payload and element limits (1 MiB, 250k values), duplicate JSON members refused, CORS that must name its origins, a restricted
JOSE suite (never `none`, algorithms only from config, mandatory `kid`/`typ`),
sanitized errors that never leak tokens, keys, SQL or Redis details,
transaction-local tenant settings, and in `ncrypto` AEAD-only encryption with
random nonces and password-hash verification bounded before allocating.

## Install

```bash
go get github.com/ams-soft/nforge
```

nForge needs **Go 1.27** or newer; build with **1.27.2+**, which fixes
`net/http`, `crypto/tls` and `html/template` vulnerabilities (this repository
pins it with `toolchain go1.27.4`). Import the HTTP package as
`nhttp "github.com/ams-soft/nforge/http"` and the crypto package as
`ncrypto "github.com/ams-soft/nforge/crypto"`.

## Examples

[`_examples/`](_examples) has nine runnable applications, each with its own
README:

| Example | Shows |
|---|---|
| [`basic`](_examples/basic) | terminal logging, HTTP, and an in-memory typed CRUD API |
| [`complete`](_examples/complete) | opt-in HTTPS + HTTP/3, config, codec, docs, health, JWT login, memory cache, PostgreSQL, RabbitMQ, crypto and observability |
| [`jose`](_examples/jose) | JWT login, bearer authentication, scopes, nested tokens, JWS/JWE, JWKS |
| [`cache`](_examples/cache) | the cache module on Redis or memory, `GetOrLoad`, outages |
| [`database`](_examples/database) | repositories, transactions, multi-tenancy (column and RLS) |
| [`static`](_examples/static) | an API, a built SPA and public files on one server, from directories or `embed.FS` |
| [`message`](_examples/message) | worker-only RabbitMQ producer, automatic/manual consumers, DLQ and retry |
| [`qrcode`](_examples/qrcode) | `helper/qrcode` gallery and a QR Code printed in the terminal |
| [`sse`](_examples/sse) | Server-Sent Events: cookie-authenticated `EventSource`, two tabs per user, memory or RabbitMQ driver, route timeout opt-in |

```bash
go run ./_examples/basic -config _examples/basic/config.yaml
# then open http://localhost:8080/docs

cp _examples/message/.env.example _examples/message/.env
docker compose --env-file _examples/message/.env -f _examples/message/docker-compose.yml up -d   # RabbitMQ for the message example
```

## Configuration

A minimal `config.yaml`:

```yaml
app:
  name: my-api
  version: 1.0.0
http:
  docs:
    enabled: true
  routes:
    health: { enabled: true }
    readiness: { enabled: true }
logging:
  output:
    terminal:
      enabled: true
```

`http.routes.graph` (off by default, built with `-tags nforge_graph`) adds a page
with the module graph, as a diagram, Mermaid source and JSON. It is public and exposes your topology: keep it
behind the gateway or a private network. In `env: prd` it logs a warning, which
protects nothing by itself.

`http.server.port` is optional: omitted, the server listens on plain HTTP
`:8080`. A section configures a module the application registered (a section
of an unregistered module is an error); `database`, `jose` and `cache` stay
off until enabled. The full
schema, with every default and validation rule, is in
[AGENTS.md §4.2](AGENTS.md#42-full-schema-defaults-and-validation); the
examples' `config.yaml` files are commented.

## HTTP, HTTPS and HTTP/3

Plain HTTP is the default. HTTPS (TCP) and HTTP/3 (QUIC, UDP) are opt-in and
share one router and middleware chain. Default ports: HTTP **8080**/tcp, HTTPS
**8443**/tcp, HTTP/3 **8443**/udp, each optional and independent.

```yaml
http:
  server:
    host: 0.0.0.0
    tls:
      enabled: true
      cert_file: ${TLS_CERT_FILE}
      key_file: ${TLS_KEY_FILE}
      # min_version: "1.3"       # default; "1.2" is the only other value
      redirect_http: true        # :8080 only answers 308 → https
      redirect_host: api.example.com   # trusted public host for the redirect
    http3:
      enabled: true              # reuses the tls certificate; Alt-Svc is advertised
```

- `disable_insecure_http: true` closes the plain HTTP port; it excludes
  `redirect_http`. The redirect never trusts the request's `Host`.
- QUIC runs over **UDP**: open UDP/8443 as well as TCP/8443. Clients discover
  HTTP/3 through the `Alt-Svc` header on HTTPS responses; an HTTP/3-only server
  needs clients that know its address.
- 0-RTT is not supported (`allow_0rtt: true` is rejected).

A runnable demo with a local-only certificate is
[`_examples/complete/config.https-http3.yaml`](_examples/complete/README.md#https--http3).
Full reference: [AGENTS.md §7.12](AGENTS.md#712-transports-http-https-and-http3-transportgo-configservergo).

## Documentation

[`AGENTS.md`](AGENTS.md) is the reference for the whole framework: the
runtime lifecycle, every package, the configuration schema, conventions and
known gotchas. It is written for contributors and AI agents alike.
[`llms.txt`](llms.txt) is the index for LLMs and coding assistants
([llmstxt.org](https://llmstxt.org) format): the rules that matter when generating
code and links to every package and example.

## Development

```bash
go build ./... && go vet ./...
go test -race ./...

# integration tests (skip when the variables are unset)
NFORGE_TEST_DATABASE_URL='postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable' \
  go test -tags integration ./database/...
NFORGE_TEST_REDIS_URL='redis://localhost:6379/15' go test -race -tags integration ./cache/

gofmt -l .
golangci-lint run ./...   # v2.15.0+ (older releases cannot read go1.27.2 export data)
govulncheck ./...
```

See [AGENTS.md §15](AGENTS.md#15-building-and-testing) for the examples,
fuzzers and benchmarks, and §16 for the code conventions. Release notes are
in [CHANGELOG.md](CHANGELOG.md).

## License

[MIT](LICENSE) © 2026 AMS Software e Consultoria LTDA
