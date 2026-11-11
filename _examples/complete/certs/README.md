# Example certificate (local development only)

`localhost.crt` / `localhost.key` are a **self-signed, intentionally public**
ECDSA P-256 pair generated for the HTTPS + HTTP/3 demo of the `complete` example.
The private key is committed on purpose and is therefore **not a secret**:

- never deploy it, trust it system-wide or reuse it for anything real;
- no other example, test of the framework or default configuration uses it
  (the framework has no default certificate);
- real certificates are user-provided secrets, kept out of the repository.

SANs: `localhost`, `127.0.0.1`, `::1`; usage: server authentication; valid
for 10 years from 2026-10-31. Regenerate with `./generate.sh`.
