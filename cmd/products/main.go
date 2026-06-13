// Command products runs the catalogue microservice. The same binary is
// launched twice in docker-compose (products-primary and products-replica),
// each with its own STORAGE_PATH, so the gateway can write to both replicas
// for strong consistency.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/filipe-ms/distributed-ecommerce/internal/killswitch"
	"github.com/filipe-ms/distributed-ecommerce/internal/products"
	"github.com/filipe-ms/distributed-ecommerce/internal/tlsserver"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	listenAddress := environmentValueOrDefault("LISTEN_ADDRESS", ":5002")
	storageFilePath := environmentValueOrDefault("STORAGE_PATH", "/data/products.json")
	signingSecretValue := os.Getenv("JWT_SECRET")
	if signingSecretValue == "" {
		logger.Error("JWT_SECRET environment variable must be set")
		os.Exit(1)
	}
	certificateFilePath := environmentValueOrDefault("TLS_CERTIFICATE_PATH", "/certs/cert.pem")
	keyFilePath := environmentValueOrDefault("TLS_KEY_PATH", "/certs/key.pem")
	replicaName := environmentValueOrDefault("REPLICA_NAME", "products")

	productStore, openError := products.OpenStore(storageFilePath)
	if openError != nil {
		logger.Error("opening products store", "error", openError)
		os.Exit(1)
	}

	serviceKillSwitch := killswitch.New()
	router := products.BuildRouter(productStore, serviceKillSwitch, []byte(signingSecretValue))

	shutdownContext, cancelShutdownContext := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancelShutdownContext()

	logger.Info("product service starting",
		"listen", listenAddress,
		"storage", storageFilePath,
		"replica", replicaName)

	if serveError := tlsserver.ListenAndServe(shutdownContext, listenAddress, router, certificateFilePath, keyFilePath); serveError != nil {
		logger.Error("product service exited with error", "error", serveError)
		os.Exit(1)
	}
	logger.Info("product service shut down cleanly")
}

func environmentValueOrDefault(variableName, fallback string) string {
	if value := os.Getenv(variableName); value != "" {
		return value
	}
	return fallback
}
