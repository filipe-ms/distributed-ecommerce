package products

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/filipe-ms/distributed-ecommerce/internal/authentication"
	"github.com/filipe-ms/distributed-ecommerce/internal/killswitch"
)

// BuildRouter monta as rotas do serviço de produtos. A chave de
// assinatura é passada mesmo sem ser usada por todas as rotas pra
// futuras rotas de admin não mudarem a assinatura da função.
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

	// Decrement: qualquer usuário autenticado pode tirar 1 do estoque
	// (é a forma como uma compra é refletida no catálogo).
	router.Group(func(authenticatedRouter chi.Router) {
		authenticatedRouter.Use(authentication.RequireValidToken(signingSecret))
		authenticatedRouter.Post("/products/{productId}/decrement", writeDecrementHandler(productStore))
	})

	router.Group(func(administratorRouter chi.Router) {
		administratorRouter.Use(authentication.RequireValidToken(signingSecret))
		administratorRouter.Use(authentication.RequireAdministratorRole)
		administratorRouter.Post("/products", writeCreateHandler(productStore))
	})

	return router
}
