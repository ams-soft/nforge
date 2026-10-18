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
| HTTP | `http` (`nhttp`) | A standard response envelope, **typed routes** validated before and after the handler, typed query strings, **OpenAPI 3.1** docs, health/readiness |
| Database | `database` | PostgreSQL with pgx + Squirrel, transactions, **multi-tenancy** by tenant column or Row-Level Security |
| JOSE | `jose` | JWT, JWS and JWE through explicit profiles, bearer authentication with scopes, JWKS, key rotation |
| Cache | `cache` | Redis or a bounded in-process LRU behind one API: objects, bytes, counters, TTLs, NX, cache-aside `GetOrLoad`, tenant namespaces |
| Crypto | `crypto` (`ncrypto`) | Standalone primitives, no config: XChaCha20-Poly1305 and AES-256-GCM encryption, HMAC-SHA256, bcrypt/Argon2id password hashing, BLAKE3/SHA-256, secure random values and ULIDs |
| Helpers | `helper/safecalc` | Money as integer cents: exact formatting (BRL by default, any separators), float input rounded half-to-even |
| Bootstrap | `deps` | Builds every module from the config and shuts them down in reverse order |

Security defaults stay on: DTD/XXE blocked, payload limits, a restricted
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
pins it with `toolchain go1.27.2`). Import the HTTP package as
`nhttp "github.com/ams-soft/nforge/http"` and the crypto package as
`ncrypto "github.com/ams-soft/nforge/crypto"`.

## Examples

[`_examples/`](_examples) has four runnable applications, each with its own
README:

| Example | Shows |
|---|---|
| [`basic`](_examples/basic) | config, codec, plain and typed routes, docs, health, minimal JWT login, memory cache, PostgreSQL (`standalone.env`: no Docker), an `ncrypto` demo and a `safecalc` test |
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
  server:
    port: 8080
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

Modules such as `database`, `jose` and `cache` are off until enabled. The full
schema, with every default and validation rule, is in
[AGENTS.md §4.2](AGENTS.md#42-full-schema-defaults-and-validation); the
examples' `config.yaml` files are commented.

## Documentation

[`AGENTS.md`](AGENTS.md) is the reference for the whole framework: the
runtime lifecycle, every package, the configuration schema, conventions and
known gotchas. It is written for contributors and AI agents alike.
[`llms.txt`](llms.txt) is a condensed, self-contained version for LLMs and
coding assistants ([llmstxt.org](https://llmstxt.org) format): every package,
its public API, the configuration keys and the pitfalls, with links to the
sources.

## Development

```bash
go build ./... && go vet ./...
go test -race ./...

# integration tests (skip when the variables are unset)
NFORGE_TEST_DATABASE_URL='postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable' \
  go test -tags integration ./database/...
NFORGE_TEST_REDIS_URL='redis://localhost:6379/15' go test -race -tags integration ./cache/

gofmt -l .
GOTOOLCHAIN=local golangci-lint run ./...   # see AGENTS.md §15 for why GOTOOLCHAIN=local
govulncheck ./...
```

See [AGENTS.md §15](AGENTS.md#15-building-and-testing) for the examples,
fuzzers and benchmarks, and §16 for the code conventions.

## License

[MIT](LICENSE) © 2026 AMS Software e Consultoria LTDA
