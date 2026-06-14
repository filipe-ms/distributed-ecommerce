package orders

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/filipe-ms/distributed-ecommerce/internal/authentication"
	"github.com/filipe-ms/distributed-ecommerce/internal/httpjson"
)

// placeOrderRequestPayload é o corpo JSON do POST /orders. Só o
// productId vem do cliente; o usuário a gente pega do JWT, então
// ninguém consegue criar pedido em nome de outro.
type placeOrderRequestPayload struct {
	ProductID int `json:"productId"`
}

// writePlaceOrderHandler implementa o POST /orders.
func writePlaceOrderHandler(orderStore *Store) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		callerClaims := authentication.ClaimsFromContext(request.Context())
		if callerClaims == nil {
			httpjson.WriteError(responseWriter, http.StatusUnauthorized, "missing authentication")
			return
		}

		var payload placeOrderRequestPayload
		if decodeError := httpjson.ReadJSON(request, &payload); decodeError != nil {
			httpjson.WriteError(responseWriter, http.StatusBadRequest, decodeError.Error())
			return
		}
		if payload.ProductID <= 0 {
			httpjson.WriteError(responseWriter, http.StatusBadRequest, "productId must be a positive integer")
			return
		}

		created, createError := orderStore.Create(request.Context(), callerClaims.UserID, payload.ProductID)
		if errors.Is(createError, ErrInvalidOrder) {
			httpjson.WriteError(responseWriter, http.StatusBadRequest, createError.Error())
			return
		}
		if createError != nil {
			httpjson.WriteError(responseWriter, http.StatusInternalServerError, "could not place order")
			return
		}
		httpjson.WriteJSON(responseWriter, http.StatusCreated, created)
	}
}

// writeListByUserIDHandler implementa o GET /orders/{userId}.
// Usuário comum só vê os próprios pedidos; admin vê os de qualquer um.
func writeListByUserIDHandler(orderStore *Store) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		callerClaims := authentication.ClaimsFromContext(request.Context())
		if callerClaims == nil {
			httpjson.WriteError(responseWriter, http.StatusUnauthorized, "missing authentication")
			return
		}

		rawUserID := chi.URLParam(request, "userId")
		parsedUserID, parseError := strconv.Atoi(rawUserID)
		if parseError != nil || parsedUserID <= 0 {
			httpjson.WriteError(responseWriter, http.StatusBadRequest, "user id must be a positive integer")
			return
		}

		// Mesma regra de acesso do serviço de usuários.
		if callerClaims.Role != authentication.RoleAdministrator && callerClaims.UserID != parsedUserID {
			httpjson.WriteError(responseWriter, http.StatusForbidden, "you can only list your own orders")
			return
		}

		listed, listError := orderStore.ListByUserID(request.Context(), parsedUserID)
		if listError != nil {
			httpjson.WriteError(responseWriter, http.StatusInternalServerError, "could not list orders")
			return
		}
		httpjson.WriteJSON(responseWriter, http.StatusOK, listed)
	}
}

// writeHealthHandler responde no /health.
func writeHealthHandler() http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, _ *http.Request) {
		httpjson.WriteJSON(responseWriter, http.StatusOK, map[string]string{"status": "ok"})
	}
}
