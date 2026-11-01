#!/bin/sh
# Regenerates the local-development-only example certificate (see README.md).
set -eu
cd "$(dirname "$0")"
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -days 3650 \
  -subj "/O=nForge local development example/CN=localhost" \
  -addext "subjectAltName=DNS:localhost,IP:127.0.0.1,IP:::1" \
  -addext "extendedKeyUsage=serverAuth" -addext "keyUsage=digitalSignature" \
  -addext "basicConstraints=critical,CA:FALSE" \
  -keyout localhost.key -out localhost.crt
