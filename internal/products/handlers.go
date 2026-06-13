package products

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/filipe-ms/distributed-ecommerce/internal/httpjson"
)

// createProductRequestPayload is the JSON body POST /products accepts.
type createProductRequestPayload struct {
	Name        string  `json:"name"`
	Price       float64 `json:"price"`
	Description string  `json:"description"`
}

func writeListAllHandler(productStore *Store) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, _ *http.Request) {
		httpjson.WriteJSON(responseWriter, http.StatusOK, productStore.ListAll())
	}
}

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

func writeCreateHandler(productStore *Store) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		var payload createProductRequestPayload
		if decodeError := httpjson.ReadJSON(request, &payload); decodeError != nil {
			httpjson.WriteError(responseWriter, http.StatusBadRequest, decodeError.Error())
			return
		}
		created, createError := productStore.Create(payload.Name, payload.Price, payload.Description)
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

func writeHealthHandler() http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, _ *http.Request) {
		httpjson.WriteJSON(responseWriter, http.StatusOK, map[string]string{"status": "ok"})
	}
}
