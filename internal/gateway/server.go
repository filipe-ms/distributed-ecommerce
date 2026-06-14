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

// GatewayConfiguration carrega tudo que o gateway precisa pra subir.
// É montada em cmd/gateway/main.go a partir de variáveis de ambiente.
type GatewayConfiguration struct {
	UsersServiceURL              string
	OrdersServiceURL             string
	ProductsPrimaryServiceURL    string
	ProductsReplicaServiceURL    string
	HTTPClientPerRequestTimeout  time.Duration
	HeartbeatPollInterval        time.Duration
	HeartbeatFailureThreshold    int
}

// Server agrupa as dependências de longa vida que os handlers e os
// loops de fundo compartilham.
type Server struct {
	configuration         GatewayConfiguration
	internalClient        *http.Client
	proxyClient           *ProxyClient
	heartbeatRegistry     *HeartbeatRegistry
	eventRing             *EventRing
	productReplicaManager *ProductReplicaManager
	logger                *slog.Logger
}

// NewServer monta o Server com valores padrão. O cliente HTTP usado
// pra falar com os outros serviços ignora a verificação do certificado
// porque a gente reusa o mesmo cert auto-assinado em todos os containers.
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

// RunHeartbeat sobe o loop de heartbeat e fica bloqueado até o context
// ser cancelado. O main.go roda isso em uma goroutine separada.
func (server *Server) RunHeartbeat(heartbeatContext context.Context) {
	server.heartbeatRegistry.Run(heartbeatContext)
}

// HeartbeatRegistry expõe o registry pros handlers que precisam dele.
func (server *Server) HeartbeatRegistry() *HeartbeatRegistry {
	return server.heartbeatRegistry
}

// BuildRouter monta todas as rotas públicas do gateway.
func (server *Server) BuildRouter() http.Handler {
	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(middleware.RealIP)
	router.Use(middleware.Recoverer)

	router.Get("/health", writeGatewayHealthHandler())

	// Front da loja na raiz (/) e dashboard de monitoramento (/dashboard).
	router.Get("/", server.writeStorefrontHTMLHandler())
	router.Get("/dashboard", server.writeDashboardHTMLHandler())
	router.Get("/estoque", server.writeStockHTMLHandler())
	router.Get("/administration/status", server.writeDashboardStatusHandler())
	router.Post("/administration/toggle/{serviceName}", server.writeAdminToggleProxyHandler())

	usersRoute := ServiceRoute{Name: "users", BaseURL: server.configuration.UsersServiceURL}
	ordersRoute := ServiceRoute{Name: "orders", BaseURL: server.configuration.OrdersServiceURL}

	router.Mount("/api/users", buildPrefixHandler(server.proxyClient.HandlerFor(usersRoute)))
	router.Mount("/api/orders", buildPrefixHandler(server.proxyClient.HandlerFor(ordersRoute)))
	router.Mount("/api/products", server.buildProductRoutes())

	return router
}

// buildProductRoutes despacha por método HTTP: GET vai pro round-robin,
// outros métodos vão pra escrita nas duas réplicas.
func (server *Server) buildProductRoutes() http.Handler {
	return http.HandlerFunc(func(responseWriter http.ResponseWriter, incomingRequest *http.Request) {
		if incomingRequest.Method == http.MethodGet {
			server.productReplicaManager.HandleRead(responseWriter, incomingRequest)
			return
		}
		server.productReplicaManager.HandleWrite(responseWriter, incomingRequest)
	})
}

// buildPrefixHandler é um wrapper pro chi.Mount aceitar um único handler.
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
