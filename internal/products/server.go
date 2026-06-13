package products

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/filipe-ms/distributed-ecommerce/internal/authentication"
	"github.com/filipe-ms/distributed-ecommerce/internal/killswitch"
)

// BuildRouter wires the product service routes. The signing secret is
// passed in even though only the create endpoint is protected, so future
// admin-only routes can be added without changing the constructor signature.
func BuildRouter(productStore *Store, killSwitch *killswitch.Switch, signingSecret []byte) http.Handler {
	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(middleware.RealIP)
	router.Use(middleware.Recoverer)
	router.Use(killSwitch.Middleware)

	router.Get("/health", writeHealthHandler())
	router.Post("/admin/toggle", killSwitch.ToggleHandler)

	router.Get("/products", writeListAllHandler(productStore))
	router.Get("/products/{productId}", writeGetByIDHandler(productStore))

	router.Group(func(administratorRouter chi.Router) {
		administratorRouter.Use(authentication.RequireValidToken(signingSecret))
		administratorRouter.Use(authentication.RequireAdministratorRole)
		administratorRouter.Post("/products", writeCreateHandler(productStore))
	})

	return router
}
