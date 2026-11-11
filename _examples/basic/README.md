# Basic example

A minimal nForge HTTP app with terminal logging and an in-memory user CRUD API.
The more complete example, with database, cache, JOSE, messaging, TLS and
observability, is in [`../complete`](../complete).

Run from the repository root:

```bash
go run ./_examples/basic -config _examples/basic/config.yaml
```

The server listens on <http://localhost:8080>. API docs are at
<http://localhost:8080/docs> and health is at <http://localhost:8080/health>.

CRUD routes:

| Method | Path | Action |
|---|---|---|
| `GET` | `/users` | List users |
| `POST` | `/users` | Create a user |
| `GET` | `/users/{id}` | Get a user |
| `PUT` | `/users/{id}` | Replace a user |
| `DELETE` | `/users/{id}` | Delete a user |

Example:

```bash
curl -X POST http://localhost:8080/users \
  -H 'Content-Type: application/json' \
  -d '{"name":"Ana","email":"ana@example.com"}'
curl http://localhost:8080/users
```

Users live in memory and are cleared when the process stops.
