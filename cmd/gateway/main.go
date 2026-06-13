// Command gateway runs the API gateway. It is the only service exposed
// outside the docker-compose network: clients hit the gateway, which
// forwards requests to the users, products and orders services over HTTPS
// while preserving the bearer token. The gateway also runs a heartbeat
// loop that flips routes to 503 when a downstream service stops responding.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/filipe-ms/distributed-ecommerce/internal/gateway"
	"github.com/filipe-ms/distributed-ecommerce/internal/tlsserver"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	listenAddress := environmentValueOrDefault("LISTEN_ADDRESS", ":8443")
	certificateFilePath := environmentValueOrDefault("TLS_CERTIFICATE_PATH", "/certs/cert.pem")
	keyFilePath := environmentValueOrDefault("TLS_KEY_PATH", "/certs/key.pem")

	configuration := gateway.GatewayConfiguration{
		UsersServiceURL:           environmentValueOrDefault("USERS_SERVICE_URL", "https://users:5001"),
		OrdersServiceURL:          environmentValueOrDefault("ORDERS_SERVICE_URL", "https://orders:5003"),
		ProductsPrimaryServiceURL: environmentValueOrDefault("PRODUCTS_PRIMARY_URL", "https://products-primary:5002"),
		ProductsReplicaServiceURL: environmentValueOrDefault("PRODUCTS_REPLICA_URL", "https://products-replica:5012"),
	}

	gatewayServer := gateway.NewServer(configuration, logger)
	router := gatewayServer.BuildRouter()

	rootContext, cancelRootContext := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancelRootContext()

	var heartbeatWaitGroup sync.WaitGroup
	heartbeatWaitGroup.Add(1)
	go func() {
		defer heartbeatWaitGroup.Done()
		gatewayServer.RunHeartbeat(rootContext)
	}()

	logger.Info("gateway starting",
		"listen", listenAddress,
		"users_url", configuration.UsersServiceURL,
		"orders_url", configuration.OrdersServiceURL,
		"products_primary_url", configuration.ProductsPrimaryServiceURL,
		"products_replica_url", configuration.ProductsReplicaServiceURL)

	if serveError := tlsserver.ListenAndServe(rootContext, listenAddress, router, certificateFilePath, keyFilePath); serveError != nil {
		logger.Error("gateway exited with error", "error", serveError)
		cancelRootContext()
		heartbeatWaitGroup.Wait()
		os.Exit(1)
	}
	heartbeatWaitGroup.Wait()
	logger.Info("gateway shut down cleanly")
}

func environmentValueOrDefault(variableName, fallback string) string {
	if value := os.Getenv(variableName); value != "" {
		return value
	}
	return fallback
}
