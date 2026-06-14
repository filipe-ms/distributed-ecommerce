// Ponto de entrada do serviço de pedidos. Abre o SQLite e expõe
// a API HTTPS por trás do gateway.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/filipe-ms/distributed-ecommerce/internal/killswitch"
	"github.com/filipe-ms/distributed-ecommerce/internal/orders"
	"github.com/filipe-ms/distributed-ecommerce/internal/tlsserver"
)

const responseFlushGracePeriod = 500 * time.Millisecond

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	rootContext, cancelRootContext := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancelRootContext()

	listenAddress := environmentValueOrDefault("LISTEN_ADDRESS", ":5003")
	databasePath := environmentValueOrDefault("DATABASE_PATH", "/data/orders.db")
	signingSecretValue := os.Getenv("JWT_SECRET")
	if signingSecretValue == "" {
		logger.Error("JWT_SECRET environment variable must be set")
		os.Exit(1)
	}
	certificateFilePath := environmentValueOrDefault("TLS_CERTIFICATE_PATH", "/certs/cert.pem")
	keyFilePath := environmentValueOrDefault("TLS_KEY_PATH", "/certs/key.pem")

	orderStore, openError := orders.OpenStore(databasePath)
	if openError != nil {
		logger.Error("opening orders store", "error", openError)
		os.Exit(1)
	}
	defer func() { _ = orderStore.Close() }()

	serviceKillSwitch := killswitch.New()
	serviceKillSwitch.SetAfterEngageCallback(func() {
		time.Sleep(responseFlushGracePeriod)
		logger.Info("kill switch engaged via /admin/toggle, beginning graceful shutdown")
		cancelRootContext()
	})

	router := orders.BuildRouter(orderStore, serviceKillSwitch, []byte(signingSecretValue))

	logger.Info("order service starting", "listen", listenAddress, "database", databasePath)

	if serveError := tlsserver.ListenAndServe(rootContext, listenAddress, router, certificateFilePath, keyFilePath); serveError != nil {
		logger.Error("order service exited with error", "error", serveError)
		os.Exit(1)
	}
	logger.Info("order service shut down cleanly")
}

func environmentValueOrDefault(variableName, fallback string) string {
	if value := os.Getenv(variableName); value != "" {
		return value
	}
	return fallback
}
