# database

`deps.DB` on PostgreSQL: repositories that build SQL with Squirrel
(`db.Query`) and run it with pgx (`db.Executor`), transactions, error
mapping and multi-tenancy.

```bash
docker compose -f _examples/docker-compose.yml up -d postgres
go run ./_examples/database -config _examples/database/config.yaml
```

The tables are created at startup only to keep the example self-contained;
a real service uses migrations.

## Users: CRUD and transactions (`users.go`)

```bash
H=(-H 'Content-Type: application/json')
curl -s -X POST localhost:8080/api/v1/users "${H[@]}" -d '{"name":"Ana","email":"ana@example.com"}'
curl -s -X POST localhost:8080/api/v1/users/batch "${H[@]}" -d '[{"name":"Bia"},{"name":"Caio","active":false}]'  # one transaction
curl -s 'localhost:8080/api/v1/users?limit=10&name=a&active=true'
curl -s -X PUT localhost:8080/api/v1/users/1 "${H[@]}" -d '{"name":"Ana Maria"}'
curl -s -X DELETE -o /dev/null -w '%{http_code}\n' localhost:8080/api/v1/users/1   # 204, then 404
```

Repository functions take `*database.DB`, so the same `insertUser` runs on
the pool and inside `db.Transaction` (where only `tx` may be used).
`pgx.ErrNoRows` becomes 404, a `DELETE` that affected no row too, and any
other database error is logged and answered as a generic 500.

## Notes: multi-tenancy (`notes.go`)

`database.tenancy` is on in **column** mode and `http.tenant.source: header`:
the tenant is the `X-Tenant-ID` header (a UUID v4). (The `jose` example's
tokens can carry it as the `tenant_id` claim instead: `source: claim`.)

| Route | How the tenant gets there |
|---|---|
| `GET/POST /api/v1/notes`, `GET /api/v1/notes/{id}`, `POST /api/v1/notes/batch` | typed: `Opts{Tenant: true}` |
| `GET /raw/notes` | raw: `r.With(server.TenantMiddleware())` |

Handlers call `db.ForContext(r.Context())` and build queries with
`tdb.TenantEq()` (an unscoped query fails to build), and write the tenant
with `tdb.TenantColumn()`/`tdb.TenantID()`; the batch uses `tdb.Transaction`.

```bash
A=6f1c3e2a-8b4d-4f7e-9a1b-2c3d4e5f6a7b; B=9b2d4f6a-1c3e-4a5b-8d7f-0e1f2a3b4c5d
curl -s -X POST localhost:8080/api/v1/notes -H "X-Tenant-ID: $A" "${H[@]}" -d '{"text":"from A"}'
curl -s -X POST localhost:8080/api/v1/notes/batch -H "X-Tenant-ID: $B" "${H[@]}" -d '[{"text":"b1"},{"text":"b2"}]'
curl -s localhost:8080/api/v1/notes -H "X-Tenant-ID: $A" | jq '.data[].text'   # only "from A"
curl -s localhost:8080/raw/notes    -H "X-Tenant-ID: $B" | jq '.data[].text'   # only b1, b2
curl -s -o /dev/null -w '%{http_code}\n' localhost:8080/api/v1/notes                      # 403: no tenant
curl -s -o /dev/null -w '%{http_code}\n' localhost:8080/api/v1/notes -H 'X-Tenant-ID: acme' # 400: not a UUID v4
```

A header is client-controlled: only trust it behind a gateway that sets it,
or check that the caller belongs to the tenant.

### Row-Level Security

The compose user `app` is a superuser, and superusers ignore RLS, so in
`rls` mode nForge refuses to start with it. Create a regular role once and
start with `rls.env` (user `app_tenant`, `mode: rls`):

```bash
docker exec -i postgres psql -U app -d app < _examples/database/rls-setup.sql
go run ./_examples/database -config _examples/database/config.yaml -envfile _examples/database/rls.env
```

`ensureNotesSchema` then enables and forces RLS on `notes` with a
`tenant_isolation` policy on `current_setting('app.tenant_id', true)`, and the
framework sets that value transaction-locally for each statement. The same
commands give the same results, now enforced by PostgreSQL even for a query
that forgets `TenantEq`. Cross-tenant jobs use a second `database.Open`
with a `BYPASSRLS` role, outside `Deps`.

```bash
go test -race ./_examples/database/...
NFORGE_TEST_DATABASE_URL='postgres://app:app@localhost:5432/app?sslmode=disable' \
  go test -race -tags integration ./_examples/database/...
```
