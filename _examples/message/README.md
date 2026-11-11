# RabbitMQ message example

This worker-only example demonstrates RabbitMQ publishing, Classic queue
priority, Quorum queues, automatic and manual acknowledgements, dead-letter
queues and DLQ reprocessing. It uses the regular `app.Run` lifecycle without
HTTP.

Start this example's local RabbitMQ from the repository root:

```bash
cp _examples/message/.env.example _examples/message/.env
docker compose --env-file _examples/message/.env -f _examples/message/docker-compose.yml up -d
```

The default local account is `nforge` / `nforge`; these credentials are for
development only. The Management UI is at <http://localhost:15672>.

Open separate terminals, from the repository root:

```bash
go run ./_examples/message -config _examples/message/config.yaml -mode consumer
go run ./_examples/message -config _examples/message/config.yaml -mode producer
go run ./_examples/message -config _examples/message/config.yaml -mode reprocess
```

The producer sends to `notifications`, publishes a priority 8 message to
`payments`, and writes an audit message. Its attempted priority on the Quorum
queue is rejected. Stop the broker briefly to observe readiness go down and
the connection supervisor recover; Ctrl+C drains consumers before the
connection closes. On a non-default host, set `RABBITMQ_URL` in the process
environment or use `amqp://user:password@rabbitmq:5672/` from another
container.

Messages use at-least-once delivery. Consumers must be idempotent. Queue type
and arguments are immutable after creation; changing Classic to Quorum or
adding DLX arguments requires an operator-managed migration. Retry counts are
best effort and do not provide durable business retry guarantees. The AMQP
protocol cannot report unacknowledged counts, so `Processing` is unknown.
