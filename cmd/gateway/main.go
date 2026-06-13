// Command gateway runs the API gateway. It is the only service exposed
// outside the docker-compose network: clients hit the gateway, which
// forwards requests to the users, products and orders services over HTTPS
// while preserving the bearer token. Subsequent commits add a heartbeat
// loop, the product replica coordinator and the monitoring dashboard.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
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
		UsersServiceURL:  environmentValueOrDefault("USERS_SERVICE_URL", "https://users:5001"),
		OrdersServiceURL: environmentValueOrDefault("ORDERS_SERVICE_URL", "https://orders:5003"),
	}

	gatewayServer := gateway.NewServer(configuration)
	router := gatewayServer.BuildRouter()

	shutdownContext, cancelShutdownContext := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancelShutdownContext()

	logger.Info("gateway starting",
		"listen", listenAddress,
		"users_url", configuration.UsersServiceURL,
		"orders_url", configuration.OrdersServiceURL)

	if serveError := tlsserver.ListenAndServe(shutdownContext, listenAddress, router, certificateFilePath, keyFilePath); serveError != nil {
		logger.Error("gateway exited with error", "error", serveError)
		os.Exit(1)
	}
	logger.Info("gateway shut down cleanly")
}

func environmentValueOrDefault(variableName, fallback string) string {
	if value := os.Getenv(variableName); value != "" {
		return value
	}
	return fallback
}
