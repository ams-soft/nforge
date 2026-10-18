# jose

JWT issuance and validation, bearer authentication with scopes, nested
(signed + encrypted) tokens and generic JWS/JWE. No database, cache or Docker.

## 1. Secrets

The example uses the **simple mode**: symmetric secrets given as strings and
read from the environment (never commit them):

```bash
export JWT_SECRET=$(openssl rand -hex 32)             # HS256 signing secret, at least 32 bytes
export JWT_ENCRYPTION_SECRET=$(openssl rand -hex 16)  # dir/A256GCM key, exactly 32 bytes
go run ./_examples/jose -config _examples/jose/config.yaml
```

In `config.yaml` a key is just `secret: ${JWT_SECRET}` (the algorithm
defaults to HS256), and profiles inherit their algorithms from their keys.
With a variable unset the app refuses to start (`secret or source is required`).

**Asymmetric alternative.** With HS256 whoever can validate a token can also
issue one, and nothing can be published. When other services must only
verify, use key files (the commented block in `config.yaml`) and enable
`jose.jwks`:

```bash
go run ./_examples/jose/cmd/keygen        # writes _examples/jose/keys/ (0700 dir, 0600 files; gitignored)
export NFORGE_KEYS_DIR=$PWD/_examples/jose/keys
```

`keygen` writes `access-sign.pem` (Ed25519), `confidential-enc.pem` (RSA
3072), `message-sign.pem` (Ed25519) and `message-enc.key` (a 32-byte
base64url secret), and refuses to overwrite them unless `-force` is given.
The framework itself never generates keys.

## 2. Profiles

The **caller always chooses the profile**; nothing in a token selects it.

| Profile | Kind | What it is |
|---|---|---|
| `access` | `jwt_signed` | API access token: claims signed with HS256 (readable, tamper-proof). `typ: at+jwt`, audience `jose-example-api`, 15 min. |
| `confidential` | `jwt_nested` | Claims signed (inner JWS) **and** encrypted (outer JWE, `dir` + A256GCM, `cty: JWT`), 5 min. |
| `message-jws` | `jws` | Optional: signs arbitrary bytes (integrity). |
| `message-jwe` | `jwe` | Optional: encrypts arbitrary bytes (confidentiality). |

Every incoming token must carry the profile's exact `typ`, a `kid` among
the profile's keys and an allowed algorithm; `iss`, `aud`, `exp`, `iat`,
`jti` and `sub` are required. `http.auth.default_profile: access` is used by
private typed routes without `Opts.AuthProfile`.

## 3. Tokens and routes

`POST /auth/login` is a **demo** login (`admin`/`admin`; `auth.go` shows the
database version in a comment). It accepts `profile` (`access` or
`confidential`) and `scope` (a subset of `wallet:read wallet:write users:write`).

```bash
login() { curl -s -X POST localhost:8080/auth/login -H 'Content-Type: application/json' -d "$1" | jq -r .data.access_token; }
TOKEN=$(login '{"username":"admin","password":"admin"}')
READ_ONLY=$(login '{"username":"admin","password":"admin","scope":"wallet:read"}')
CONFIDENTIAL=$(login '{"username":"admin","password":"admin","profile":"confidential"}')
code() { curl -s -o /dev/null -w '%{http_code}\n' "$@"; }
```

| Route | Protection |
|---|---|
| `GET /public` | none |
| `GET /me` | typed private route, default profile |
| `GET /raw/wallets` | raw: `Authenticate(dps.JOSE, "access", WithScopes("wallet:read", "wallet:write"))` |
| `GET /raw/wallets-slice` | the same with `WithScopes(required...)` |
| `GET /api/v1/wallets` | typed: `IsPrivate`, `AuthProfile: "access"`, `RequiredScopes: ["wallet:read"]` |
| `GET /api/v1/confidential` | typed: `AuthProfile: "confidential"` (nested tokens only) |
| `POST /api/v1/jose/demo` | JWS + JWE round trip with the generic profiles |

```bash
code localhost:8080/raw/wallets -H "Authorization: Bearer $TOKEN"               # 200: both scopes
code localhost:8080/raw/wallets                                                  # 401: no token
code localhost:8080/raw/wallets -H "Authorization: Bearer ${TOKEN%?????}AAAAA"   # 401: tampered
code localhost:8080/raw/wallets -H "Authorization: Bearer $READ_ONLY"            # 403: wallet:write missing
code localhost:8080/api/v1/wallets -H "Authorization: Bearer $READ_ONLY"         # 200: only wallet:read needed
code localhost:8080/api/v1/confidential -H "Authorization: Bearer $TOKEN"        # 401: access token on a nested route
code localhost:8080/api/v1/confidential -H "Authorization: Bearer $CONFIDENTIAL" # 200
curl -s localhost:8080/me -H "Authorization: Bearer $TOKEN" | jq .data
curl -s -X POST localhost:8080/api/v1/jose/demo -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d '{"message":"hello"}' | jq .data
```

Failures use the envelope plus a `WWW-Authenticate` challenge, e.g.
`Bearer realm="nforge-api", error="insufficient_scope", scope="wallet:read wallet:write"`.
Typed routes use `http.auth.realm` (`jose-example`); raw `Authenticate` uses
`nforge-api` unless you pass `nhttp.WithRealm`.

Handlers read the identity with `nhttp.PrincipalFromContext(r.Context())`:
`Subject`, `Issuer`, `Audience`, `TokenID` come from the validated claims;
`TenantID`, `Roles` and `Scopes` from the custom claims `tenant_id`, `roles`
and `scope`. The raw token is never stored or logged.

```bash
go test -race ./_examples/jose/...
```
