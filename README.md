# AMS nForge

**AMS nForge** (Nano Forge) is a small, opinionated Go framework for building
HTTP APIs, by AMS Software e Consultoria LTDA. From one `config.yaml` an
application gets configuration, structured logs, distributed tracing, a
multi-format codec, an HTTP server with typed routes and generated OpenAPI
docs, PostgreSQL access, JOSE/JWT authentication and a cache, all wired by
one call and shut down in order, plus standalone cryptographic primitives.

```go
cfg, err := config.Load()          // YAML + ${ENV} + .env file + per-environment overlay + validation
if err != nil { /* ... */ }
dps := deps.NewDeps(*cfg)          // logger, tracing, codec, database, jose, cache
server := nhttp.NewHTTPServer(dps) // router, middlewares, docs, health/readiness

server.Typed().Route("/api/v1", func(api *nhttp.TypedRouter) {
    api.TPost("/users", User{}, User{}, func(w http.ResponseWriter, r *http.Request) {
        u := nhttp.Body[User](r)   // already decoded and validated
        _ = nhttp.SendJSON(w, r, http.StatusCreated, u)
    }, nhttp.Opts{IsPrivate: true, RequiredScopes: []string{"users:write"}})
})

_ = server.Start()                 // graceful shutdown on SIGINT/SIGTERM
```

## Features

| Area | Package | What you get |
|---|---|---|
| Configuration | `config` | Layered YAML: `${VAR}` expansion, `.env` overrides, `config.<env>.yaml` overlays, defaults and validation at load time |
| Logging | `logs` | `log/slog` to the terminal (tint) and/or OpenTelemetry (OTLP), `request_id` on every record |
| Tracing | `telemetry` | OpenTelemetry spans per request, with `request_id == trace_id` |
| Codec | `codec` | JSON, CBOR and XML from **one struct**: required fields, strict mode, payload limits, XXE protection, `Validate()` |
| HTTP | `http` (`nhttp`) | Plain HTTP, opt-in **HTTPS** (TLS 1.3) and **HTTP/3**, a standard response envelope, **typed routes** validated before and after the handler, typed query strings, **OpenAPI 3.1** docs, health/readiness, an optional module graph page |
| Database | `database` | PostgreSQL with pgx + Squirrel, transactions, **multi-tenancy** by tenant column or Row-Level Security |
| JOSE | `jose` | JWT, JWS and JWE through explicit profiles, bearer authentication with scopes, JWKS, key rotation |
| Cache | `cache` | Redis or a bounded in-process LRU behind one API: objects, bytes, counters, TTLs, NX, cache-aside `GetOrLoad`, tenant namespaces |
| Crypto | `crypto` (`ncrypto`) | Standalone primitives, no config: XChaCha20-Poly1305 and AES-256-GCM encryption, HMAC-SHA256, bcrypt/Argon2id password hashing, BLAKE3/SHA-256, secure random values and ULIDs |
| Helpers | `helper/safecalc` | Money as integer cents: exact formatting (BRL by default, any separators), float input rounded half-to-even |
| Bootstrap | `deps`, `module` | Resolves the module graph before anything starts (a required module that is disabled is an error, never switched on for you), starts it in order, rolls back on failure and shuts down in reverse |

Security defaults stay on: HTTP server timeouts (no Slowloris), DTD/XXE
blocked, payload limits, a restricted
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
pins it with `toolchain go1.27.3`). Import the HTTP package as
`nhttp "github.com/ams-soft/nforge/http"` and the crypto package as
`ncrypto "github.com/ams-soft/nforge/crypto"`.

## Examples

[`_examples/`](_examples) has four runnable applications, each with its own
README:

| Example | Shows |
|---|---|
| [`basic`](_examples/basic) | opt-in HTTPS + HTTP/3 (`config.https-http3.yaml`), config, codec, plain and typed routes, docs, health, minimal JWT login, memory cache, PostgreSQL (`standalone.env`: no Docker), an `ncrypto` demo and a `safecalc` test |
| [`jose`](_examples/jose) | JWT login, bearer authentication, scopes, nested tokens, JWS/JWE, JWKS |
| [`cache`](_examples/cache) | `deps.Cache` on Redis or memory, `GetOrLoad`, outages |
| [`database`](_examples/database) | repositories, transactions, multi-tenancy (column and RLS) |

```bash
go run ./_examples/basic -config _examples/basic/config.yaml
# then open http://localhost:8080/docs

docker compose -f _examples/docker-compose.yml up -d   # PostgreSQL, Redis, OTel Collector, Tempo, Loki, Grafana
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
`:8080`. Modules such as `database`, `jose` and `cache` are off until enabled. The full
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
[`_examples/basic/config.https-http3.yaml`](_examples/basic/README.md#https--http3).
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
