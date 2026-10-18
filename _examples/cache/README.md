# cache

`deps.Cache` with the same code on both drivers (`cache.driver: redis |
memory`) and both object codecs (`cache.codec: json | cbor`).

```bash
docker compose -f _examples/docker-compose.yml up -d redis
go run ./_examples/cache -config _examples/cache/config.yaml
# or without Docker, on the in-process memory driver:
go run ./_examples/cache -config _examples/cache/config.yaml -envfile _examples/cache/standalone.env
```

| Route | Cache call |
|---|---|
| `PUT /api/v1/items/{key}` `{"name":…,"ttl":"30s"}` | `Set` (empty `ttl` = persistent; a new `Set` replaces the TTL) |
| `GET /api/v1/items/{key}` | `Get` (miss → `cache.ErrNotFound` → 404) |
| `POST /api/v1/items/{key}/claim` | `SetIfAbsent` (atomic NX: `created` is true once) |
| `GET /api/v1/items/{key}/ttl` | `TTL` (`exists`, `persistent`, `remaining_ms`) |
| `POST /api/v1/items/{key}/expire` `{"ttl":"5s"}` | `Expire` (`updated` is false when missing) |
| `DELETE /api/v1/items/{key}` | `Delete` (number of keys removed) |
| `POST /api/v1/counters/{key}` `{"delta":1}` | `Increment` (decimal int64; a non-number → 409) |
| `PUT`/`GET /raw/{key}` | `SetBytes`/`GetBytes` (bytes as is; `?ttl=1m`) |
| `GET /api/v1/products/{id}` | `GetOrLoad` over a deliberately slow catalog |

Every error is mapped to a static response by `sendCacheError` (keys and
backend details never reach the client).

```bash
H=(-H 'Content-Type: application/json')
curl -s localhost:8080/api/v1/items/greeting | jq .status                                  # 404 (miss)
curl -s -X PUT localhost:8080/api/v1/items/greeting "${H[@]}" -d '{"name":"hello","ttl":"30s"}'
curl -s localhost:8080/api/v1/items/greeting | jq .data                                    # {"name":"hello"}
curl -s localhost:8080/raw/items:greeting; echo                                            # {"name":"hello"}: plain JSON, no envelope
curl -s localhost:8080/api/v1/items/greeting/ttl | jq .data
curl -s -X POST localhost:8080/api/v1/items/lock/claim "${H[@]}" -d '{"name":"w1","ttl":"10s"}' | jq .data   # created: true
curl -s -X POST localhost:8080/api/v1/items/lock/claim "${H[@]}" -d '{"name":"w2"}' | jq .data              # created: false
curl -s -X POST localhost:8080/api/v1/counters/visits "${H[@]}" -d '{"delta":1}' | jq .data                 # value: 1, 2, ...

# cache-aside: 10 concurrent misses share ONE load of the catalog
for i in $(seq 10); do curl -s -o /dev/null localhost:8080/api/v1/products/1 & done; wait
```

**GetOrLoad policy.** A cache *read* error other than a miss (Redis down,
corrupt value) is returned and the loader is **not** called (503 here), so an
outage does not become a stampede on the source; a failed *write* of the
loaded value is only logged. Loader errors (product not found) are never
cached. Concurrent loads are shared per process, not across instances. An
application can choose the opposite trade-off and fall back to its database
on `cache.ErrUnavailable`.

**Keys.** Without `cache.prefix` keys are `nforge:<env>:<app.name>:<key>`:

```bash
docker exec redis redis-cli --scan --pattern 'nforge:local:cache-example:*'
```

With `database.tenancy.enabled` the tenant is inserted in the key
(`nforge:<env>:<tenant>:<app.name>:<key>`) and every call needs a tenant in
the context (`nhttp.Tenant()` / `Opts.Tenant`), otherwise `ErrTenantRequired`.

**CBOR.** Objects switch format by configuration only; raw bytes are not
affected. Entries in the old format fail with `ErrDecode` (409 here) instead of
being misses, so change `cache.prefix` or let them expire when you switch:

```bash
go run ./_examples/cache -config _examples/cache/config.yaml -envfile <(echo CACHE_CODEC=cbor)
```

**Outage and recovery.** Readiness pings the cache on every probe:

```bash
docker stop redis
curl -s -o /dev/null -w '%{http_code}\n' localhost:8080/ready         # 503, "cache":{"health":false}
curl -s -o /dev/null -w '%{http_code}\n' localhost:8080/api/v1/products/1   # 503, the loader is not called
docker start redis
curl -s -o /dev/null -w '%{http_code}\n' localhost:8080/ready         # 200 again, same process
```

A write whose reply is lost answers `cache unavailable (outcome unknown)`: it
may have been applied and is never replayed. With Redis down **at startup**
the app does not start.

```bash
go test -race ./_examples/cache/...
NFORGE_TEST_REDIS_URL='redis://localhost:6379/15' go test -race -tags integration ./_examples/cache/...
```
