package orders

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/filipe-ms/distributed-ecommerce/internal/authentication"
	"github.com/filipe-ms/distributed-ecommerce/internal/killswitch"
)

// BuildRouter monta as rotas do serviço de pedidos. Tanto /orders
// quanto /orders/{userId} exigem JWT.
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
