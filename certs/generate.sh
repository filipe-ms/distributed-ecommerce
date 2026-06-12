#!/usr/bin/env bash
# Generate a self-signed certificate plus matching key used for HTTPS between
# every microservice and the gateway. The Subject Alternative Names cover both
# the docker-compose service hostnames and "localhost" so the same certificate
# works for in-cluster traffic and for the developer hitting the gateway from
# a browser.
#
# Run from anywhere; output lands in certs/cert.pem and certs/key.pem.
set -euo pipefail

scriptDirectory="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$scriptDirectory"

certificateLifetimeInDays=365

openssl req -x509 -newkey rsa:2048 -nodes \
  -days "$certificateLifetimeInDays" \
  -keyout key.pem \
  -out cert.pem \
  -subj "/C=BR/ST=PE/L=Recife/O=FCCPD/CN=localhost" \
  -addext "subjectAltName=DNS:localhost,DNS:gateway,DNS:users,DNS:products-primary,DNS:products-replica,DNS:orders,IP:127.0.0.1"

chmod 600 key.pem
chmod 644 cert.pem

echo "Generated certs/cert.pem and certs/key.pem (valid for $certificateLifetimeInDays days)."
