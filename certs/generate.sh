#!/usr/bin/env bash

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

echo "Gerado certs/cert.pem e certs/key.pem (válido por $certificateLifetimeInDays dias)."
