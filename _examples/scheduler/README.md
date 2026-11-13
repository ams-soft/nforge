# Scheduler example

A **scheduler-only** application: no HTTP server, no listener, no broker. A
`stock` domain module requires the database and the scheduler, and registers one
Cron job in its `Init`:

```go
sched.AddJob("0 7 * * *", countStock(db, logger)) // every day at 07:00 in scheduler.timezone
```

The job runs `SELECT COUNT(*) FROM stock` through the framework's database
(`db.Query` builds it, `db.Executor` runs it) and logs the total and the
duration with the framework logger. `config.yaml` sets
`scheduler.timezone: America/Sao_Paulo`, so 07:00 is Sao Paulo time. `app.Run`
keeps the process alive until SIGINT/SIGTERM; the scheduler does not need HTTP.

## Run

From the repository root:

```bash
docker compose -f _examples/scheduler/docker-compose.yml up -d   # PostgreSQL; loads schema.sql (stock table + 3 rows)
go run ./_examples/scheduler -config _examples/scheduler/config.yaml
```

The table comes from [`schema.sql`](schema.sql); the scheduler never creates
tables. Against your own PostgreSQL, apply it with
`psql "$DSN" -f _examples/scheduler/schema.sql` and edit `database:` in the config.

## See it run without waiting for 07:00

`-cron` replaces the expression for that run only (the example's default stays
`0 7 * * *`):

```bash
go run ./_examples/scheduler -config _examples/scheduler/config.yaml -cron "* * * * *"
```

At the start of the next minute the log shows
`[StockModule] Stock count total=3 duration=...`. Press Ctrl+C (or send SIGTERM)
to stop: the scheduler stops first, cancels a running job and waits for it
within the shutdown deadline (15 s by default), and only then does the database
pool close.

## What to expect

- No database or an invalid config: the application does not start, with a clear
  error (`unable to setup database: ...`, `module scheduler: ...`).
- `scheduler.mode: distributed` is not implemented and fails at startup; it never
  runs locally instead.
- Runs of a job overlap by default; `scheduler.max_concurrency: N` skips a firing
  while N runs of that job are active (nothing is queued or replayed). A firing
  missed while the process is down is not recovered. With several replicas every
  instance runs the job: make real jobs idempotent.

## Tests

```bash
go test -race ./_examples/scheduler/
NFORGE_TEST_DATABASE_URL='postgres://app:app@localhost:5432/app?sslmode=disable' \
  go test -race -tags integration ./_examples/scheduler/
```
