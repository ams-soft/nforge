# SSE example

Server-Sent Events with nForge: authenticated, **user-targeted** streams, a
browser `EventSource` page, and two drivers. Events are best effort: a user who
is offline misses them, and nothing is replayed (`Last-Event-ID` is only a hint).

```bash
export JWT_SECRET=$(openssl rand -hex 32)
go run ./_examples/sse -config _examples/sse/config.yaml      # from the repository root
```

Open <http://127.0.0.1:8080/> in two tabs, sign in as `alice` in both and as
`bob` in a third (any name works: this is a demo login), then notify `alice`:
both of alice's tabs receive it and bob's does not. `GET /stats` shows the
manager's metrics; `GET /slow` shows a per-route timeout opt-in
(`nhttp.Timeout`), because `http.server.handler_timeout` is **off** by default
and streams are exempt from it.

## Authentication

`EventSource` cannot send an `Authorization` header, so `/login` sets the JWT in
an **HttpOnly, SameSite=Strict** cookie (add `Secure` behind HTTPS), a small
middleware lifts it into the bearer header, and nForge's own `Authenticate`
validates it; the stream's identity is the validated subject. A cross-origin
`Origin` is refused by the handler (`sse.allowed_origins` lists the exceptions).
Never put a token in the URL. An alternative is a short-lived, single-use
stream ticket issued by an authenticated call. Streams end after
`sse.max_lifetime`, so an expired or revoked token only stops a stream at its
next reconnect; the page handles the 401 that follows.

## Identity, expiry and reconnection

The stream's identity is `sse.UserKey(principal)` (issuer + tenant + subject): two
tenants that share a subject never share a stream, and `/notify` builds the target
user's key from the caller's principal. A stream ends at `min(token exp,
sse.max_lifetime)` after an `event: reauth`; the page listens for it, signs in
again and reconnects.

## RabbitMQ, two instances

```bash
cp _examples/sse/.env.example _examples/sse/.env
docker compose --env-file _examples/sse/.env -f _examples/sse/docker-compose.yml up -d
printf 'HTTP_SERVER_PORT=8081\n' > /tmp/sse-8081.env
go run ./_examples/sse -config _examples/sse/config.rabbitmq.yaml &                       # :8080
go run ./_examples/sse -config _examples/sse/config.rabbitmq.yaml -envfile /tmp/sse-8081.env &  # :8081
```

Sign in as the same user on `:8080` and `:8081` and notify from either: both
receive it. Stop the broker (`docker compose ... stop rabbitmq`): the open
streams and heartbeats stay up, `/notify` answers 503 and `/ready` 503, and
after `start` delivery resumes by itself.

## Behind a proxy

```nginx
location /notifications/stream {
    proxy_pass http://app;
    proxy_http_version 1.1;
    proxy_buffering off;      # the server also sends X-Accel-Buffering: no
    proxy_read_timeout 1h;    # must exceed sse.heartbeat
}
```
