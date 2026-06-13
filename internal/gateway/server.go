package gateway

import (
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
	UsersServiceURL  string
	OrdersServiceURL string
	// Forward-declared fields filled in by later phases (heartbeat, product
	// replicas, dashboard) so adding them does not break the constructor
	// signature.
	HTTPClientPerRequestTimeout time.Duration
}

// Server bundles the long-lived dependencies that handlers and background
// tasks share. It is the only place outside of main.go that knows about the
// HTTP client used for downstream calls.
type Server struct {
	configuration   GatewayConfiguration
	internalClient  *http.Client
	proxyClient     *ProxyClient
}

// NewServer builds a Server with sensible defaults. The HTTP client used for
// internal calls skips certificate verification because we share one
// self-signed certificate across every container.
func NewServer(configuration GatewayConfiguration) *Server {
	if configuration.HTTPClientPerRequestTimeout == 0 {
		configuration.HTTPClientPerRequestTimeout = 10 * time.Second
	}
	internalClient := tlsserver.InsecureInternalClient(configuration.HTTPClientPerRequestTimeout)
	return &Server{
		configuration:  configuration,
		internalClient: internalClient,
		proxyClient:    NewProxyClient(internalClient, nil),
	}
}

// BuildRouter assembles the public HTTP surface of the gateway. The product
// replica routes and the dashboard are mounted in subsequent commits as the
// project grows; for now only the users and orders services are reachable.
func (server *Server) BuildRouter() http.Handler {
	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(middleware.RealIP)
	router.Use(middleware.Recoverer)

	// The gateway's own /health endpoint lets infrastructure probe the
	// gateway itself without bouncing through any backing service.
	router.Get("/health", writeGatewayHealthHandler())

	usersRoute := ServiceRoute{Name: "users", BaseURL: server.configuration.UsersServiceURL}
	ordersRoute := ServiceRoute{Name: "orders", BaseURL: server.configuration.OrdersServiceURL}

	router.Mount("/api/users", buildPrefixHandler(server.proxyClient.HandlerFor(usersRoute)))
	router.Mount("/api/orders", buildPrefixHandler(server.proxyClient.HandlerFor(ordersRoute)))

	return router
}

// buildPrefixHandler wraps a single handler so that chi's Mount accepts it.
// chi.Router.Mount expects an http.Handler that handles every method/path
// under its prefix; our forward handler does exactly that.
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
