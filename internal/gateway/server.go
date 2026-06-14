package gateway

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/filipe-ms/distributed-ecommerce/internal/tlsserver"
)

// GatewayConfiguration carries every external dependency the router needs.
// It is constructed in cmd/gateway/main.go from environment variables and
// passed in here so this package never touches os.Getenv.
type GatewayConfiguration struct {
	UsersServiceURL              string
	OrdersServiceURL             string
	ProductsPrimaryServiceURL    string
	ProductsReplicaServiceURL    string
	HTTPClientPerRequestTimeout  time.Duration
	HeartbeatPollInterval        time.Duration
	HeartbeatFailureThreshold    int
}

// Server bundles the long-lived dependencies that handlers and background
// tasks share. It is the only place outside of main.go that knows about the
// HTTP client used for downstream calls.
type Server struct {
	configuration         GatewayConfiguration
	internalClient        *http.Client
	proxyClient           *ProxyClient
	heartbeatRegistry     *HeartbeatRegistry
	eventRing             *EventRing
	productReplicaManager *ProductReplicaManager
	logger                *slog.Logger
}

// NewServer builds a Server with sensible defaults. The HTTP client used for
// internal calls skips certificate verification because we share one
// self-signed certificate across every container.
func NewServer(configuration GatewayConfiguration, logger *slog.Logger) *Server {
	if configuration.HTTPClientPerRequestTimeout == 0 {
		configuration.HTTPClientPerRequestTimeout = 10 * time.Second
	}
	if logger == nil {
		logger = slog.Default()
	}
	internalClient := tlsserver.InsecureInternalClient(configuration.HTTPClientPerRequestTimeout)
	eventRing := NewEventRing(50)

	monitoredServices := []MonitoredService{
		{Name: "users", BaseURL: configuration.UsersServiceURL},
		{Name: "orders", BaseURL: configuration.OrdersServiceURL},
		{Name: "products-primary", BaseURL: configuration.ProductsPrimaryServiceURL},
		{Name: "products-replica", BaseURL: configuration.ProductsReplicaServiceURL},
	}
	heartbeatRegistry := NewHeartbeatRegistry(monitoredServices, internalClient, logger, eventRing)
	if configuration.HeartbeatPollInterval > 0 {
		heartbeatRegistry.SetPollInterval(configuration.HeartbeatPollInterval)
	}
	if configuration.HeartbeatFailureThreshold > 0 {
		heartbeatRegistry.SetFailureThreshold(configuration.HeartbeatFailureThreshold)
	}

	return &Server{
		configuration:         configuration,
		internalClient:        internalClient,
		proxyClient:           NewProxyClient(internalClient, heartbeatRegistry),
		heartbeatRegistry:     heartbeatRegistry,
		eventRing:             eventRing,
		productReplicaManager: NewProductReplicaManager(
			configuration.ProductsPrimaryServiceURL,
			configuration.ProductsReplicaServiceURL,
			"products-primary",
			"products-replica",
			internalClient,
			heartbeatRegistry,
			logger,
		),
		logger: logger,
	}
}

// RunHeartbeat starts the heartbeat poller and blocks until heartbeatContext
// is cancelled. main.go runs this in a separate goroutine.
func (server *Server) RunHeartbeat(heartbeatContext context.Context) {
	server.heartbeatRegistry.Run(heartbeatContext)
}

// HeartbeatRegistry exposes the registry to handlers that need it (e.g. the
// dashboard). It is intentionally a getter rather than an exported field so
// code outside the package cannot accidentally swap registries at runtime.
func (server *Server) HeartbeatRegistry() *HeartbeatRegistry {
	return server.heartbeatRegistry
}

// BuildRouter assembles the public HTTP surface of the gateway.
func (server *Server) BuildRouter() http.Handler {
	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(middleware.RealIP)
	router.Use(middleware.Recoverer)

	router.Get("/health", writeGatewayHealthHandler())

	// Monitoring dashboard (HTML page) plus its JSON status endpoint and the
	// per-service toggle proxy. These are deliberately under a separate path
	// prefix from the /api/* proxy routes so they cannot conflict with
	// downstream service URLs.
	router.Get("/dashboard", server.writeDashboardHTMLHandler())
	router.Get("/administration/status", server.writeDashboardStatusHandler())
	router.Post("/administration/toggle/{serviceName}", server.writeAdminToggleProxyHandler())

	usersRoute := ServiceRoute{Name: "users", BaseURL: server.configuration.UsersServiceURL}
	ordersRoute := ServiceRoute{Name: "orders", BaseURL: server.configuration.OrdersServiceURL}

	router.Mount("/api/users", buildPrefixHandler(server.proxyClient.HandlerFor(usersRoute)))
	router.Mount("/api/orders", buildPrefixHandler(server.proxyClient.HandlerFor(ordersRoute)))
	router.Mount("/api/products", server.buildProductRoutes())

	return router
}

// buildProductRoutes returns a handler that dispatches by HTTP method:
// GET requests round-robin between the two replicas while non-GET requests
// fan out to both for strong-consistency writes.
func (server *Server) buildProductRoutes() http.Handler {
	return http.HandlerFunc(func(responseWriter http.ResponseWriter, incomingRequest *http.Request) {
		if incomingRequest.Method == http.MethodGet {
			server.productReplicaManager.HandleRead(responseWriter, incomingRequest)
			return
		}
		server.productReplicaManager.HandleWrite(responseWriter, incomingRequest)
	})
}

// buildPrefixHandler wraps a single handler so that chi's Mount accepts it.
func buildPrefixHandler(forwardHandler http.HandlerFunc) http.Handler {
	subRouter := chi.NewRouter()
	subRouter.Handle("/*", forwardHandler)
	subRouter.Handle("/", forwardHandler)
	return subRouter
}

func writeGatewayHealthHandler() http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.Header().Set("Content-Type", "application/json; charset=utf-8")
		responseWriter.WriteHeader(http.StatusOK)
		_, _ = responseWriter.Write([]byte(`{"status":"ok"}`))
	}
}
