// Ponto de entrada do serviço de produtos. O mesmo binário roda
// duas vezes (primary/replica), com pastas de dados diferentes.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/filipe-ms/distributed-ecommerce/internal/killswitch"
	"github.com/filipe-ms/distributed-ecommerce/internal/products"
	"github.com/filipe-ms/distributed-ecommerce/internal/tlsserver"
)

const responseFlushGracePeriod = 500 * time.Millisecond

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	rootContext, cancelRootContext := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancelRootContext()

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
	if seedError := productStore.SeedDefaultsIfEmpty(); seedError != nil {
		logger.Error("seeding default products", "error", seedError)
		os.Exit(1)
	}

	serviceKillSwitch := killswitch.New()
	serviceKillSwitch.SetAfterEngageCallback(func() {
		time.Sleep(responseFlushGracePeriod)
		logger.Info("kill switch engaged via /admin/toggle, beginning graceful shutdown",
			"replica", replicaName)
		cancelRootContext()
	})

	router := products.BuildRouter(productStore, serviceKillSwitch, []byte(signingSecretValue))

	logger.Info("product service starting",
		"listen", listenAddress,
		"storage", storageFilePath,
		"replica", replicaName)

	if serveError := tlsserver.ListenAndServe(rootContext, listenAddress, router, certificateFilePath, keyFilePath); serveError != nil {
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
