# gap.md — nForge audit: vulnerabilities, gaps and bugs

Audit of `main` at `45f67c2` (after the JSON decode work and the HTTPS/HTTP3 merge).
Go 1.27.3, linux/amd64.

**Status: all 40 items are resolved on `main`.** Each item below carries a
`[RESOLVED]` tag and a "Resolved in" note naming the commit subject, what
changed and the tests that pin it; the findings text is kept as it was written
at audit time (corrections made while fixing are called out inside the item,
for example G-21 and G-38). The sections after "Summary" are the audit record,
section 7 is the work log.

**Evidence levels** (used on every item):

- **[PoC]** reproduced by running code; the numbers quoted are measured.
- **[CODE]** read in the source; not executed. Treat as a strong lead, not a proof.
- **[TOOL]** reported by a scanner.

**Severity** is the author's judgement for an app that follows `AGENTS.md`.

PoC files were temporary and are deleted; each item says how to reproduce.

---

## 0. Summary

| # | Sev | Evidence | Where | One line |
|---|---|---|---|---|
| G-01 | High | PoC | `codec/xml.go` | **RESOLVED** XML decode memory is quadratic in nesting depth: 683 KB -> 10 GB |
| G-02 | Med-High | PoC | `config/config.go` | **RESOLVED** Unknown YAML keys ignored outside `jose`; a typo in `cors.origins` means "any origin" |
| G-03 | Medium | PoC | `deps/deps.go` | **RESOLVED** Fatal startup errors are silent with default logging; no flush with OTel |
| G-04 | Medium | CODE | `http/internal/openapi` | **RESOLVED** Docs page loads Swagger UI from a floating CDN version, no SRI, host-wide CSP allowlist |
| G-05 | Medium | PoC | `codec/presence*.go` | **RESOLVED** JSON tree fallback is quadratic: 131 KB -> 337 MB |
| G-06 | Low | PoC | `codec/json.go` | **RESOLVED** Decode errors leak Go package-qualified type names to clients |
| G-07 | Low | CODE | `codec/xml.go:763` | **RESOLVED** Non-English error text in production code |
| G-08 | Low | CODE | `http/transport.go` | **RESOLVED** TLS/QUIC certificates are loaded once; no rotation without restart |
| G-09 | Low | CODE | `config/server.go` | **RESOLVED** TLS private key file permissions are never checked |
| G-10 | Low | CODE | `http/transport.go` | **RESOLVED** HTTPS and HTTP/3 servers ignore customisations of `ServerHttp.Srv` |
| G-11 | Low | CODE | `http/routes.go` | **RESOLVED** Docs, spec and readiness endpoints are public; spec rebuilt per request |
| G-12 | Low | CODE | `http/` | **RESOLVED** Raw (untyped) routes have no request-body limit |
| G-13 | Low | CODE | `config/config.go` | **RESOLVED** `${VAR}` that is unset silently becomes `""`; a missing `-config` file is silently ignored |
| G-14 | Info | CODE | `http/internal/authn` | **RESOLVED** `Bearerx token` answers 400 instead of 401 |
| G-15 | Medium | PoC | `cache/load.go` | **RESOLVED** `GetOrLoad` hard-fails on an undecodable entry and never calls the loader until the TTL ends |
| G-16 | Medium | CODE | `config`, `logs` | **RESOLVED** `env` is never validated: `production`/`prod` runs at DEBUG and skips the prd overlay |
| G-17 | Low | CODE | `telemetry`, `http/internal/tracing` | **RESOLVED** 100 % sampling, no sampler setting; client picks the trace/request id and baggage |
| G-18 | Low | CODE | `logs/logs.go` | **RESOLVED** `AddSource: true` on every record, including OTel export |
| G-19 | Info | CODE | `jose/service.go` | **RESOLVED** `Reload` is never triggered by the framework; the replaced snapshot is not wiped |
| G-20 | Info | CODE | `cache/load.go` | **RESOLVED** A shared load runs with the first caller's context values and hands its error to every waiter |
| G-21 | Medium | PoC | `config/env.go` | **RESOLVED** Env-file values undergo `$VAR` expansion from the process env; passwords with `$NAME` are corrupted |
| G-22 | Medium | PoC | `config/env.go` | **RESOLVED** Env-file keys that match no field are ignored (`HTTP_CORS_ORIGNS`) |
| G-23 | Low-Med | CODE | `http/response.go` | **RESOLVED** Missing `Content-Type` is accepted: cross-site JSON POSTs without preflight |
| G-24 | Low | CODE | `config/security.go` | **RESOLVED** HSTS `includeSubDomains` on by default |
| G-25 | Low | CODE | `config/config.go` | **RESOLVED** Unreadable overlay file is skipped silently |
| G-26 | Low | CODE | `config/config.go` | **RESOLVED** `codec.max_payload_bytes` has no upper bound |
| G-27 | Medium | PoC | `codec/fields.go`, `cbor.go`, `xml.go` | **RESOLVED** Cyclic values crash the process: CBOR/XML encode and `CheckRequired` (fatal stack overflow) |
| G-28 | Low | PoC | `http/internal/capture` | **RESOLVED** `CheckRequired` walks every response (1.9 ms per MB) even with no `required` tags |
| G-29 | Low-Med | CODE | `codec/registry.go` | **RESOLVED** `Validate()` only runs on the root value, never on nested types |
| G-30 | Med-High | PoC | `config`, `database` | **RESOLVED** `config.Config` and `database.Config` print passwords/DSNs; slog JSON leaks every secret incl. JOSE key |
| G-31 | Low | CODE | `jose/config.go` | **RESOLVED** No ceiling on JWT `ttl`/`max_ttl` |
| G-32 | Low | CODE | `cache`, `database` | **RESOLVED** Credentials allowed over plain TCP to remote hosts, no warning |
| G-33 | Low | CODE | `cache` | **RESOLVED** No atomic increment-with-TTL: counters can live forever |
| G-34 | Low-Med | PoC | `database/config.go` | **RESOLVED** `redactDSN` leaves `?password=` and `?sslpassword=` visible |
| G-35 | Low-Med | CODE | `http/server.go`, `database` | **RESOLVED** No handler timeout and no `statement_timeout` by default |
| G-36 | Medium | PoC | `config/bytesize.go` | **RESOLVED** `ByteSize` overflows to `MinInt64` with no error (cache copy is correct) |
| G-37 | Low | CODE | `cache`, `codec` | **RESOLVED** `cache.max_value_bytes` can exceed `codec.max_payload_bytes`: stored but unreadable |
| G-38 | Low | PoC | `helper/safecalc` | **RESOLVED** `Round` returns NaN/Inf for finite input; fixed two decimals |
| G-39 | Low | CODE | `config/security.go` | **RESOLVED** Security headers and cache control off by default |
| G-40 | Info | CODE | `deps/deps.go` | **RESOLVED** `Shutdown` not safe for concurrent calls |

Non-findings (checked and fine) are listed in section 3.
Coverage limits are in section 4.

---

## 1. Findings in detail

### G-01 — XML decode: memory is quadratic in nesting depth (High, PoC) [RESOLVED]

**Where.** `codec/xml.go`, `xmlDecoder.structValue` calls
`d.value(tk, fv, joinPath(path, fl.name), childNS)` at line 667 (and the map
branch at 737). `joinPath` is in `codec/presence.go:157`.

**What happens.** Every nested element builds a fresh string `parent.path + "." + name`
eagerly, even when no error will ever be reported. The strings are all alive
at once because they are arguments of the recursive call. For a document nested
`n` deep with a 1-character name the total is about `n^2 * 1` bytes, plus the
Go stack of the recursion itself. There is no depth limit (`grep -n depth codec/xml.go`
finds nothing), unlike JSON where the engine stops at depth 10000.

**Reproduction.** Temporary test in `package codec`:

```go
type pocNode struct {
    XMLName xml.Name `xml:"a"`
    A       *pocNode `xml:"a"`
}
r, _ := New(Config{Formats: []string{"xml"}, Default: "xml"})
payload := strings.Repeat("<a>", n) + strings.Repeat("</a>", n)
err := r.DecodeAs("xml", []byte(payload), &pocNode{})
```

**Measured (this machine).**

| depth | payload | time | process Sys memory | result |
|---|---|---|---|---|
| 10 000 | 68 KB | 30 ms | 135 MB | ok |
| 100 000 | 683 KB | 2.45 s | **10 078 MB** | ok (decoded fine) |
| 1 000 000 | 6.8 MB | 0.6 ms | - | rejected by the 5 MiB payload limit |

So a request well **under** the default 5 MiB limit exhausts RAM. A handful of
parallel requests is an OOM kill of the whole process.

**Who is affected.** Any application with `xml` in `codec.formats.enabled`,
and any typed route with `Opts.Codec: "xml"`. The XML example in
`_examples/basic` does this. The process default registry created by
`codec.init()` also includes XML until `deps.NewDeps` replaces it.

**Impact.** Unauthenticated denial of service (private routes authenticate
before decoding, public ones do not).

**Fix direction.**
1. Add a maximum element depth to the XML decoder (a few hundred is plenty for
   an API; make it a `codec` setting with a safe default) and return a
   `SyntaxError`-like error.
2. Stop building path strings eagerly: keep a path stack (as `presence_json.go`
   already does) and render the string only when an error is created.
3. Add a regression test with depth 100 000 asserting the limit error and a
   memory/time bound.


> **Resolved** in `fix(codec): bound XML nesting and make locations lazy`. XML nesting is limited to 256 levels and error locations are xpath linked lists rendered on demand (codec/xpath.go). Tests: TestXMLNestingIsBounded (100 000 levels now fails fast with a SyntaxError and allocates < 64 MB), TestXMLUnknownDeepContentIsSkippedCheaply.

---

### G-02 — Unknown config keys are ignored outside `jose` (Med-High, PoC) [RESOLVED]

**Where.** `config/config.go:255` `loadYAMLFile` ends with `root.Decode(cfg)`,
which is permissive. Only `checkJOSEKeys` (line ~293) calls
`dec.KnownFields(true)`, and only on the `jose` subtree.

**What happens.** A typo in any other section is accepted without a word.
Several of those sections are security controls, and the zero value of the
mistyped field is the *unsafe* one.

**Reproduction.** `config.yaml`:

```yaml
app: {name: poc, version: 1.0.0}
http:
  security: {enabld: true}
  cors: {enabled: true, origns: ["https://a.example"], credentails: true}
  server: {tls: {enabled: false, min_verison: "1.0"}}
database: {enabled: false, tenancy: {enabled: false, modee: rls}}
```

`config.LoadFlags(path, "")` returned `err=nil` with:

```
security.enabled=false  cors.enabled=true  origins=[]  credentials=false
```

**Why this is dangerous.**

- `cors.enabled: true` + typo in `origins` -> `origins` is empty -> the CORS
  middleware treats `[]` as **any origin**. The operator believed they had a
  whitelist.
- `security.enabld` -> security headers (CSP, HSTS, frame options) silently
  off.
- `tls.min_verison`, `tenancy.modee`, `auth.default_profil`, `tenant.sorce`,
  `routes.readyness`, ... all fall back to defaults with no signal.

The env-file path is stricter (an unknown key is an error for sequences), so
the YAML path is the outlier.

**Fix direction.** Decode the whole file with `KnownFields(true)` (keep the
`jose` special handling if its message matters) and fail startup on unknown
keys, naming the key and line. Keep a flag to opt out if existing deployments
carry extra keys. Add tests for a typo in each security-relevant section.


> **Resolved** in `fix(config): fail on configuration typos instead of defaulting`. Strict YAML (KnownFields) with top-level x- extension keys, strict env-file keys, env validated and normalized, explicit -config must exist, unreadable overlay is an error, unset ${VAR} reported in Config.Warnings and logged by deps. Tests: config/strict_test.go, deps TestConfigWarningsAreLogged.

---

### G-03 — Fatal startup errors disappear (Medium, PoC) [RESOLVED]

**Where.** `deps/deps.go`: `os.Exit(1)` at lines 114, 146, 165, 185, 216
(logger, codec, database, jose, cache). Tracing failure at line 122 is a
`panic(err)`.

**What happens.**

1. With logging disabled (the default: `logging.output.*.enabled: false`) the
   logger is the no-op handler, so `d.Logger.ErrorContext(... "[Fatal ERROR]
   unable to setup database" ...)` is thrown away and the process exits 1
   **with no output at all**.
2. With OTel logging enabled the record goes to a batch processor; `os.Exit(1)`
   runs before `Deps.Shutdown`, so the batch is never flushed and the fatal
   message never reaches the collector. Only the terminal sink (if enabled)
   shows it.
3. Resources opened earlier in the same `NewDeps` call are not closed.

**Reproduction.** Temporary `main` in the module:

```go
cfg := config.Config{Env: "local"}
cfg.App.Name, cfg.App.Version = "poc", "1"
cfg.Database.Enabled = true
cfg.Database.DSN = "postgres://u:p@127.0.0.1:1/db?connect_timeout=1"
deps.NewDeps(cfg)
```

`go run` printed nothing and exited with status 1.

**Impact.** Operability: the most common production incident (bad DSN, wrong
key permissions, Redis down) is undiagnosable from process output under the
default configuration. A secondary design smell: a library function that calls
`os.Exit` cannot be embedded or unit-tested (every failure test needs a
subprocess).

**Fix direction.** At minimum write the fatal error to `os.Stderr` (always) and
run the registered shutdown functions before exiting. Better: add
`NewDepsE(cfg) (*Deps, error)` and keep `NewDeps` as the exit-on-error wrapper.


> **Resolved** in `fix(deps): report fatal startup errors and add NewDepsE`. NewDepsE returns the error after logging it and shutting down earlier modules; NewDeps prints it to stderr and exits 1; a tracing failure is an error, not a panic. Tests: deps/startup_test.go (including a subprocess test of the stderr output).

---

### G-04 — Docs page: unpinned CDN script, no SRI, host-wide CSP allowlist (Medium, CODE) [RESOLVED]

**Where.** `http/internal/openapi/docs.html:20` and `:280`
(`https://cdn.jsdelivr.net/npm/swagger-ui-dist@5/...`), and
`http/internal/openapi/page.go:81` `pageCSP`.

**What happens.**

- `swagger-ui-dist@5` is a floating major: every page load gets whatever the
  latest 5.x is at that moment.
- No `integrity=` attribute on the script or the stylesheet.
- The CSP sent with the page is
  `script-src 'nonce-<n>' https://cdn.jsdelivr.net` (and the same host in
  `style-src`). jsDelivr serves arbitrary npm packages and GitHub files, so the
  allowlist permits attacker-controlled JavaScript if the page ever has an
  injection point; the nonce is then not the only gate.

**Impact.** Supply-chain: a compromised or malicious 5.x release (or a CDN
incident) runs JS in the API's own origin. That page is where users type a
bearer token into "Authorize", so the token is exposed. Severity is Medium
because the docs are off by default (`http.docs.enabled: false`) and meant for
non-production.

**Fix direction.** Pin an exact version, add SRI hashes, or embed the Swagger
UI bundle with `//go:embed` and serve it from `'self'` (also removes the
Google Fonts dependency and works offline). Drop the CDN from `script-src`.


> **Resolved** in `fix(http): pin Swagger UI with SRI and drop the CDN from script-src`. docs.html loads swagger-ui-dist@5.33.1 with sha384 integrity (hashes cross-checked against the npm tarball) and script-src is the nonce alone. Tests: TestDocsPageAssetsArePinnedWithIntegrity, openapi TestRenderPagePinsSwaggerUIAndDropsTheScriptHost.

---

### G-05 — JSON presence fallback is quadratic (Medium, PoC) [RESOLVED]

**Where.** `codec/presence_json.go` falls back to `checkPresenceTree` on
duplicate names, invalid UTF-8 or trailing data; that path calls
`checkPresence` (`codec/presence.go`) which builds `joinPath(path, ...)`
strings at lines 88 and 121, again eagerly and at every level.

**Reproduction.** Recursive type, strict mode, and a duplicate member at the
root so the fast walker gives up:

```go
type pocRec struct {
    Next *pocRec `json:"next"`
    V    int     `json:"v" codec:"required"`
}
// StrictUnknownFields: true
payload := `{"v":1,"v":2,"next":` + strings.Repeat(`{"v":1,"next":`, n) + `null` + strings.Repeat(`}`, n) + `}`
```

| depth | payload | time | Sys memory |
|---|---|---|---|
| 1 000 | 14 KB | 3.4 ms | 23 MB |
| 5 000 | 73 KB | 37 ms | 112 MB |
| 9 000 | 131 KB | 106 ms | 337 MB |

Bounded by the JSON depth limit (10 000), so it is an amplification of about
2 500x rather than unbounded, but it is attacker-triggerable with a single
duplicated key, only needs a recursive request type with strict or required
fields, and several requests in parallel add up.

The same eager-path pattern also exists for CBOR (`checkPresence(fmtCBOR, ...)`),
where the library depth limit (32) keeps it harmless.

**Fix direction.** Same as G-01: path stack, render on error. Also consider
making the fallback decision cheaper to trigger-proof (e.g. detect duplicates
without building the whole tree).


> **Resolved** in `fix(codec): bound XML nesting and make locations lazy`. The tree fallback of the JSON/CBOR presence check uses the same lazy xpath; TestJSONPresenceFallbackIsNotQuadratic keeps a 9 000-deep fallback under 128 MB (was 337 MB).

---

### G-06 — Go type names leak to clients (Low, PoC) [RESOLVED]

**Where.** `codec/json.go:179` (`Expected: typ.Type.String()`), surfaced by
`TypeError.Error()` (`codec/errors.go:94`) and sent verbatim by
`decodeRequest` -> `write(..., err.Error())` in `http/typed_route.go`.

**Evidence.** Decoding `{"addr":"x"}` into a struct with an `Addr pocAddr` field
yields:

```
codec: json: field "addr" expects nhttp.pocAddr, got value string
```

A request to a typed route puts that text in `error.detail`.

**Impact.** Discloses internal package and type names (information that helps
fingerprinting and targeted payload crafting). Not exploitable by itself.

**Fix direction.** Map `Expected` to a wire-level word (`object`, `string`,
`integer`, `array`, `boolean`) in the client-facing message; keep the Go type
in the log. The same applies to the XML and query decoders, which also put
`t.String()` into `TypeError`.


> **Resolved** in `fix(codec): describe type mismatches in payload terms`. TypeError.Error() prints Wire (codec.WireType) instead of the Go type; Expected keeps the Go type. Tests: codec/wire_test.go, query TestTypeErrorsDoNotNameGoTypes.

---

### G-07 — Non-English text in production code (Low, CODE) [RESOLVED]

`codec/xml.go:763`: `Got: "elemento <" + tk.Name.Local + ">"`.
`AGENTS.md` section 16 requires English for error strings and API response
texts. This one reaches clients through the 400 detail. Replace with
`"element <...>"` and add a test that no non-ASCII word appears in codec error
formats (cheap guard).


> **Resolved** in `fix(codec): describe type mismatches in payload terms`. The XML message now says "element <x>"; TestTypeErrorMessagesDoNotNameGoTypes rejects the old word.

---

### G-08 — Certificates are loaded once (Low, CODE) [RESOLVED]

`http/transport.go:153` and `:161` call `tls.LoadX509KeyPair` once during
`prepare` and store the result in `tls.Config.Certificates` /
`http3.Server.TLSConfig`. There is no `GetCertificate` callback and no reload
signal, so renewing a short-lived certificate (ACME, cert-manager) needs a
process restart, which for a server with 10 s graceful shutdown means dropped
connections on every renewal (QUIC connections included).

**Fix direction.** Use `tls.Config.GetCertificate` backed by a cached
`LoadX509KeyPair` that re-reads when the files' mtime changes (or on SIGHUP).
Mirror what `jose.Service.Reload` does for keys.


> **Resolved** in `feat(http): reload TLS certificates and JOSE keys without a restart`. certReloader (http/certs.go) serves the pair through GetCertificate, re-reads on mtime change (30s) and on SIGHUP; a bad pair keeps the previous one. Tests: TestCertReloaderFollowsItsFiles, TestSIGHUPReloadsTheCertificate (HTTPS and HTTP/3 end to end).

---

### G-09 — TLS private key permissions are not checked (Low, CODE) [RESOLVED]

`config/server.go:242` `checkKeyPair` only loads the pair. JOSE refuses key
files that are group/other readable (`jose/keys.go` `ownerOnly`); the TLS key,
which is just as sensitive, gets no equivalent check or even a warning.
Inconsistent security posture between two key loaders in the same framework.


> **Resolved** in `fix(config): cap risky limits and warn about weak transports`. A TLS key readable by group/others is reported in Config.Warnings (not an error: orchestrators mount keys 0644). TestTLSKeyPermissionWarning.

---

### G-10 — HTTPS/HTTP3 servers ignore `ServerHttp.Srv` customisation (Low, CODE) [RESOLVED]

`AGENTS.md` section 7.1 invites apps to tune `Srv` "before Start"
(`MaxHeaderBytes`, `ErrorLog`, `ConnState`, `BaseContext`, `ConnContext`,
`TLSNextProto`, HTTP/2 settings). `newTCPServer` (`http/transport.go:239`)
creates a fresh `http.Server` and copies only `Addr`, `Handler` and the four
timeouts. The plain HTTP listener uses `s.Srv` itself, so the same app code
behaves differently per transport; with `disable_insecure_http: true` the
customisation is dropped entirely. The HTTP/3 server has no counterpart at all
(no `MaxHeaderBytes`, no error log).

**Fix direction.** Clone the relevant fields of `s.Srv` into the TCP servers
(`srv := *s.Srv; srv.Addr = ...`), or document the limitation next to the
`Srv` field and expose a hook (`ConfigureServer func(*http.Server)`).


> **Resolved** in `feat(http): reload TLS certificates and JOSE keys without a restart`. newTCPServer and the HTTP/3 server carry the Srv customisation. TestSrvCustomisationReachesEveryTransport (verified to fail without the change).

---

### G-11 — Public docs/spec/readiness (Low, CODE) [RESOLVED]

`http/routes.go`. `http.docs.auth: bearer` only changes how the spec
*describes* security; it does not protect `/docs`, `/docs/swagger.json` or
`/docs/swagger.yaml`. The spec lists every route, private ones included, with
their scopes (`x-required-scopes`) and auth profile names. `/ready` is public
and reports which dependencies are configured and healthy plus `online_since`.
The spec is rebuilt from reflection on **every** request (no cache), so on a
large API the unauthenticated spec endpoint is a CPU amplifier.

**Fix direction.** An option to put docs/readiness behind a middleware or a
shared secret; cache the rendered spec until a route is added.


> **Resolved** in `fix(http): cache the public spec and warn about docs in production`. The spec is cached per route count with an ETag (TestSpecIsCachedAndConditional) and startup warns when docs are enabled with env prd (TestDocsInProductionWarning). Protecting the endpoints is left to the gateway/disabling docs, documented in AGENTS 7.8 (a browser cannot send a bearer token on a navigation).

---

### G-12 — Raw routes have no body limit (Low, CODE) [RESOLVED]

Typed routes are bounded by the codec's `max_payload_bytes`. A handler
registered with `server.Router().Post(...)` that reads `r.Body` directly is
bounded only by `read_timeout` (30 s default), which at line speed allows
gigabytes. There is no default `http.MaxBytesReader` middleware. The docs
steer people to `ParseJSON`, which is bounded, but nothing enforces it.

**Fix direction.** Ship a `MaxBody(limit)` middleware and apply it by default
at `codec.max_payload_bytes` (opt out per route).


> **Resolved** in `fix(http): bound request context, body and statement time`. http.server.max_body_bytes (default codec.max_payload_bytes) wraps every request body; ParseStatus maps *http.MaxBytesError to 413. Tests: TestRawRouteBodyIsBounded, TestTypedRouteAnswers413AtEitherLimit.

---

### G-13 — Silent configuration fallbacks (Low, CODE) [RESOLVED]

- `config/config.go` `expandEnvNodes`: an undefined `${VAR}` becomes `""`
  (documented). For `database.password`, `tls.cert_file`, `cache.redis.url`
  this produces an empty credential or path without a warning. Only the JOSE
  secret path rejects empties.
- `loadYAMLFile`: `os.IsNotExist` -> `return nil`. An explicit
  `-config /wrong/path.yaml` is silently ignored and the user sees the
  unrelated error `app.name is required`.

**Fix direction.** Log (or fail with an opt-out) when a `${VAR}` is unset;
treat a missing file as an error when the path was given explicitly, and only
ignore the default `./config.yaml`.


> **Resolved** in `fix(config): fail on configuration typos instead of defaulting`. Strict YAML (KnownFields) with top-level x- extension keys, strict env-file keys, env validated and normalized, explicit -config must exist, unreadable overlay is an error, unset ${VAR} reported in Config.Warnings and logged by deps. Tests: config/strict_test.go, deps TestConfigWarningsAreLogged.

---

### G-14 — `Bearerx token` is 400, not 401 (Info, CODE) [RESOLVED]

`http/internal/authn/authn.go:16` `BearerToken`: the scheme test uses the first
six characters, so `Authorization: Bearerx abc` or `BearerFoo abc` passes the
prefix check and then fails `rest[0] != ' '` -> 400 `invalid_request`. A
different scheme should be 401 with a challenge (RFC 6750 / 9110). Harmless,
but it makes clients that probe schemes see an inconsistent contract.


> **Resolved** in `fix: bearer scheme, log source, Round and concurrent shutdown`. authn.BearerToken takes the scheme up to the first SP/HTAB, so Bearerx is another scheme (401). Test cases in http/auth_test.go.

---

## 2. Scanner output

`gosec` (7 issues, all reviewed):

| Rule | File | Verdict |
|---|---|---|
| G404 x3 | `telemetry/traceid.go:34,35,48` | Trace IDs need uniqueness, not secrecy; `//nolint` justified |
| G404 | `cache/redis.go:134` | Retry jitter; justified |
| G203 | `http/internal/openapi/page.go:65` | `template.CSS(accent)`; safe only while `config` keeps validating the hex value |
| G304 | `jose/keys.go:108`, `config/config.go:255` | Paths come from trusted configuration; not exploitable |

`govulncheck ./...`: 0 vulnerabilities reachable. One module-level advisory,
GO-2026-5932 (`golang.org/x/crypto/openpgp` unmaintained, no fix available);
nForge does not import `openpgp`.

`golangci-lint run ./codec/...`: clean. `go vet` clean. `go test -race`
clean for the packages that were run.

---

## 3. Checked and found sound

- **Redirect-only HTTP listener**: `//evil.example/x`, backslash, CRLF in the
  path, absolute-URL request target and `?next=//evil` all produce a 308 to
  the configured authority; no open redirect, no header injection.
- **Authentication**: authentication runs before decoding; responses are
  static; no token or claim in logs; `WWW-Authenticate` omitted on 500/503;
  duplicate `Authorization` rejected.
- **Tenant**: UUID v4 validation, fail-closed resolvers, tenant never logged,
  header-source caveat documented.
- **`ncrypto`**: constant-time comparisons, Argon2id parameters validated
  before allocation, fresh random nonces, bcrypt input never truncated.
- **JOSE key loading**: opened once and `Stat`ed on the descriptor (no TOCTOU),
  size-bounded read, owner-only permission check, secrets cleared.
- **CBOR decoder**: library defaults (nesting 32, array/map 131 072) apply.
- **JSON engine**: nesting capped at 10 000, rejects deeper payloads.
- **RLS executor**: `set_config($1,$2,true)` is parameterised and
  transaction-local.

---

## 4. Coverage

**Read line by line:** `http/transport.go`, `config/server.go`, `http/auth.go`,
`http/internal/authn`, `http/tenant.go`, `http/typed_route.go` (request flow),
`http/routes.go`, `http/server.go`, `http/internal/accesslog`,
`http/internal/tracing`, `http/internal/openapi/page.go`, `crypto/` (hashing,
AES), `jose/jwt.go` (claim validation), `jose/keys.go` (file loading),
`cache/keys.go`, `cache/load.go`, `cache/memory.go`, `logs/logs.go`,
`telemetry/tracing.go`, `deps/deps.go` (startup), `config/config.go` (loader).

**Exercised with tools:**

| Tool | Target | Result |
|---|---|---|
| `gosec` | whole module | 7 issues, all justified (section 2) |
| `govulncheck` | whole module | nothing reachable |
| fuzz, 45 s | `http/internal/query` `Decode` (struct with ints, uints, floats, bools, durations, times, pointers, slices, embedded, required, strict on/off) | 17.9 M executions, no panic |
| fuzz, 40 s each | `codec` XML, CBOR and JSON decode into a struct and into `map[string]any`, strict mode on, 64 KiB cap | 3.5-6.8 M executions each, no panic, no hang (inputs are small, so G-01 does not show) |

**Third pass (full read):** `http/response.go`, `http/security.go`,
`http/internal/capture`, `http/internal/jwks`, `http/internal/envelope`,
`http/internal/openapi/schema.go` and the `openapi.go` model,
`config/config.go`, `config/env.go`, `config/bytesize.go`,
`config/security.go`, `codec/registry.go`, `codec/fields.go`, `codec/cbor.go`,
`codec/xml.go` (encoder and decode entry), `jose/compact.go`, `jose/jws.go`,
`jose/jwe.go`, `jose/jwt.go`, `jose/jwk.go` (parser), `jose/service.go`
(snapshot), `jose/internal/strictjson` (start), `cache/redis.go`,
`cache/config.go` (endpoint, redaction), `cache/cache.go`, `cache/options.go`,
`cache/expiration.go`, `database/config.go`, `database/pool.go`,
`database/tenancy.go` (predicate and `CheckTenancy`), `crypto/random.go`,
`helper/safecalc`, `logs/`, `telemetry/`, `deps/deps.go`, `version.go`.

**Skimmed only (tests and fuzzers exist; not re-read line by line):**
`codec/typeinfo.go` tag resolution, `codec/presence.go` (beyond the path
handling), `http/internal/query` decoding logic (fuzzed instead),
`jose/config.go` validation tables, `jose/keys.go` beyond file loading,
`cache/memory.go` heap internals, `database/db.go` and `transaction.go`.

---

## 5. Findings from the second pass

### G-15 — `GetOrLoad` fails forever on an undecodable cached entry (Medium, PoC) [RESOLVED]

**Where.** `cache/load.go`, the hit path of `GetOrLoad`:

```go
data, err := c.backend.get(ctx, full)
if err == nil {
    return c.fail(op, c.decodeInto(data, target))   // a decode error ends the call
}
```

`decodeInto` returns `ErrDecode` when the stored bytes no longer satisfy the
target: a new `codec:"required"` field, a stricter `Validate()`, a renamed
field under strict mode, a changed `cache.codec`. The loader is never called,
nothing is deleted, and every caller keeps getting the error until the TTL
expires.

**Reproduction** (memory driver, temporary test in `package cache`):

```go
type v1 struct{ Name string `json:"name"` }
type v2 struct {
    Name  string `json:"name"`
    Email string `json:"email" codec:"required"`
}
c.Set(ctx, "user:1", v1{Name: "ana"}, time.Hour)       // written by the old deploy
for i := 0; i < 3; i++ {
    var v v2
    err := c.GetOrLoad(ctx, "user:1", &v, time.Hour, loader) // new deploy
}
```

Output, three times in a row, with `loaderCalls=0`:

```
cache: get_or_load (memory): decode failed: codec: json: missing required field "email"
```

**Impact.** Any rolling deploy that tightens a cached type turns the cache-aside
helper into a hard outage for the affected keys for up to the TTL (minutes to
hours). `AGENTS.md` gotcha 23 documents the `cache.codec` variant; schema
evolution is the same failure and far more common. During a rolling deploy the
old and new versions also write each other's incompatible values, so it can
flap.

**Fix direction.** In `GetOrLoad` treat `ErrDecode` on the read as a miss:
log a WARN (without key/value), fall through to the flight, and let the store
overwrite the bad entry. Keep `Get` strict. Add a regression test for the
required-field case.


> **Resolved** in `fix(cache): reload undecodable GetOrLoad entries`. GetOrLoad treats ErrDecode on a hit (and on the flight's recheck) as a miss: WARN, load, overwrite. Get stays strict. Tests: TestGetOrLoadReloadsAnUndecodableEntry, TestGetOrLoadUndecodableEntryWithConcurrentCallers, TestGetOrLoadReadErrorSkipsLoader (corrupt data).

---

### G-16 — `env` is never validated (Medium, CODE) [RESOLVED]

**Where.** `config/config.go` `validateRequiredConfig` does not mention `Env`.
`overlayFileFor` (`config/config.go:~327`) maps only `prd`/`stg`/`dev`
(case-insensitive); `logs.SetLogLevelByEnv` (`logs/multi_handler.go:75`) maps
only the exact lowercase strings and returns **DEBUG for anything else**.

**Consequences.** Any value other than the four documented ones is accepted:

| `env:` value | overlay | terminal log level | note |
|---|---|---|---|
| `prd` | `config.prd.yaml` | ERROR | intended |
| `PRD` | `config.prd.yaml` | **DEBUG** | documented gotcha 3 |
| `prod`, `production`, `prd ` | **none** | **DEBUG** | typo = production config silently not loaded |
| `""` (cleared by an env file) | none | DEBUG | |

So a one-word typo in production runs with the debug level (auth failure
reasons, tenant rejection reasons, source file locations) **and** without the
production overlay, which is where hardening such as `docs.enabled: false`
or `cors.origins` normally lives. The cache namespace also embeds the raw
string, so `Prd` and `prd` would not share keys.

**Fix direction.** Validate `env` against `local|dev|stg|prd` after trimming
and lower-casing it (store the normalised value), and fail startup otherwise.
Make the log-level function default to a *quieter* level, not DEBUG.


> **Resolved** in `fix(config): fail on configuration typos instead of defaulting`. Strict YAML (KnownFields) with top-level x- extension keys, strict env-file keys, env validated and normalized, explicit -config must exist, unreadable overlay is an error, unset ${VAR} reported in Config.Warnings and logged by deps. Tests: config/strict_test.go, deps TestConfigWarningsAreLogged.

---

### G-17 — Tracing: no sampler setting, client-controlled identifiers (Low, CODE) [RESOLVED]

**Where.** `telemetry/tracing.go` builds the provider with
`sdktrace.NewTracerProvider(WithResource, WithBatcher, WithIDGenerator)` and no
sampler: the SDK default is `ParentBased(AlwaysSample)`.
`http/internal/tracing/tracing.go` accepts `X-Request-Id` as the trace id and
the W3C `traceparent` and `baggage` headers from any caller.

**What happens.**

- 100 % of requests become spans; there is no `sample_ratio` in
  `tracing.otel`. A client that sends `traceparent` with the sampled flag
  forces recording; one that sends it unsampled suppresses the span for its
  own requests.
- A client can pick the `request_id`/`trace_id` of a request (32 hex or UUID),
  including an id that already exists, so it can append its spans and log
  lines to someone else's trace in Tempo/Loki (log/trace forging by the
  correlation key).
- `baggage` from an untrusted caller is propagated into any downstream call
  made with the request context.

**Impact.** Cost and integrity of observability data, not application data.

**Fix direction.** Add `tracing.otel.sample_ratio` (default 1.0) and a setting
to ignore inbound `traceparent`/`X-Request-Id`/`baggage` on edge services;
document that the correlation id is client-influenced.


> **Resolved** in `feat(telemetry): sample traces and optionally distrust the caller`. tracing.otel.sample_ratio and trust_incoming; nhttp.Tracing(WithoutIncomingContext()). The correlation id is still client-influenced when trust_incoming is on (documented in AGENTS 6.1). Tests: telemetry TestSampleRatio, tracing TestUntrustedIncomingContextIsIgnored, config TestTracingSampleRatioAndTrust, http TestTracingTrustIncomingFollowsTheConfig.

---

### G-18 — `AddSource: true` everywhere (Low, CODE) [RESOLVED]

`logs/logs.go` sets `AddSource: true` on the terminal handler and
`otelslog.WithSource(true)` on the exporter. Every record carries the
source file and line of the call, in every environment. That is useful in
`local`, noisy in `prd`, and publishes build-path information to the log
backend. Make it conditional on `env`/level.


> **Resolved** in `fix: bearer scheme, log source, Round and concurrent shutdown`. AddSource / OTel WithSource only at the DEBUG level (env local). logs/logs_test.go.

---

### G-19 — JOSE `Reload` has no trigger; replaced keys are not wiped (Info, CODE) [RESOLVED]

`jose/service.go` `Reload` swaps the snapshot atomically, but nothing in the
framework calls it: no SIGHUP handler, no admin hook, no file watcher (by
design, documented). An application that wants key rotation without a restart
must wire it itself; the `Deps` does not expose a helper. After a successful
swap the *old* snapshot's key material is simply dropped (garbage collected),
not `wipe()`d, unlike the identical-material branch which does wipe. In-flight
operations may still be using it, so wiping is non-trivial; at least document
it.


> **Resolved** in `feat(http): reload TLS certificates and JOSE keys without a restart`. SIGHUP now triggers Deps.ReloadJOSE together with the certificates; the unwiped replaced snapshot is documented (in-flight operations may still use it) rather than changed. TestReloadJOSEDisabled, TestSetupJOSEEnabledRegistersShutdown.

---

### G-20 — Shared loads run as the first caller (Info, CODE) [RESOLVED]

`cache/load.go` `join`: the loader gets
`context.WithoutCancel(first caller's ctx)`, so it runs with the first
caller's context *values* (principal, tenant, request id). Every concurrent
waiter of the same full key receives the leader's result **and the leader's
error value unchanged**. The tenant is part of the key so tenants do not mix,
and the doc already says a tenant namespace "isolates data, it does not
authorize"; but a loader that consults the principal in `ctx` will have its
authorization applied to whichever caller happened to arrive first. Worth a
sentence in the `GetOrLoad` doc comment and in `AGENTS.md` section 11.3.


> **Resolved** in `fix(http): cache the public spec and warn about docs in production`. Documented in the GetOrLoad doc comment and AGENTS 11.3: the shared load runs with the first caller's context values and every waiter gets its result or error.

---

## 6. Third pass (full read of the remaining modules)

Findings from reading the remaining modules in full. Same evidence labels.

### G-21 — Env-file values go through `$VAR` expansion (Medium, PoC) [RESOLVED]

**Where.** `config/env.go:17` `applyEnvFile` uses `godotenv.Read`, which
expands `$NAME` / `${NAME}` in unquoted and double-quoted values (the name
must be upper-case). *Correction after the fix:* the expansion looks names up
among the keys **defined earlier in the same file**, not in the process
environment; an undefined name becomes empty. (The first version of this item
said "process environment"; that was wrong, the PoC output below is right.)

**Reproduction** (temporary test, `LoadFlags(yaml, envfile)`), env file:

```
DATABASE_PASSWORD=pa$sword$HOME
DATABASE_HOST="h$USER"
```

Neither `USER` nor `HOME` is defined in the file. Result:

```
password="pa$sword"   host="h"
```

`$USER` was replaced by an empty string, silently shortening the host.
Lower-case names such as `$sword` are left alone, so the bug is inconsistent
and hard to notice.

**Impact.** A password, token or DSN that contains `$` followed by an
upper-case identifier is silently truncated, which shows up as a mysterious
authentication failure at startup (and, per G-03, with no message).
A reference to a key defined earlier in the file (`B=$A`) was also expanded,
which is surprising in a file that is documented as plain `KEY=value`.

**Fix direction.** Read the file with a parser that does not expand
(`godotenv.Unmarshal` on the raw text after disabling expansion, or
single-quote semantics for every value), document that `$` is literal, and
add a test with `$` in a password.


> **Resolved** in `fix(config): take env-file values literally`. readEnvFile hides every $ from godotenv and restores it, so nothing is expanded. Test: TestEnvFileValuesAreLiteral. (The first description said the expansion used the process environment; it uses earlier keys of the same file, corrected in the item.)

---

### G-22 — Env-file keys that match nothing are ignored (Medium, PoC) [RESOLVED]

Same root cause as G-02 but for the env file: `applyEnvVars` only *looks up*
the keys of known fields (`vars[envKey]`), never iterates the file. Confirmed
with `HTTP_CORS_ORIGNS=https://a.example`: load succeeds and
`cors.origins` stays `[]` (= any origin when CORS is enabled). Misspelt
`HTTP_SERVER_TLS_MIN_VERSON`, lower-case `http_server_port`, a stale key left
after a field was renamed: all silently ignored. Fix together with G-02:
collect the file's keys, subtract the consumed ones and fail on leftovers
(allow an opt-out prefix for variables meant for other tools).


> **Resolved** in `fix(config): fail on configuration typos instead of defaulting`. Strict YAML (KnownFields) with top-level x- extension keys, strict env-file keys, env validated and normalized, explicit -config must exist, unreadable overlay is an error, unset ${VAR} reported in Config.Warnings and logged by deps. Tests: config/strict_test.go, deps TestConfigWarningsAreLogged.

---

### G-23 — JSON requests without `Content-Type` are accepted: CSRF-style cross-site POSTs (Low-Med, CODE) [RESOLVED]

**Where.** `http/response.go:130` `checkContentType`: an empty `Content-Type`
returns `nil` (accepted) for every typed body route and for
`ParseJSON/ParseXML/ParseCBOR`.

**Why it matters.** A cross-origin request is exempt from the CORS preflight
when its `Content-Type` is absent or one of `text/plain`,
`application/x-www-form-urlencoded`, `multipart/form-data`. A page on any
origin can send `fetch(url, {method: "POST", body: new Blob([json])})` with an
empty blob type, which carries **no** `Content-Type`, and nForge decodes it as
JSON. The attacker cannot read the response, but the side effect (create,
update, delete) happens. With bearer tokens in the `Authorization` header
this is not exploitable (the browser does not add it), but it is for:

- routes with **ambient authority**: cookies, mTLS, client IP allow-lists,
  "internal network only" APIs reachable from a user's browser;
- public routes whose action is the vulnerability (anonymous writes).

`text/plain` is rejected correctly with 415; the gap is only the empty case.

**Fix direction.** Require a matching `Content-Type` whenever the request has a
body (`r.ContentLength != 0`); keep accepting a missing header for GET/no-body.
This is a behaviour change, so make it a `codec`/`http` setting if existing
clients rely on it.


> **Resolved** in `fix(http): bound request context, body and statement time`. A body without Content-Type is refused with 415 (TestBodyWithoutContentTypeIsRefused).

---

### G-24 — HSTS `includeSubDomains` is on by default (Low, CODE) [RESOLVED]

`config/security.go` / `http/security.go`: `hsts.include_subdomains` defaults
to true with a one-year `max_age`. On an API served from an apex or shared
parent domain, one response permanently forces HTTPS on *every* subdomain of
that domain in each browser that saw it for a year, including internal tools
that only speak HTTP. Safe for a dedicated `api.` host, a footgun otherwise.
Consider defaulting to `false` and documenting the trade-off.


> **Resolved** in `fix(config): cap risky limits and warn about weak transports`. hsts.include_subdomains defaults to false (TestHSTSIncludeSubdomainsIsOptIn).

---

### G-25 — Overlay load errors are skipped silently (Low, CODE) [RESOLVED]

`config/config.go:~54`: `if _, err := os.Stat(overlay); err == nil { load }`.
Any `Stat` error other than "not exist" (permission denied on the file or its
directory, a stale NFS handle) is treated as "no overlay", so the production
overlay is silently not applied. Same fail-open family as G-02/G-13/G-16.
Treat `!errors.Is(err, fs.ErrNotExist)` as a startup error.


> **Resolved** in `fix(config): fail on configuration typos instead of defaulting`. Strict YAML (KnownFields) with top-level x- extension keys, strict env-file keys, env validated and normalized, explicit -config must exist, unreadable overlay is an error, unset ${VAR} reported in Config.Warnings and logged by deps. Tests: config/strict_test.go, deps TestConfigWarningsAreLogged.

---

### G-26 — `codec.max_payload_bytes` has no upper bound (Low, CODE) [RESOLVED]

`config/config.go` `feedDefaults` only defaults a zero value; nothing caps it.
`max_payload_bytes: 100gb` is accepted and, with G-01/G-05 amplification and
the decoders that buffer the whole payload (`io.ReadAll` for strict/required,
XML), turns the limit meant to protect the process into the attack surface.
Validate a sane ceiling (for example 64 MiB) and log the effective value.


> **Resolved** in `fix(config): cap risky limits and warn about weak transports`. codec.max_payload_bytes is capped at 256 MiB (TestMaxPayloadHasACeiling).

---

### G-27 — Cyclic values crash the process in CBOR/XML encode and in `CheckRequired` (Medium, PoC) [RESOLVED]

**Where.** `codec/fields.go:84` `checkRequired` (recursive, follows pointers,
slices and maps with no cycle detection); `codec/cbor.go` `Encode`
(fxamacker/cbor) and `codec/xml.go` `xmlEncoder.value/structElement`
(custom recursion). Only the JSON encoder (`encoding/json`) detects cycles.

**Reproduction** (each in its own process because the failure is fatal):

```go
type cyc struct {
    XMLName xml.Name `json:"-" xml:"n"`
    Name    string   `json:"name" xml:"name"`
    Next    *cyc     `json:"next" xml:"next"`
}
a := &cyc{Name: "a"}; a.Next = a
registry.EncodeAs(format, a)
```

| format / call | result |
|---|---|
| `json` | error `json: unsupported value: encountered a cycle via *codec.cyc` (fine) |
| `cbor` | **`fatal error: stack overflow`** |
| `xml` | **`fatal error: stack overflow`** |
| `codec.CheckRequired(a)` | **`fatal error: out of memory`** (stack growth with a 4 GB address-space limit; unlimited it is the 1 GB stack overflow) |

A Go stack overflow is not a panic: `middleware.Recoverer` cannot catch it,
the whole process dies and every in-flight request is lost.

**Reach.** Every typed route runs `codec.CheckRequired` on the handler's
response *regardless of format and regardless of `codec:"required"` tags*
(`http/internal/capture/capture.go` `conform`). A handler that returns an
object graph with a back-reference (parent/child, ORM-style associations, a
cache of pointers) crashes the server instead of answering `500 failed to
build the response`. The JSON route would have returned a clean 500 if
`CheckRequired` did not run first. Also reachable on any `SendCBOR/SendXML`
or route with `Codec: "cbor"|"xml"`.

**Fix direction.** Add a visited-set (pointer identity) or a depth limit to
`checkRequired`, the XML encoder and, for CBOR, a pre-pass or a recover-safe
depth guard (the library has no cycle detection). Return `EncodeError`.
Regression tests with a self-referential value for each format.


> **Resolved** in `fix(codec): reject reference cycles instead of crashing`. checkAcyclic (codec/cycle.go) runs before the CBOR and XML encoders and CheckRequired; typed routes answer 500. Tests: TestEncodeRejectsCycles, TestCheckRequiredHandlesCycles, TestTypedRouteWithCyclicResponseIsA500, plus no-false-positive tests for shared pointers and json:"-" back-references.

---

### G-28 — `CheckRequired` walks every response even without `required` tags (Low, PoC) [RESOLVED]

`http/internal/capture/capture.go` `conform` calls `codec.CheckRequired` for
every successful typed response. On a 1 MB response (1089 orders x 3 lines)
with no `codec:"required"` field anywhere the walk takes **1.9 ms per call**
(measured, 20 calls), about the same as encoding that response (2.0 ms).
The decode side already skips the presence pass when `hasRequired(t)` is
false; the response side should do the same (`if !hasRequired(t) { skip }`).
Roughly halves the per-response CPU of large untagged responses.


> **Resolved** in `fix(codec): reject reference cycles instead of crashing`. CheckRequired returns immediately for types with no required field and no reachable interface (TestCheckRequiredSkipsTypesWithoutRequiredFields: 0 allocations on a 1 MB response).

---

### G-29 — `Validate()` only runs on the root value (Low-Med, CODE) [RESOLVED]

`codec/registry.go` `runValidation` checks `v.(Validator)` on the decode
target only. A `Validate() error` method on a **nested** struct, on slice
elements or on map values is never called, silently. Developers who put
validation on `Address` and decode an `Order` get no validation of the
address. `AGENTS.md` section 8.3 says "type's `Validate() error`" without
saying "root type only". Either walk the value (like `CheckRequired`) or
document it prominently; the `Config.Validate` hook (go-playground/validator
style) does recurse, so the behaviours are inconsistent.


> **Resolved** in `fix(codec): run Validate on nested values`. validateTree (codec/validate.go) visits nested structs, pointers, slice elements and map values, children first, with the location in the message. Tests: codec/validate_test.go.

---

### G-30 — Secret redaction is incomplete: `config.Config` prints database passwords and DSNs (Medium-High, PoC) [RESOLVED]

**What `AGENTS.md` promises.** Section 9: "`LogValue` redacts the password/DSN
for slog"; section 16: "redacted DB config"; JOSE and cache redact "in every
`fmt` verb".

**What the code does.** Which types carry a redactor:

| type | `LogValue` | `Format` / `String` / `GoString` | JSON |
|---|---|---|---|
| `jose.Config`, `KeyConfig`, `KeySourceConfig` | yes | `String` + `GoString` | no |
| `cache.Config` | yes | `RedisConfig.Format` only | no |
| `database.Config` (`database/config.go:106`) | yes | **none** | no |
| `config.Config` (top level) | **none** | **none** | **none** |
| `logging.output.otel.endpoint` (userinfo) | - | - | - |

`fmt` and `slog` reach a nested value's redactor only when they call it on
that value; `encoding/json` never does.

**Reproduction.** Temporary test building a `config.Config` with
`Database.Password`, `Database.DSN` (password in the URL),
`Cache.Redis.Password`, `Cache.Redis.URL`, `Logging.Output.Otel.Endpoint`
(`https://user:secret@collector:4317`) and a `JOSE.Keys[0].Secret`, then
printing it. Which secrets appeared in the output:

| printed with | leaked |
|---|---|
| `fmt "%v"`, `"%+v"`, `"%#v"`, `"%v"` of `&cfg` | **database password, database DSN password, OTel endpoint credentials** |
| `slog` text handler (uses `fmt`) | database password, DSN password, OTel credentials |
| `slog` **JSON** handler | **everything**: database password and DSN, Redis password and URL, OTel credentials, **JOSE key secret** |
| `fmt "%+v"` of `cfg.Database` alone | password and DSN leak |
| `fmt "%+v"` of `cfg.Cache` / `cfg.JOSE` alone | redacted (good) |

**Why it matters.** `Deps.Config` is an exported `config.Config` value;
`slog.Info("starting", "config", dps.Config)` or
`log.Printf("%+v", dps.Config)` is the most natural debugging line a developer
writes, and production log pipelines (OTel exporter, JSON stdout) are exactly
where the JSON handler is used. The JOSE HS256 secret ends up in the log
backend.

**Fix direction.**
1. `config.Config` gets `LogValue`, `String`, `GoString` and `MarshalJSON`
   that redact (or exclude) every secret-bearing section.
2. `database.Config` gets `Format`/`String`/`GoString` like `RedisConfig`.
3. Redact userinfo in `logging.output.otel.endpoint` and
   `tracing.otel.endpoint`.
4. Add a test that prints a fully populated config through
   `%v/%+v/%#v`, slog text, slog JSON and `json.Marshal` and fails if any
   planted secret appears.


> **Resolved** in `fix(config): redact secrets in every print path`. config.Config now has String, GoString, LogValue, MarshalJSON and MarshalYAML that print a redacted copy (database password/DSN, Redis password/URL/CA path, JOSE key secrets and key file paths, TLS key paths, OTel endpoint userinfo). database.Config has Format for every verb. Regression tests print a fully populated config through fmt, slog text/JSON, json and yaml.

---

### G-31 — No ceiling on JWT `ttl` / `max_ttl` (Low, CODE) [RESOLVED]

`jose/config.go:~500`: `ttl` and `max_ttl` must be positive whole seconds and
`ttl <= max_ttl`; there is no upper bound. `max_ttl: 87600h` (ten years) is
valid and `ValidateJWT` then accepts a ten-year token. Because the design is
stateless (no revocation list, `jti` is not tracked), the lifetime is the only
revocation window. Add a hard ceiling (for example 24 h for access profiles,
configurable) or at least a startup warning above a threshold.


> **Resolved** in `fix(config): cap risky limits and warn about weak transports`. jose.limits.max_token_lifetime (default 720h, max 8760h) caps jwt ttl and max_ttl (TestTokenLifetimeHasACeiling).

---

### G-32 — Redis credentials are allowed over plain TCP to remote hosts (Low, CODE) [RESOLVED]

`cache/config.go`, `newRedis`: with `tls.enabled: false` and a `redis://` URL
(or host/port) nothing stops `password` or `username` from being sent in
cleartext (`AUTH`) to any address. There is no warning, and no check that the
host is loopback or a Unix socket. Reject (or warn loudly on) credentials
without TLS unless the host is loopback, like `RedirectAuthority` does for the
redirect host. The same applies to PostgreSQL `ssl.mode: ""` (pgx default
`prefer`, which silently downgrades): the database section never warns when
the effective mode can send the password unencrypted.


> **Resolved** in `fix(config): cap risky limits and warn about weak transports`. Redis credentials without TLS and PostgreSQL passwords with a clear-text-capable sslmode to non-local hosts are reported in Config.Warnings. TestPlaintextCredentialWarnings.

---

### G-33 — No atomic "increment with TTL" (Low, CODE) [RESOLVED]

`cache/cache.go` `Increment` creates a missing counter as **persistent**;
giving it an expiry is a second call (`Expire`). The two are not atomic: a
crash, a context timeout or `ErrOutcomeUnknown` between them leaves a
counter with no TTL that never resets. This is the textbook rate-limiter bug
and the API is exactly what people reach for. Offer
`IncrementWithTTL(ctx, key, delta, ttl)` implemented atomically (Redis: a
small Lua script or `SET key 0 NX PX` + `INCRBY` in `MULTI`; memory: under the
lock) that sets the TTL only when it creates the key.


> **Resolved** in `feat(cache): add IncrementWithTTL`. Cache.IncrementWithTTL creates the counter and its TTL atomically (memory: under the lock; Redis: Lua script, EVALSHA with EVAL only after NOSCRIPT, never replayed). Tests: TestIncrementWithTTL*, the RESP-fake tests and TestIntegrationIncrementWithTTL, which was run against a real Redis 8.

---

### G-34 — `database.Config.LogValue` does not redact secrets in DSN query parameters (Low-Med, PoC) [RESOLVED]

`database/config.go` `redactDSN` relies on `url.URL.Redacted()`, which masks
only the password in the userinfo part. pgx/libpq accept the password and
the client-key password as query parameters. Measured:

| DSN | `redactDSN` output |
|---|---|
| `postgres://u:pw1@h/db` | `postgres://u:xxxxx@h/db` (ok) |
| `postgres://u@h/db?password=pw2&sslmode=disable` | **unchanged, `pw2` visible** |
| `postgres://u@h/db?sslpassword=pw3&sslkey=/etc/key.pem` | **unchanged, `pw3` and key path visible** |
| `host=h user=u password=pw4 dbname=db` | `[REDACTED]` (ok) |

The DSN is logged by `Config.LogValue` whenever the database section is
logged. Parse the query, mask `password`, `sslpassword` (and `passfile`,
`sslkey`, `sslcert`, `sslrootcert` as paths), or print only host/port/name.


> **Resolved** in `fix(config): redact secrets in every print path`. redactDSN masks password and sslpassword query parameters (tests in database/redact_test.go).

---

### G-35 — No handler/request timeout and no statement timeout by default (Low-Med, CODE) [RESOLVED]

`http/server.go` mounts Tracing, Logger, Recoverer, Security, CORS; no
`middleware.Timeout`. `http.server.write_timeout` closes the connection after
30 s but does **not** cancel the request context of a handler that is still
running; and `database.NewPoolConfig` sets no `statement_timeout`. A slow or
adversarial request (a typed route doing an expensive query, a `GetOrLoad`
loader that hangs on a dependency) keeps its goroutine and its pooled
connection long after the client is gone. pgx's default pool is
`max(4, NumCPU)` connections, so a handful of such requests exhausts it and
turns into a full outage, including `/ready` (which then correctly reports
503, but the service is already down).

**Fix direction.** A configurable `http.server.handler_timeout` applied as a
context deadline (and a `statement_timeout` / `lock_timeout` setting under
`database.connection`), both with finite defaults; document that handlers must
pass `r.Context()` to queries.


> **Resolved** in `fix(http): bound request context, body and statement time`. http.server.handler_timeout (default 30s) sets the request context deadline; database.connection.statement_timeout (default 30s) sets PostgreSQL statement_timeout. Tests: TestRequestContextHasADeadline, TestRequestContextIsCanceledAtTheDeadline, TestNewPoolConfigSetsStatementTimeout.

---

### G-36 — `config.ByteSize` overflows to a negative number with no error (Medium, PoC) [RESOLVED]

**Where.** `config/bytesize.go` `ParseByteSize`:
`ByteSize(n * float64(mult))` after `strconv.ParseFloat`, with no range check.
Converting an out-of-range float to `int64` is implementation-defined; on
amd64 it yields `math.MinInt64`.

**Measured** (`UnmarshalText`):

| input | result |
|---|---|
| `9999999999gb` | **-9223372036854775808, err = nil** |
| `9223372036854775807` | **-9223372036854775808, err = nil** |
| `9223372036854775808` | **-9223372036854775808, err = nil** |
| `8589934592gb` (= 2^63) | **-9223372036854775808, err = nil** |
| `8gb` | 8589934592 (ok) |
| `9007199254740993` (2^53+1) | parsed through `float64`, loses the last byte |

The sibling parser in `cache/config.go` is correct: the same inputs return
`invalid size ...: too large`. So the two copies of "the same syntax" have
diverged.

**Impact.** `codec.max_payload_bytes` is the only field of this type. A huge
value becomes negative; `codec.New` rejects negatives, `deps` then calls
`os.Exit(1)` and (G-03) prints nothing. Any future field of this type that
does not re-validate would silently get a negative limit, which most
`> limit` comparisons treat as "everything is too large" or "no limit".

**Fix direction.** Parse integers with `strconv.ParseInt` and multiply with
overflow checks; use `ParseFloat` only for fractional input and reject results
outside `[0, MaxInt64]`. Better: make `config.ByteSize` an alias of (or share
code with) `cache.ByteSize`, since `config` is allowed to import `cache`.


> **Resolved** in `fix(config): reject byte sizes that overflow`. config.ParseByteSize delegates to cache.ParseByteSize, which is exact for whole numbers and errors on overflow. Tests: TestParseByteSizeOverflowAndPrecision (config) and TestParseByteSizeIsExactAndRejectsOverflow (cache).

---

### G-37 — `cache.limits.max_value_bytes` and `codec.max_payload_bytes` are independent (Low, CODE) [RESOLVED]

`cache.decodeInto` reads through `registry.DecodeAs`, so every cached object
is subject to **`codec.max_payload_bytes`** (default 5 MiB) on top of
`cache.limits.max_value_bytes` (1 MiB default, up to 64 MiB). Configure
`max_value_bytes: 20mb`: `Set` stores a 10 MiB object successfully and every
later `Get` fails with `ErrDecode (payload exceeds the configured limit)`;
`GetOrLoad` fails permanently for that key (see G-15) after storing it.
`validateCache` compares neither number. Validate
`cache.limits.max_value_bytes <= codec.max_payload_bytes` at load, or decode
cached bytes without the HTTP payload limit.


> **Resolved** in `fix(cache): reload undecodable GetOrLoad entries`. config.validateCache rejects cache.limits.max_value_bytes > codec.max_payload_bytes (config/cache_test.go).

---

### G-38 — `safecalc.Round` returns NaN/Inf for finite input (Low, PoC) [RESOLVED]

`helper/safecalc/safecalc.go` `Round`: `math.RoundToEven(val*shift) / shift`
with `shift = 10^places` and no guard.

| call | result |
|---|---|
| `Round(1.5, 400)` | **NaN** (`Pow(10,400)` is `+Inf`, `Inf/Inf`) |
| `Round(1e300, 10)` | **+Inf** (`val*shift` overflows) |
| `Round(1234, -2)` | 1200 (negative places silently allowed) |

It is documented as a display helper, but it is the one function in the
package that can turn a finite number into a non-finite one, which then
reaches `NewFromFloat` (rejected) or a JSON encoder (error). Clamp `places` to
a sane range (0..15) and return the input unchanged when `val*shift` is not
finite.

Also: `Money.Format` always prints exactly two decimal digits and `Value` is
"cents", so currencies with 0 (JPY) or 3 (KWD, BHD) minor digits cannot be
represented correctly; the `MoneyOpt` has no `Decimals` field.


> **Resolved** in `fix: bearer scheme, log source, Round and concurrent shutdown`. Round returns its input when nothing is left to round (TestRoundNeverProducesNonFiniteValues); the fixed two-decimal Format is documented as a limit of the package (0/3-digit currencies unsupported). Also corrected the documented Round example (2.675 does round to 2.68; 1.005 shows the imprecision).

---

### Additions to earlier findings

- **G-15 (poisoned cache entries)** also fires from settings that are global,
  not only from schema changes: `codec.strict_unknown_fields: true` makes an
  *older* instance reject entries written by a *newer* one that has an extra
  field (every rolling deploy), and a request-oriented `Config.Validate` hook
  (go-playground/validator rules) is re-applied to cached data on every read.
  Both become `ErrDecode` and, through `GetOrLoad`, a hard failure with no
  reload. Reads of cached data should use a decode path without strict mode
  and without the application's request validators.
- **G-17 (trace ids)**: because `idGenerator.NewIDs` reuses the client's
  `X-Request-Id` as the trace id of every root span, a client that sends the
  same id on many requests merges all of them into one trace in the backend.
- **G-11 (public spec)**: component names come from `reflect.Type.Name()` and,
  on collision, the Go package name (`componentName` in `schema.go`), so the
  public spec also discloses internal package and type names.

---

### G-39 — Defaults leave security headers and cache control off (Low, CODE) [RESOLVED]

`http.security.enabled` and `http.cors.enabled` default to false
(`config/security.go`), so a fresh app sends no `X-Content-Type-Options`, no
HSTS, no CSP, and no `Cache-Control` on any response, including private,
principal-specific JSON. `AGENTS.md` section 16 lists "security defaults are
on and must stay on" for timeouts, DTD and payload limits, but headers are an
opt-in. A conservative default for typed routes (`Cache-Control: no-store` on
`IsPrivate` routes, `nosniff` everywhere) costs nothing and removes a class of
shared-cache and MIME-sniffing surprises. Related: `stdhttp.Error` responses
from docs/JWKS are `text/plain` and rely on Go's own `nosniff`.


> **Resolved** in `fix(http): bound request context, body and statement time`. Envelope responses carry nosniff and private typed routes (401/403 included) carry Cache-Control: no-store (TestPrivateResponsesAreNoStoreAndNosniff). Security headers/CORS stay opt-in; the defaults now cover the two that cost nothing.

---

### G-40 — `Deps.Shutdown` is not guarded against concurrent calls (Info, CODE) [RESOLVED]

`deps/deps.go` `Shutdown` reads `d.Aux.ShutdownFuncs`, runs them and then sets
the slice to nil with no mutex or `sync.Once`. `AGENTS.md` says
`Deps.Shutdown` "runs exactly once" (true for `StartContext` alone), but
`Deps` is exported and `Aux` is a shared pointer: an application that also
calls `dps.Shutdown(ctx)` from its own signal handler while `StartContext`
is draining races on the slice and may run a closer twice (closing the pool
or the cache twice). Guard with `sync.Once` or a mutex; `ClearAux` has the
same unsynchronised write.


> **Resolved** in `fix: bearer scheme, log source, Round and concurrent shutdown`. Aux has a mutex; Shutdown and ClearAux take the closers out under it (TestShutdownIsSafeToCallConcurrently, run with -race).

---

## 7. Work log

All items were fixed in the order below, each in a commit that carries its
regression tests, the documentation change (`AGENTS.md`, `llms.txt`,
`CHANGELOG.md`) and the `[RESOLVED]` mark in this file. Every commit passed
`go build`, `go vet`, `gofmt`, the full test suite (the four examples
included) and `golangci-lint` before it was pushed.

| Commit | Items |
|---|---|
| `fix(config): redact secrets in every print path` | G-30, G-34 |
| `fix(codec): bound XML nesting and make error locations lazy` | G-01, G-05 |
| `fix(codec): reject reference cycles instead of crashing` | G-27, G-28 |
| `fix(config): fail on configuration typos instead of defaulting` | G-02, G-13, G-16, G-22, G-25 |
| `fix(deps): report fatal startup errors and add NewDepsE` | G-03 |
| `fix(config): reject byte sizes that overflow` | G-36 |
| `fix(config): take env-file values literally` | G-21 |
| `fix(cache): reload undecodable GetOrLoad entries` | G-15, G-37 |
| `fix(codec): run Validate on nested values` | G-29 |
| `fix(http): pin Swagger UI with SRI and drop the CDN from script-src` | G-04 |
| `fix(codec): describe type mismatches in payload terms` | G-06, G-07 |
| `fix(config): cap risky limits and warn about weak transports` | G-09, G-24, G-26, G-31, G-32 |
| `fix: bearer scheme, log source, Round and concurrent shutdown` | G-14, G-18, G-38, G-40 |
| `fix(http): bound request context, body and statement time` | G-12, G-23, G-35, G-39 |
| `feat(cache): add IncrementWithTTL` | G-33 |
| `feat(http): reload TLS certificates and JOSE keys without a restart` | G-08, G-10, G-19 |
| `feat(telemetry): sample traces and optionally distrust the caller` | G-17 |
| `fix(http): cache the public spec and warn about docs in production` | G-11, G-20 |

**Verified against real servers** (Docker): the integration tests of
`database`, `cache` and the four examples ran with PostgreSQL 18 and Redis 8
under `-race`; the Lua script of `IncrementWithTTL` and the server-side
`statement_timeout` are proven there, not only against fakes. `govulncheck`
reports nothing reachable; the fuzzers (`FuzzPresenceJSON` and the four JOSE
ones) found nothing.

**Decisions worth knowing about**

- Several fixes change behavior on purpose and are listed under "Upgrading" in
  `CHANGELOG.md`: unknown configuration keys and unknown env-file keys are
  errors, `env` must be `local|dev|stg|prd`, a request body needs a
  `Content-Type`, nested `Validate()` now runs, requests have a 30 s handler
  deadline and a body limit, PostgreSQL gets `statement_timeout` 30 s, HSTS
  `includeSubDomains` is opt-in, and SIGHUP is handled.
- Some items are mitigations rather than eliminations, and say so in their
  note: G-11 (the docs and `/ready` stay public; they are cached and a
  production warning is logged), G-09 and G-32 (warnings, not errors, because
  orchestrators mount keys 0644 and private networks carry plain traffic),
  G-19 (the replaced JOSE snapshot is documented as not wiped), G-38 (the
  two-decimal limit of `Money.Format` is documented as a limit of the
  package) and G-17 (`trust_incoming` defaults to true, so a deployment must
  turn it off on its public edge).
- Two statements in the original findings were wrong and were corrected where
  they appear: G-21 (godotenv expands from earlier keys of the same file, not
  from the process environment) and the documented `Round(2.675, 2)` example
  (it is 2.68; `1.005` shows the imprecision).
