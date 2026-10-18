# nForge examples

Each directory is a small, runnable application that focuses on one part of
the framework. Start with `basic`, then pick the module you need.

| Example | Shows | Needs |
|---|---|---|
| [`basic`](basic) | config, logging, JSON/XML/CBOR from one struct, plain and typed routes, typed query strings, OpenAPI docs, health/readiness, a global middleware, plus a minimal JWT login, memory cache, PostgreSQL notes, an `ncrypto` demo and a `safecalc` test | PostgreSQL (or `standalone.env`: nothing) |
| [`jose`](jose) | JWT login, bearer authentication, scopes, `Principal`, nested (signed + encrypted) tokens, generic JWS/JWE, JWKS and key files | two secrets in the environment |
| [`cache`](cache) | `deps.Cache` on Redis or memory: objects, TTL, NX, counters, raw bytes, `GetOrLoad`, CBOR, outages | Redis (or `standalone.env`) |
| [`database`](database) | `deps.DB`: Squirrel + pgx repositories, transactions, error mapping, multi-tenancy in column or Row-Level Security mode | PostgreSQL |

All commands run from the **repository root**. Every example reads its own
`config.yaml` (pass it with `-config`) and listens on `127.0.0.1:8080`, with
the interactive API docs at <http://localhost:8080/docs>.

## Local stack

[`docker-compose.yml`](docker-compose.yml) starts everything the examples can
use:

| Service | Port | Used by |
|---|---|---|
| PostgreSQL 18 (`app`/`app`, database `app`) | 5432 | basic, database |
| Redis 8 | 6379 | cache |
| OpenTelemetry Collector (OTLP gRPC / HTTP) | 4317 / 4318 | any example with `logging.output.otel.enabled` (`basic` is preconfigured: flip it to `true`) |
| Tempo (traces) | 3200 | via Grafana |
| Loki (logs) | 3100 | via Grafana |
| Grafana (anonymous admin) | 3000 | dashboard *nForge · Logs*, Explore → Tempo |

```bash
docker compose -f _examples/docker-compose.yml up -d            # everything
docker compose -f _examples/docker-compose.yml up -d postgres   # or only what you need
docker compose -f _examples/docker-compose.yml down             # stop (add -v to drop the data)
```

The collector, Tempo, Loki and Grafana configuration lives in
[`observability/`](observability).

## Build and test

Go wildcards skip directories that start with `_`, so name the examples
explicitly:

```bash
EXAMPLES='./_examples/basic/... ./_examples/jose/... ./_examples/cache/... ./_examples/database/...'
go vet $EXAMPLES
GOTOOLCHAIN=local golangci-lint run $EXAMPLES
go test -race $EXAMPLES

# against the local stack (tests that need it skip when the variables are unset)
NFORGE_TEST_DATABASE_URL='postgres://app:app@localhost:5432/app?sslmode=disable' \
NFORGE_TEST_REDIS_URL='redis://localhost:6379/15' \
  go test -race -tags integration $EXAMPLES
```
