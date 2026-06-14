# Distributed Mini E-Commerce — Project Report

> Written for the FCCPD coursework. Covers the five required questions and
> the implementation choices that back them up.

## Architecture summary

The system is composed of five long-running processes communicating
exclusively over HTTPS:

```
                              ┌───────────────────────┐
                       HTTPS  │     API Gateway       │
   Browser / curl ───────────▶│  - JWT pass-through   │  :8443
                              │  - Heartbeat poller   │
                              │  - Replica coordinator│
                              │  - Dashboard          │
                              └─────────┬─────────────┘
                                        │  HTTPS  (InsecureSkipVerify)
   ┌──────────────┬──────────────────────┼───────────────────────┬──────────────┐
   ▼              ▼                      ▼                       ▼              ▼
┌──────────┐  ┌──────────────────┐  ┌──────────────────┐  ┌──────────┐
│  users   │  │ products-primary │  │ products-replica │  │  orders  │
│  :5001   │  │      :5002       │  │      :5012       │  │  :5003   │
│  SQLite  │  │  products.json   │  │  products.json   │  │  SQLite  │
└──────────┘  └──────────────────┘  └──────────────────┘  └──────────┘
```

Every service exposes `/health` and `/admin/toggle`. The kill switch's
dashboard button engages the toggle, which prompts the service to exit
gracefully; `restart: unless-stopped` then brings the container back up
automatically. The gateway's heartbeat detects the brief outage, marks the
service `DOWN`, and logs `RECOVERED` on the first successful probe after
the container is back.

---

## Q1 — How do the services communicate?

REST/JSON over HTTPS. There is no message bus, gRPC, or shared database.
The flow is:

1. The client (browser, curl, dashboard) sends an HTTPS request to the
   gateway on port 8443. The body is JSON; authentication is a `Bearer`
   token in the `Authorization` header.
2. The gateway determines the target service from the URL prefix
   (`/api/users/...`, `/api/products/...`, `/api/orders/...`) and uses
   its internal `*http.Client` to make a second HTTPS request to the
   downstream service. The bearer token is forwarded verbatim.
3. The downstream service validates the JWT against the shared
   `JWT_SECRET` and processes the request.
4. The response is streamed back through the gateway to the client.

Internally the gateway uses `tls.InsecureSkipVerify=true` because the
self-signed certificate is shared between every container; production
deployments would replace this with a CA-signed chain (and ideally mTLS).

---

## Q2 — Which consistency strategy was adopted?

**Strong consistency** for the product catalogue. The gateway's
`ProductReplicaManager` issues writes (`POST /products`) to **both**
replicas in parallel via two goroutines, waits for both responses, and
only returns success when both replicas reply with `2xx`. Reads
round-robin between replicas; if the chosen replica is currently marked
unavailable by the heartbeat registry, the request transparently falls
over to the other one.

### Justification

The catalogue is read far more often than it is written, and stale reads
("the product I just created doesn't appear yet") would be confusing in a
demo. With only two replicas the cost of strong consistency is a hit to
write availability — if either replica is down, writes fail with `500`
— which is a deliberate trade-off:

| Concern | Choice | Cost |
|---------|--------|------|
| Read freshness | Strong | Higher write coupling |
| Write availability | Reduced | Writes blocked when 1/2 replicas are down |
| Read availability | Increased | Reads succeed if either replica is up |

For a production-scale catalogue with many replicas, the trade-off would
shift toward eventual consistency (asynchronous replication or quorum-based
writes) because the probability of "all replicas up" decreases sharply as
the replica count grows.

### What about partial failures?

If replica A succeeds and replica B fails, the gateway returns `500` and
logs the inconsistency. There is **no automatic rollback** — implementing
two-phase commit or write-ahead replication is well outside the scope of
this project. A real system would either use 2PC or an idempotent
replicated log (Raft, Kafka).

---

## Q3 — What happens if the Order Service goes down?

The gateway's heartbeat sends a `GET /health` to every service every
5 seconds (`HeartbeatPollInterval`). After **two consecutive failures**
(`FailuresBeforeMarkingDown = 2`), the registry flips the order service
to `DOWN`, logs the event, and pushes it onto the dashboard's event ring.

From that moment forward:

* Any request to `/api/orders/*` returns **HTTP 503** immediately,
  without touching the network. The gateway's `ProxyClient.HandlerFor`
  short-circuits when `availabilityRegistry.IsAvailable("orders")` is
  false.
* The user service and product service are completely unaffected — login,
  registration, catalogue browsing and product creation all keep working.
  Failures are isolated.
* Orders placed during the outage are **lost** for as long as the service
  is down. There is no retry queue. A real system would buffer writes via
  a durable message broker (Kafka, RabbitMQ) or accept the requests
  optimistically and reconcile later.

When the service comes back up, the next successful probe flips it to
`UP` again, logs a `RECOVERED` event, and traffic resumes immediately. No
manual intervention is required. The whole cycle — kill, detect, restart,
recover — completes in roughly 15 seconds with the default poll interval.

---

## Q4 — How does JWT prevent regular users from creating products?

`POST /products` is wrapped in two middlewares applied as a chi sub-group
(`internal/products/server.go`):

```go
router.Group(func(administratorRouter chi.Router) {
    administratorRouter.Use(authentication.RequireValidToken(signingSecret))
    administratorRouter.Use(authentication.RequireAdministratorRole)
    administratorRouter.Post("/products", writeCreateHandler(productStore))
})
```

The first middleware does three things:

1. Pulls the bearer token out of the `Authorization` header.
2. Verifies the HMAC-SHA256 signature against the shared
   `JWT_SECRET`. The library is pinned to that algorithm so an attacker
   cannot substitute `alg: none`.
3. Checks the `exp` claim and rejects expired tokens.

If any of those fail, the request returns `401`.

The second middleware reads the `role` claim from the verified payload
and rejects anything that is not `admin` with `403`. Because the JWT is
signed, a regular user cannot tamper with the `role` claim — the
signature would no longer verify. They also cannot mint a new token
claiming `role=admin` because they don't possess the signing secret.

The login endpoint enforces this from the start: when someone logs in,
the user service queries the row's `role` column from SQLite (defaulted
to `'user'` at registration) and writes that value into the issued
token. The only way to obtain an admin token is to log in as an existing
admin account.

---

## Q5 — Limitations vs. a real production system

* **Single shared JWT secret.** Every service uses the same HMAC key.
  Real systems use asymmetric signing (RS256/ES256) so only the auth
  service holds the private key; downstream services verify with the
  public key.
* **`InsecureSkipVerify` on internal HTTPS.** Self-signed certs are baked
  into every image and verification is disabled. Production needs a
  CA-signed chain and ideally mutual TLS for service-to-service auth.
* **No conflict resolution for product replicas.** Both replicas accept
  identical writes via the gateway, but if a network partition let them
  diverge, there is no merge logic. We sidestep the problem by routing
  every write through the gateway and rejecting on partial failure.
* **No automatic rollback on partial replica failure.** If replica A
  succeeds and replica B fails, the gateway logs and returns `500`. No
  attempt is made to undo the successful write.
* **No service discovery.** Service URLs are wired in via environment
  variables. Production would use Consul, Kubernetes DNS, or a service
  mesh.
* **No rate limiting, no observability.** No Prometheus metrics, no
  OpenTelemetry traces, no structured access log shipping.
* **No cross-service transactions.** An order can reference a deleted
  product because the order service does not consult the product service.
  This is intentional (it keeps the failure model simple), but a real
  system would either validate via an event log or accept the eventual
  inconsistency.
* **In-memory kill state and event ring.** Both reset whenever a
  container restarts. Acceptable for a dashboard demo, useless as an
  audit trail.
* **Single-instance gateway.** A real deployment would run multiple
  gateway instances behind a load balancer; ours is a single point of
  failure.
* **No request throttling on `/admin/toggle`.** Anyone with network access
  to a service can knock it offline. Production would put this behind an
  authenticated admin endpoint.

---

## What is and is not graded

| Requirement | Where to find it |
|-------------|------------------|
| Gateway routing + JWT pass-through | `internal/gateway/proxy.go`, `internal/gateway/server.go` |
| User service (register, login, get) | `internal/users/handlers.go`, `internal/users/store.go` |
| Product service (list, get, create) | `internal/products/handlers.go`, `internal/products/store.go` |
| Order service (place, list) | `internal/orders/handlers.go`, `internal/orders/store.go` |
| JWT (HS256, exp, role) | `internal/authentication/authentication.go` |
| Password hashing (bcrypt) | `HashPassword` / `VerifyPassword` |
| Admin guard | `RequireAdministratorRole` middleware |
| Two product replicas | `products-primary`, `products-replica` in `docker-compose.yml` |
| Strong-consistency replication | `internal/gateway/replica.go` (`HandleWrite`) |
| Round-robin reads | `internal/gateway/replica.go` (`HandleRead`) |
| Heartbeat (5 s, 2-strike) | `internal/gateway/heartbeat.go` |
| Failure & recovery logging | `slog.Info("service recovered")`, `slog.Warn("service marked down")` |
| `/health` on every service | each service's `BuildRouter` |
| Dashboard | `internal/gateway/web/dashboard.html`, `internal/gateway/dashboard.go` |
| Docker Compose (bonus) | `Dockerfile`, `docker-compose.yml` |
| HTTPS/TLS (bonus) | `internal/tlsserver/tlsserver.go`, `certs/generate.sh` |
| Monitoring dashboard (bonus) | served at `https://localhost:8443/dashboard` |

The kill button on the dashboard simulates an outage by triggering a
graceful self-shutdown; the Compose restart policy brings the container
back up so the recovery path is exercised end-to-end without any manual
intervention.
