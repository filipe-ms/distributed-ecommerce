package products

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/filipe-ms/distributed-ecommerce/internal/authentication"
	"github.com/filipe-ms/distributed-ecommerce/internal/killswitch"
)

const testSigningSecretLiteral = "products-test-secret"
const testTokenLifetime = time.Minute

func openTemporaryProductStore(t *testing.T) *Store {
	t.Helper()
	storagePath := filepath.Join(t.TempDir(), "products.json")
	productStore, openError := OpenStore(storagePath)
	if openError != nil {
		t.Fatalf("OpenStore failed: %v", openError)
	}
	return productStore
}

func buildProductRouter(t *testing.T) (http.Handler, *Store) {
	t.Helper()
	productStore := openTemporaryProductStore(t)
	router := BuildRouter(productStore, killswitch.New(), []byte(testSigningSecretLiteral))
	return router, productStore
}

func performJSONProductRequest(router http.Handler, method, path string, payload any, bearerToken string) *httptest.ResponseRecorder {
	encodedBody := bytes.NewBuffer(nil)
	if payload != nil {
		_ = json.NewEncoder(encodedBody).Encode(payload)
	}
	request := httptest.NewRequest(method, path, encodedBody)
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if bearerToken != "" {
		request.Header.Set("Authorization", "Bearer "+bearerToken)
	}
	recordedResponse := httptest.NewRecorder()
	router.ServeHTTP(recordedResponse, request)
	return recordedResponse
}

func TestStoreCreateAssignsAscendingIDs(t *testing.T) {
	productStore := openTemporaryProductStore(t)
	first, _ := productStore.Create("Coffee", 9.99, "Arabica", 0)
	second, _ := productStore.Create("Tea", 4.50, "Earl Grey", 0)
	if first.ID != 1 || second.ID != 2 {
		t.Fatalf("expected IDs 1 and 2, got %d and %d", first.ID, second.ID)
	}
}

func TestStoreCreateRejectsInvalid(t *testing.T) {
	productStore := openTemporaryProductStore(t)
	_, emptyNameError := productStore.Create("", 1.0, "", 0)
	if !errors.Is(emptyNameError, ErrInvalidProduct) {
		t.Fatalf("expected ErrInvalidProduct for empty name, got %v", emptyNameError)
	}
	_, negativePriceError := productStore.Create("Coffee", -1.0, "", 0)
	if !errors.Is(negativePriceError, ErrInvalidProduct) {
		t.Fatalf("expected ErrInvalidProduct for negative price, got %v", negativePriceError)
	}
}

func TestStoreSurvivesReopen(t *testing.T) {
	storagePath := filepath.Join(t.TempDir(), "products.json")
	firstStore, _ := OpenStore(storagePath)
	_, _ = firstStore.Create("Coffee", 9.99, "Arabica", 0)
	_, _ = firstStore.Create("Tea", 4.50, "Earl Grey", 0)

	reopenedStore, reopenError := OpenStore(storagePath)
	if reopenError != nil {
		t.Fatalf("re-opening store failed: %v", reopenError)
	}
	persistedList := reopenedStore.ListAll()
	if len(persistedList) != 2 {
		t.Fatalf("expected 2 products after reopen, got %d", len(persistedList))
	}
	if persistedList[0].ID != 1 || persistedList[1].ID != 2 {
		t.Fatalf("IDs not preserved across reopen: %+v", persistedList)
	}
	// Novos IDs continuam depois do maior persistido, não voltam pra 1.
	nextProduct, _ := reopenedStore.Create("Cocoa", 5.0, "", 0)
	if nextProduct.ID != 3 {
		t.Fatalf("expected next ID to be 3, got %d", nextProduct.ID)
	}
}

func TestHandlerListAllReturnsCatalogue(t *testing.T) {
	router, productStore := buildProductRouter(t)
	_, _ = productStore.Create("Coffee", 9.99, "Arabica", 0)

	recordedResponse := performJSONProductRequest(router, http.MethodGet, "/products", nil, "")
	if recordedResponse.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recordedResponse.Code)
	}
	var listed []ProductRecord
	_ = json.Unmarshal(recordedResponse.Body.Bytes(), &listed)
	if len(listed) != 1 || listed[0].Name != "Coffee" {
		t.Fatalf("unexpected list response: %+v", listed)
	}
}

func TestHandlerGetByIDReturns404WhenMissing(t *testing.T) {
	router, _ := buildProductRouter(t)
	recordedResponse := performJSONProductRequest(router, http.MethodGet, "/products/999", nil, "")
	if recordedResponse.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", recordedResponse.Code)
	}
}

func TestHandlerCreateRequiresAuthentication(t *testing.T) {
	router, _ := buildProductRouter(t)
	body := map[string]any{"name": "Coffee", "price": 9.99, "description": "Arabica"}
	recordedResponse := performJSONProductRequest(router, http.MethodPost, "/products", body, "")
	if recordedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", recordedResponse.Code)
	}
}

func TestHandlerCreateRejectsRegularUsers(t *testing.T) {
	router, _ := buildProductRouter(t)
	regularUserToken, _ := authentication.SignToken([]byte(testSigningSecretLiteral), 1, "alice@example.com", authentication.RoleUser, testTokenLifetime)
	body := map[string]any{"name": "Coffee", "price": 9.99, "description": "Arabica"}
	recordedResponse := performJSONProductRequest(router, http.MethodPost, "/products", body, regularUserToken)
	if recordedResponse.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", recordedResponse.Code)
	}
}

func TestHandlerCreateAllowsAdministrator(t *testing.T) {
	router, productStore := buildProductRouter(t)
	administratorToken, _ := authentication.SignToken([]byte(testSigningSecretLiteral), 1, "boss@example.com", authentication.RoleAdministrator, testTokenLifetime)
	body := map[string]any{"name": "Coffee", "price": 9.99, "description": "Arabica"}
	recordedResponse := performJSONProductRequest(router, http.MethodPost, "/products", body, administratorToken)
	if recordedResponse.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d (body=%s)", recordedResponse.Code, recordedResponse.Body.String())
	}
	if listLength := len(productStore.ListAll()); listLength != 1 {
		t.Fatalf("expected 1 product after create, got %d", listLength)
	}
}

func TestHandlerHealthEndpoint(t *testing.T) {
	router, _ := buildProductRouter(t)
	recordedResponse := performJSONProductRequest(router, http.MethodGet, "/health", nil, "")
	if recordedResponse.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recordedResponse.Code)
	}
}

func TestKillSwitchShortCircuitsHealth(t *testing.T) {
	productStore := openTemporaryProductStore(t)
	serviceKillSwitch := killswitch.New()
	router := BuildRouter(productStore, serviceKillSwitch, []byte(testSigningSecretLiteral))
	serviceKillSwitch.Toggle()

	request := httptest.NewRequest(http.MethodGet, "/health", nil)
	recordedResponse := httptest.NewRecorder()
	router.ServeHTTP(recordedResponse, request)

	if recordedResponse.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when killed, got %d", recordedResponse.Code)
	}
}
