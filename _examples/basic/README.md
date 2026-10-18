# basic

The smallest nForge application, with a minimal taste of each module: a JWT
login, an in-memory cache, PostgreSQL and the `ncrypto` primitives (`standalone.env` turns the
database off, so no Docker is needed). The demo login's HS256 secret is inline in
`config.yaml` (generate your own with `openssl rand -hex 32`; outside local
runs read it with `${JWT_SECRET}`).

```bash
docker compose -f _examples/docker-compose.yml up -d postgres
go run ./_examples/basic -config _examples/basic/config.yaml

# without Docker: no database, /db/notes is not registered
go run ./_examples/basic -config _examples/basic/config.yaml -envfile _examples/basic/standalone.env
```

| File | What it shows |
|---|---|
| `main.go` | the bootstrap: `config.Load` → `deps.NewDeps` → `nhttp.NewHTTPServer` → global middleware → routes → `Start` (graceful shutdown on Ctrl+C) |
| `auth.go` | a minimal JWT login: `POST /auth/login` with `admin@admin.com` / `admin123` issues a token (`jose.IssueJWT`), and the private typed route `GET /auth/me` (`IsPrivate`) only runs with it. Scopes, nested tokens and JWKS are in the [jose example](../jose) |
| `cache.go` | `PUT`/`GET`/`DELETE /cache/{key}` over `deps.Cache` on the memory driver (`cache.Set/Get/Delete`, optional TTL). Redis, counters, NX and `GetOrLoad` are in the [cache example](../cache) |
| `crypto.go` | `runCrypto`, a startup demo of the standalone `ncrypto` package: XChaCha20-Poly1305 with associated data, HMAC-SHA256 with its own key, bcrypt and Argon2id hashes, BLAKE3, a random token and a ULID. It is off by default (uncomment `runCrypto(&dps.Logger)` in `main.go`; password hashing is slow on purpose); `crypto_test.go` runs it |
| `safecalc_test.go` | the `helper/safecalc` money helper: amounts kept as integer cents, a float accepted only at the edge (`NewFromFloat`, rounded half to even), `Format` in BRL and US formats, plus a runnable `ExampleMoney_Format` |
| `database.go` | `/db/notes` create/list/get/delete with Squirrel + pgx, not registered when the database is off (`standalone.env`). Filters, transactions and tenancy are in the [database example](../database) |
| `plain.go` | plain routes that parse and answer by hand in JSON, XML and CBOR, and `AddDocRoute` to document one |
| `users.go` | typed routes (`TGet`/`TPost`/`TPut`/`TDelete`) over an in-memory store: one `User` struct for every format, `codec:"required"`, `Validate()`, a typed query string with defaults, a route whose codec is XML |
| `config.yaml` | only the keys this example uses, commented; OpenTelemetry is preconfigured for the local collector (`localhost:4317`, insecure) but off: set `logging.output.otel.enabled: true` with the compose stack up to see logs and traces in Grafana (<http://localhost:3000>, dashboard *nForge · Logs*) |

## Try it

```bash
curl -s -X POST localhost:8080/api/v1/users -H 'Content-Type: application/json' -d '{"name":"Ana","email":"ana@example.com"}'
curl -s -X POST localhost:8080/api/v1/users -H 'Content-Type: application/json' -d '{"name":"Bia","active":false}'
curl -s 'localhost:8080/api/v1/users?limit=10&active=true'        # typed query: filters + paging
curl -s 'localhost:8080/api/v1/users'                             # 400: limit is required
curl -s 'localhost:8080/api/v1/users?limit=500'                   # 422: Validate() rejected it
curl -s -X POST localhost:8080/api/v1/users -H 'Content-Type: application/json' -d '{"email":"x@y.z"}'  # 400: name is required
curl -s -X DELETE -o /dev/null -w '%{http_code}\n' localhost:8080/api/v1/users/1   # 204

# the same struct in XML and CBOR
curl -s -X POST localhost:8080/api/v1/xml/users -H 'Content-Type: application/xml' -d '<user><name>Caio</name></user>'
curl -s -X POST localhost:8080/echo/xml -H 'Content-Type: application/xml' -d '<user id="1"><name>Ana</name></user>'
printf '\xa1\x64name\x63Ana' | curl -s -X POST localhost:8080/echo/cbor -H 'Content-Type: application/cbor' --data-binary @- | od -c

curl -s -D - -o /dev/null localhost:8080/health | grep -i clacks  # the global middleware

# login and a private route
TOKEN=$(curl -s -X POST localhost:8080/auth/login -H 'Content-Type: application/json' \
  -d '{"email":"admin@admin.com","password":"admin123"}' | jq -r .data.access_token)
curl -s localhost:8080/auth/me -H "Authorization: Bearer $TOKEN"  # 200: sub, iss, aud, jti
curl -s localhost:8080/auth/me                                    # 401 + WWW-Authenticate: Bearer

# cache (memory driver)
curl -s -X PUT localhost:8080/cache/greeting -H 'Content-Type: application/json' -d '{"value":"hello","ttl":"30s"}'
curl -s localhost:8080/cache/greeting                             # 200, then 404 after 30s

# database (not with standalone.env)
curl -s -X POST localhost:8080/db/notes -H 'Content-Type: application/json' -d '{"text":"hello"}'
curl -s localhost:8080/db/notes
```

Every response uses the standard envelope:

```json
{"success":true,"status":201,"data":{"id":1,"name":"Ana","email":"ana@example.com","active":true}}
{"success":false,"status":422,"error":{"title":"422 ERROR UNPROCESSABLE ENTITY","detail":"…","instance":"/api/v1/users"}}
```

Docs: <http://localhost:8080/docs> (also `/docs/swagger.json` and `/docs/swagger.yaml`).

```bash
go test -race ./_examples/basic/...                       # includes the crypto demo and safecalc
NFORGE_TEST_DATABASE_URL='postgres://app:app@localhost:5432/app?sslmode=disable' \
  go test -race -tags integration ./_examples/basic/...   # the /db/notes routes
```
