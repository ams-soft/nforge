# Email example

An application with **no HTTP server, database or broker**. A `welcome` module
requires `email.Capability` and does nothing in `Init`; once the application
has started, `main` renders [`welcome.mjml`](welcome.mjml) with
`email.RenderMJML` and sends it once through the SMTP client, then shuts down.

```go
html, err := email.RenderMJML(ctx, welcomeMJML)
err = client.Send(ctx, email.Message{To: to, Subject: "Welcome to nForge", Text: "...", HTML: html})
```

A successful run prints that the SMTP server **accepted** the message. That is
not a delivery confirmation.

## Run

```bash
cp _examples/email/.env.example _examples/email/.env     # placeholders: fill in your provider's values (.env is git-ignored)
set -a; . _examples/email/.env; set +a
go run ./_examples/email -config _examples/email/config.yaml -to you@example.com
```

`-to` defaults to `$EMAIL_TO`. Exit codes: `0` accepted, `1` failure, `2` missing
input.

## Secure transport

| `SMTP_SECURITY` | Port | What happens |
|---|---|---|
| `starttls` (default) | 587 | connects, requires the server to offer STARTTLS, upgrades and verifies the certificate **before** AUTH or any data |
| `tls` | 465 | implicit TLS from the first byte |

The certificate is always verified against `SMTP_HOST` (TLS 1.2 or newer). There
is no plaintext and no skip-verify mode. With `SMTP_USERNAME` and `SMTP_PASSWORD`
empty, no AUTH is attempted: use that only for a relay that trusts your network.
Never commit real credentials.

## Without a provider

- `SMTP_HOST` unset: the example prints how to configure it and exits `2` without
  dialing.
- No recipient: it prints that and exits `2`.
- An unreachable server or a refused TLS/AUTH: the error says so (`1`), without
  the server's text or your credentials.

## Trying it locally

Use any SMTP test server that offers STARTTLS or TLS with a certificate your
machine trusts and a name matching `SMTP_HOST`. The framework's own tests
(`email/smtp_test.go`) do exactly that with an in-process server, so no real
provider is needed in CI. A server without TLS is refused on purpose.
