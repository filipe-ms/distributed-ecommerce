package orders

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/filipe-ms/distributed-ecommerce/internal/authentication"
	"github.com/filipe-ms/distributed-ecommerce/internal/killswitch"
)

// BuildRouter wires the order service routes. Both /orders and
// /orders/{userId} are protected by JWT validation.
func BuildRouter(orderStore *Store, killSwitch *killswitch.Switch, signingSecret []byte) http.Handler {
	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(middleware.RealIP)
	router.Use(middleware.Recoverer)
	router.Use(killSwitch.Middleware)

	router.Get("/health", writeHealthHandler())
	router.Post("/admin/toggle", killSwitch.ToggleHandler)

	router.Group(func(authenticatedRouter chi.Router) {
		authenticatedRouter.Use(authentication.RequireValidToken(signingSecret))
		authenticatedRouter.Post("/orders", writePlaceOrderHandler(orderStore))
		authenticatedRouter.Get("/orders/{userId}", writeListByUserIDHandler(orderStore))
	})

	return router
}
