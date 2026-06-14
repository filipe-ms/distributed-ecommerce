package users

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/filipe-ms/distributed-ecommerce/internal/authentication"
	"github.com/filipe-ms/distributed-ecommerce/internal/killswitch"
)

// BuildRouter monta toda a parte HTTP do serviço de usuários. É o
// único ponto de contato entre o pacote e o cmd/users/main.go.
func BuildRouter(userStore *Store, killSwitch *killswitch.Switch, signingSecret []byte, tokenLifetime time.Duration) http.Handler {
	deps := dependencies{
		userStore:     userStore,
		signingSecret: signingSecret,
		tokenLifetime: tokenLifetime,
	}

	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(middleware.RealIP)
	router.Use(middleware.Recoverer)
	router.Use(killSwitch.Middleware)

	router.Get("/health", writeHealthHandler())
	router.Post("/admin/toggle", killSwitch.ToggleHandler)

	router.Post("/users/register", writeRegistrationHandler(deps))
	router.Post("/users/login", writeLoginHandler(deps))

	router.Group(func(authenticatedRouter chi.Router) {
		authenticatedRouter.Use(authentication.RequireValidToken(signingSecret))
		authenticatedRouter.Get("/users/{userId}", writeGetUserByIDHandler(deps))
	})

	return router
}
