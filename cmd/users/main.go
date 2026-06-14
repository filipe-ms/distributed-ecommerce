// Ponto de entrada do serviço de usuários. Abre o banco SQLite,
// cria as contas padrão (admin e usuário) se faltarem, e sobe o
// servidor HTTPS.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/filipe-ms/distributed-ecommerce/internal/killswitch"
	"github.com/filipe-ms/distributed-ecommerce/internal/tlsserver"
	"github.com/filipe-ms/distributed-ecommerce/internal/users"
)

// Pequeno tempo de espera antes de desligar o processo, só para
// garantir que a resposta HTTP do toggle saia antes do shutdown.
const responseFlushGracePeriod = 500 * time.Millisecond

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	rootContext, cancelRootContext := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancelRootContext()

	listenAddress := environmentValueOrDefault("LISTEN_ADDRESS", ":5001")
	databasePath := environmentValueOrDefault("DATABASE_PATH", "/data/users.db")
	signingSecretValue := os.Getenv("JWT_SECRET")
	if signingSecretValue == "" {
		logger.Error("JWT_SECRET environment variable must be set")
		os.Exit(1)
	}
	tokenLifetimeMinutes := parseIntegerOrDefault("TOKEN_LIFETIME_MINUTES", 60)
	tokenLifetime := time.Duration(tokenLifetimeMinutes) * time.Minute

	defaultAdministratorEmail := environmentValueOrDefault("DEFAULT_ADMINISTRATOR_EMAIL", "admin@local")
	defaultAdministratorPassword := environmentValueOrDefault("DEFAULT_ADMINISTRATOR_PASSWORD", "admin123")
	defaultUserEmail := environmentValueOrDefault("DEFAULT_USER_EMAIL", "user@local")
	defaultUserPassword := environmentValueOrDefault("DEFAULT_USER_PASSWORD", "user123")

	certificateFilePath := environmentValueOrDefault("TLS_CERTIFICATE_PATH", "/certs/cert.pem")
	keyFilePath := environmentValueOrDefault("TLS_KEY_PATH", "/certs/key.pem")

	userStore, openError := users.OpenStore(databasePath)
	if openError != nil {
		logger.Error("opening users store", "error", openError)
		os.Exit(1)
	}
	defer func() { _ = userStore.Close() }()

	seedingContext, cancelSeedingContext := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelSeedingContext()
	if seedError := userStore.EnsureDefaultAccountsExist(seedingContext, defaultAdministratorEmail, defaultAdministratorPassword, defaultUserEmail, defaultUserPassword); seedError != nil {
		logger.Error("seeding default accounts", "error", seedError)
		os.Exit(1)
	}

	serviceKillSwitch := killswitch.New()
	serviceKillSwitch.SetAfterEngageCallback(func() {
		time.Sleep(responseFlushGracePeriod)
		logger.Info("kill switch engaged via /admin/toggle, beginning graceful shutdown")
		cancelRootContext()
	})

	router := users.BuildRouter(userStore, serviceKillSwitch, []byte(signingSecretValue), tokenLifetime)

	logger.Info("user service starting",
		"listen", listenAddress,
		"database", databasePath,
		"token_lifetime_minutes", tokenLifetimeMinutes)

	if serveError := tlsserver.ListenAndServe(rootContext, listenAddress, router, certificateFilePath, keyFilePath); serveError != nil {
		logger.Error("user service exited with error", "error", serveError)
		os.Exit(1)
	}
	logger.Info("user service shut down cleanly")
}

func environmentValueOrDefault(variableName, fallback string) string {
	if value := os.Getenv(variableName); value != "" {
		return value
	}
	return fallback
}

// parseIntegerOrDefault tenta converter a variável de ambiente para int.
// Se ela não existir ou estiver inválida, devolve o fallback.
func parseIntegerOrDefault(variableName string, fallback int) int {
	rawValue := os.Getenv(variableName)
	if rawValue == "" {
		return fallback
	}
	parsedValue, parseError := strconv.Atoi(rawValue)
	if parseError != nil {
		return fallback
	}
	return parsedValue
}
