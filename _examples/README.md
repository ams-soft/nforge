# nForge examples

Each directory is a small, runnable application that focuses on one part of
the framework. Start with `basic`, then pick the module you need.

| Example | Shows | Needs |
|---|---|---|
| [`basic`](basic) | terminal logging, HTTP, and an in-memory typed CRUD API | none |
| [`complete`](complete) | HTTPS + HTTP/3, typed routes, docs, JWT, cache, PostgreSQL, RabbitMQ, crypto and observability | PostgreSQL; RabbitMQ optional |
| [`jose`](jose) | JWT login, bearer authentication, scopes, `Principal`, nested (signed + encrypted) tokens, generic JWS/JWE, JWKS and key files | two secrets in the environment |
| [`cache`](cache) | the cache module on Redis or memory: objects, TTL, NX, counters, raw bytes, `GetOrLoad`, CBOR, outages | Redis (or `standalone.env`) |
| [`database`](database) | the database module: Squirrel + pgx repositories, transactions, error mapping, multi-tenancy in column or Row-Level Security mode | PostgreSQL |
| [`static`](static) | static files and a built single-page application next to the API: `Static`, `SPA`, directories and `embed.FS`, cache headers, reserved prefixes | nothing |
| [`message`](message) | worker-only RabbitMQ publisher and consumers, Classic priority, Quorum queues, ACK/NACK, DLQ, retry and reprocessing | RabbitMQ (`rabbitmq` service in Compose) |

All commands run from the **repository root**. Every example reads its own
`config.yaml` (pass it with `-config`). The HTTP examples listen on
`127.0.0.1:8080`, with API docs at <http://localhost:8080/docs>; the message
example is a worker-only application with no HTTP listener.

## Local dependencies

Each example keeps its environment examples in its own folder. Examples that
need Docker services have a local Compose file; JOSE has no external service
dependency. Copy a `.env.example` to `.env` in that same folder before using
Compose. The `.env` files are ignored by Git. The complete example also includes
the optional observability stack, configured in [`observability/`](observability).

```bash
cp _examples/complete/.env.example _examples/complete/.env
docker compose --env-file _examples/complete/.env -f _examples/complete/docker-compose.yml up -d

cp _examples/cache/.env.example _examples/cache/.env
docker compose --env-file _examples/cache/.env -f _examples/cache/docker-compose.yml up -d

cp _examples/database/.env.example _examples/database/.env
docker compose --env-file _examples/database/.env -f _examples/database/docker-compose.yml up -d

cp _examples/message/.env.example _examples/message/.env
docker compose --env-file _examples/message/.env -f _examples/message/docker-compose.yml up -d
```

Stop a stack with `docker compose --env-file <example>/.env -f <example>/docker-compose.yml down`.

## Build and test

Go wildcards skip directories that start with `_`, so name the examples
explicitly:

```bash
EXAMPLES='./_examples/basic/... ./_examples/complete/... ./_examples/jose/... ./_examples/cache/... ./_examples/database/... ./_examples/message/...'
go vet $EXAMPLES
GOTOOLCHAIN=local golangci-lint run $EXAMPLES
go test -race $EXAMPLES

# against the local stack (tests that need it skip when the variables are unset)
NFORGE_TEST_DATABASE_URL='postgres://app:app@localhost:5432/app?sslmode=disable' \
NFORGE_TEST_REDIS_URL='redis://localhost:6379/15' \
  go test -race -tags integration $EXAMPLES
NFORGE_TEST_RABBITMQ_URL='amqp://nforge:nforge@localhost:5672/' \
  go test -race -tags integration ./message/ ./_examples/message/...
```
