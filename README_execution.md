# Execution Guide

This document explains how to run the distributed e-commerce stack locally,
walks through the demo flow that exercises every requirement, and shows how
to use the monitoring dashboard to simulate and recover from outages.

## 1. Prerequisites

You only need two things:

| Tool | Tested version |
|------|----------------|
| Docker (Engine or Desktop) | 24.x or newer |
| Docker Compose plugin | v2.x |

`curl` and `jq` make the demo flow easier to read but are not required.
There is **no need to install Go locally** — the build happens inside the
`golang:1.22-alpine` image.

## 2. Configuration

The single secret the stack needs is the JWT signing key. Copy the example
file and adjust the value if you wish:

```bash
cp .env.example .env
```

The default value (`dev-secret-change-me-before-shipping`) is fine for
grading; in production you would set it to a long random string.

## 3. Generating TLS material

Every container uses the same self-signed certificate to terminate HTTPS.
A short script regenerates it from scratch:

```bash
bash certs/generate.sh
```

This produces `certs/cert.pem` and `certs/key.pem`. They are baked into the
Docker image at build time, so you only need to run this script once
(or when the previous certificate expires after one year).

> **Note:** the certificate is valid for `localhost` and for every internal
> service hostname (`gateway`, `users`, `products-primary`,
> `products-replica`, `orders`). The browser will show a one-time warning
> when you open the dashboard — accept it and move on.

## 4. Starting the stack

From the repository root:

```bash
docker compose up --build
```

The first build takes ~90 seconds (Go module download + compile). Subsequent
runs reuse the cache and start in a few seconds. When the stack is healthy,
five containers are running:

| Container | Port (internal) | Notes |
|-----------|-----------------|-------|
| `gateway` | `8443` (published) | Single entry point, also serves the dashboard |
| `users` | `5001` | SQLite-backed user/auth service |
| `products-primary` | `5002` | Catalogue replica A |
| `products-replica` | `5012` | Catalogue replica B |
| `orders` | `5003` | SQLite-backed order service |

Tear everything down with `docker compose down`. Volumes are kept by
default; add `-v` to wipe persisted data as well.

## 5. The dashboard

Open <https://localhost:8443/dashboard>. After accepting the self-signed
certificate, you see a status grid with four rows — one per backing service
— plus the most recent up/down events. Each row has a button:

* **Kill** — engages the kill switch on that service. The service responds
  to the toggle, then exits gracefully (~500 ms later). The Compose
  `restart: unless-stopped` policy brings it back up automatically; the
  heartbeat detects the brief outage and logs `DOWN` and `RECOVERED`
  events.
* **Revive** — appears briefly between the kill and the auto-restart. Most
  graders never see it, because the recovery is faster than they can
  click.

The page polls `GET /administration/status` every two seconds, so the
status indicators turn red within ~5–10 seconds of a real outage and back
to green within ~5 seconds of the container restart completing.

## 6. Demo flow with curl

The default administrator account is created the first time the user
service starts:

```
email:    admin@local
password: admin123
```

Run these commands from a second terminal while the stack is up. The `-k`
flag tells curl to accept the self-signed certificate.

```bash
# (1) Register a regular user.
curl -k -X POST https://localhost:8443/api/users/register \
     -H 'content-type: application/json' \
     -d '{"name":"Alice","email":"alice@example.com","password":"hunter2"}'

# (2) Log in as the administrator.
ADMINISTRATOR_TOKEN=$(curl -ks -X POST https://localhost:8443/api/users/login \
     -H 'content-type: application/json' \
     -d '{"email":"admin@local","password":"admin123"}' \
     | jq -r .token)

# (3) Create a product (only administrators are allowed to).
curl -k -X POST https://localhost:8443/api/products \
     -H "authorization: Bearer $ADMINISTRATOR_TOKEN" \
     -H 'content-type: application/json' \
     -d '{"name":"Coffee","price":9.99,"description":"Arabica"}'

# (4) Confirm both replicas have the product.
docker compose exec products-primary cat /data/products.json
docker compose exec products-replica cat /data/products.json

# (5) Log in as Alice and place an order.
ALICE_TOKEN=$(curl -ks -X POST https://localhost:8443/api/users/login \
     -H 'content-type: application/json' \
     -d '{"email":"alice@example.com","password":"hunter2"}' \
     | jq -r .token)

curl -k -X POST https://localhost:8443/api/orders \
     -H "authorization: Bearer $ALICE_TOKEN" \
     -H 'content-type: application/json' \
     -d '{"productId":1}'

# (6) List Alice's orders.
ALICE_USER_ID=$(curl -ks -X POST https://localhost:8443/api/users/login \
     -H 'content-type: application/json' \
     -d '{"email":"alice@example.com","password":"hunter2"}' \
     | jq -r .user.id)

curl -k -H "authorization: Bearer $ALICE_TOKEN" \
     "https://localhost:8443/api/orders/$ALICE_USER_ID"
```

You can also try the negative paths:

```bash
# Regular users cannot create products: returns HTTP 403.
curl -k -i -X POST https://localhost:8443/api/products \
     -H "authorization: Bearer $ALICE_TOKEN" \
     -H 'content-type: application/json' \
     -d '{"name":"Tea","price":4.5,"description":""}'

# Alice cannot list someone else's orders: returns HTTP 403.
curl -k -i -H "authorization: Bearer $ALICE_TOKEN" \
     https://localhost:8443/api/orders/9999
```

## 7. Simulating an outage

1. Open the dashboard.
2. Click **Kill** next to `orders`.
3. Within ~10 seconds the indicator turns red and an event line appears:
   `<timestamp>  orders  DOWN`.
4. While the service is down, hitting `https://localhost:8443/api/orders`
   returns **HTTP 503** — the gateway short-circuits the request because
   the heartbeat marks the service as unavailable.
5. A few seconds later, the container restarts automatically. The
   indicator turns green and a `RECOVERED` event is appended.

You can repeat the experiment for any of the four backing services. Killing
`products-primary` is particularly instructive: read traffic continues
because `products-replica` is still serving, but write traffic returns
**HTTP 500** because strong-consistency replication requires both replicas
to acknowledge.

## 8. Running unit tests

The Go test suite runs entirely inside Docker so you do not need a local Go
toolchain:

```bash
docker run --rm -v "$PWD:/src" -w /src golang:1.22-alpine \
    sh -c 'go mod tidy && go test ./...'
```

You should see green output across `internal/authentication`,
`internal/httpjson`, `internal/killswitch`, `internal/users`,
`internal/products`, `internal/orders` and `internal/gateway`.

## 9. Project layout

```
.
├── certs/              self-signed certificate generation
├── cmd/
│   ├── gateway/        gateway entry point (main.go)
│   ├── users/          user service entry point
│   ├── products/       product service entry point (used twice in compose)
│   └── orders/         order service entry point
├── internal/
│   ├── authentication/ JWT + bcrypt + middleware
│   ├── httpjson/       JSON read/write helpers
│   ├── killswitch/     /admin/toggle implementation
│   ├── tlsserver/      HTTPS server wrapper, internal HTTP client
│   ├── users/          user service handlers and SQLite store
│   ├── products/       product service handlers and JSON store
│   ├── orders/         order service handlers and SQLite store
│   └── gateway/        proxy, heartbeat, replica manager, dashboard
├── Dockerfile          single image used by every service
├── docker-compose.yml  five containers, one network, four volumes
└── README_execution.md you are here
```

## 10. What to look at while grading

* **Strong consistency:** `internal/gateway/replica.go` — `HandleWrite`
  fans out to both replicas and only returns success when both 2xx.
* **Heartbeat:** `internal/gateway/heartbeat.go` — five-second poll, mark
  DOWN after two consecutive failures, mark RECOVERED on the first
  successful probe.
* **JWT and admin guard:** `internal/authentication/authentication.go`
  (signing/verification) and `internal/products/server.go` (admin-only
  route group).
* **Password hashing:** `HashPassword` / `VerifyPassword` in
  `internal/authentication/authentication.go`, used at every entry point in
  `internal/users/handlers.go`.

The accompanying **report.pdf** answers the five required questions and
discusses the trade-offs (strong vs. eventual consistency, single shared
JWT secret, `InsecureSkipVerify` on internal calls, etc.).
