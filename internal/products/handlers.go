package products

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/filipe-ms/distributed-ecommerce/internal/httpjson"
)

// createProductRequestPayload é o corpo JSON do POST /products.
type createProductRequestPayload struct {
	Name        string  `json:"name"`
	Price       float64 `json:"price"`
	Description string  `json:"description"`
	Quantity    int     `json:"quantity"`
}

// writeListAllHandler responde no GET /products com o catálogo todo.
func writeListAllHandler(productStore *Store) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, _ *http.Request) {
		httpjson.WriteJSON(responseWriter, http.StatusOK, productStore.ListAll())
	}
}

// writeGetByIDHandler responde no GET /products/{productId}.
func writeGetByIDHandler(productStore *Store) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		rawID := chi.URLParam(request, "productId")
		parsedID, parseError := strconv.Atoi(rawID)
		if parseError != nil || parsedID <= 0 {
			httpjson.WriteError(responseWriter, http.StatusBadRequest, "product id must be a positive integer")
			return
		}
		found, lookupError := productStore.GetByID(parsedID)
		if errors.Is(lookupError, ErrProductNotFound) {
			httpjson.WriteError(responseWriter, http.StatusNotFound, "product not found")
			return
		}
		if lookupError != nil {
			httpjson.WriteError(responseWriter, http.StatusInternalServerError, "could not load product")
			return
		}
		httpjson.WriteJSON(responseWriter, http.StatusOK, found)
	}
}

// writeCreateHandler responde no POST /products. Só admin chega aqui.
func writeCreateHandler(productStore *Store) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		var payload createProductRequestPayload
		if decodeError := httpjson.ReadJSON(request, &payload); decodeError != nil {
			httpjson.WriteError(responseWriter, http.StatusBadRequest, decodeError.Error())
			return
		}
		created, createError := productStore.Create(payload.Name, payload.Price, payload.Description, payload.Quantity)
		if errors.Is(createError, ErrInvalidProduct) {
			httpjson.WriteError(responseWriter, http.StatusBadRequest, createError.Error())
			return
		}
		if createError != nil {
			httpjson.WriteError(responseWriter, http.StatusInternalServerError, "could not create product")
			return
		}
		httpjson.WriteJSON(responseWriter, http.StatusCreated, created)
	}
}

// writeDecrementHandler responde no POST /products/{productId}/decrement.
// Tira uma unidade do estoque. Qualquer usuário autenticado pode chamar.
func writeDecrementHandler(productStore *Store) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		rawID := chi.URLParam(request, "productId")
		parsedID, parseError := strconv.Atoi(rawID)
		if parseError != nil || parsedID <= 0 {
			httpjson.WriteError(responseWriter, http.StatusBadRequest, "product id must be a positive integer")
			return
		}
		updated, decrementError := productStore.DecrementStock(parsedID)
		if errors.Is(decrementError, ErrProductNotFound) {
			httpjson.WriteError(responseWriter, http.StatusNotFound, "product not found")
			return
		}
		if errors.Is(decrementError, ErrOutOfStock) {
			httpjson.WriteError(responseWriter, http.StatusConflict, "product is out of stock")
			return
		}
		if decrementError != nil {
			httpjson.WriteError(responseWriter, http.StatusInternalServerError, "could not decrement stock")
			return
		}
		httpjson.WriteJSON(responseWriter, http.StatusOK, updated)
	}
}

// writeHealthHandler responde no /health.
func writeHealthHandler() http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, _ *http.Request) {
		httpjson.WriteJSON(responseWriter, http.StatusOK, map[string]string{"status": "ok"})
	}
}
