package orders

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/filipe-ms/distributed-ecommerce/internal/authentication"
	"github.com/filipe-ms/distributed-ecommerce/internal/killswitch"
)

const testSigningSecretLiteral = "orders-test-secret"
const testTokenLifetime = time.Minute

func openTemporaryOrderStore(t *testing.T) *Store {
	t.Helper()
	databaseFilePath := filepath.Join(t.TempDir(), "orders.db")
	orderStore, openError := OpenStore(databaseFilePath)
	if openError != nil {
		t.Fatalf("OpenStore failed: %v", openError)
	}
	t.Cleanup(func() { _ = orderStore.Close() })
	return orderStore
}

func buildOrdersRouter(t *testing.T) (http.Handler, *Store) {
	t.Helper()
	orderStore := openTemporaryOrderStore(t)
	router := BuildRouter(orderStore, killswitch.New(), []byte(testSigningSecretLiteral))
	return router, orderStore
}

func performJSONOrderRequest(router http.Handler, method, path string, payload any, bearerToken string) *httptest.ResponseRecorder {
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

func TestStoreCreateAndListByUserIsolatesUsers(t *testing.T) {
	orderStore := openTemporaryOrderStore(t)
	_, _ = orderStore.Create(context.Background(), 1, 100)
	_, _ = orderStore.Create(context.Background(), 1, 101)
	_, _ = orderStore.Create(context.Background(), 2, 200)

	listForUserOne, _ := orderStore.ListByUserID(context.Background(), 1)
	listForUserTwo, _ := orderStore.ListByUserID(context.Background(), 2)

	if len(listForUserOne) != 2 {
		t.Fatalf("expected 2 orders for user 1, got %d", len(listForUserOne))
	}
	if len(listForUserTwo) != 1 {
		t.Fatalf("expected 1 order for user 2, got %d", len(listForUserTwo))
	}
	if listForUserOne[0].ID >= listForUserOne[1].ID {
		t.Fatalf("expected ascending IDs, got %d then %d", listForUserOne[0].ID, listForUserOne[1].ID)
	}
}

func TestStoreCreateRejectsNonPositiveIDs(t *testing.T) {
	orderStore := openTemporaryOrderStore(t)
	if _, createError := orderStore.Create(context.Background(), 0, 1); createError == nil {
		t.Fatal("expected an error for zero user id")
	}
	if _, createError := orderStore.Create(context.Background(), 1, -5); createError == nil {
		t.Fatal("expected an error for negative product id")
	}
}

func TestPlaceOrderRequiresAuthentication(t *testing.T) {
	router, _ := buildOrdersRouter(t)
	body := map[string]int{"productId": 1}
	recordedResponse := performJSONOrderRequest(router, http.MethodPost, "/orders", body, "")
	if recordedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", recordedResponse.Code)
	}
}

func TestPlaceOrderHappyPathDerivesUserFromToken(t *testing.T) {
	router, orderStore := buildOrdersRouter(t)
	bearerToken, _ := authentication.SignToken([]byte(testSigningSecretLiteral), 42, "alice@example.com", authentication.RoleUser, testTokenLifetime)
	body := map[string]int{"productId": 7}

	recordedResponse := performJSONOrderRequest(router, http.MethodPost, "/orders", body, bearerToken)
	if recordedResponse.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d (body=%s)", recordedResponse.Code, recordedResponse.Body.String())
	}

	persistedOrders, _ := orderStore.ListByUserID(context.Background(), 42)
	if len(persistedOrders) != 1 {
		t.Fatalf("expected 1 persisted order for user 42, got %d", len(persistedOrders))
	}
	if persistedOrders[0].ProductID != 7 {
		t.Fatalf("unexpected product id persisted: %d", persistedOrders[0].ProductID)
	}
}

func TestPlaceOrderRejectsMissingProductId(t *testing.T) {
	router, _ := buildOrdersRouter(t)
	bearerToken, _ := authentication.SignToken([]byte(testSigningSecretLiteral), 42, "alice@example.com", authentication.RoleUser, testTokenLifetime)
	body := map[string]int{"productId": 0}

	recordedResponse := performJSONOrderRequest(router, http.MethodPost, "/orders", body, bearerToken)
	if recordedResponse.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", recordedResponse.Code)
	}
}

func TestListOrdersAllowsOwnerAndBlocksOthers(t *testing.T) {
	router, orderStore := buildOrdersRouter(t)
	_, _ = orderStore.Create(context.Background(), 1, 100)
	_, _ = orderStore.Create(context.Background(), 2, 200)

	tokenForUserOne, _ := authentication.SignToken([]byte(testSigningSecretLiteral), 1, "u1@example.com", authentication.RoleUser, testTokenLifetime)
	ownResponse := performJSONOrderRequest(router, http.MethodGet, "/orders/1", nil, tokenForUserOne)
	if ownResponse.Code != http.StatusOK {
		t.Fatalf("expected 200 for own list, got %d", ownResponse.Code)
	}

	otherResponse := performJSONOrderRequest(router, http.MethodGet, "/orders/2", nil, tokenForUserOne)
	if otherResponse.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for other user's list, got %d", otherResponse.Code)
	}
}

func TestListOrdersAllowsAdministratorAcrossUsers(t *testing.T) {
	router, orderStore := buildOrdersRouter(t)
	_, _ = orderStore.Create(context.Background(), 7, 100)

	administratorToken, _ := authentication.SignToken([]byte(testSigningSecretLiteral), 1, "boss@example.com", authentication.RoleAdministrator, testTokenLifetime)
	recordedResponse := performJSONOrderRequest(router, http.MethodGet, "/orders/7", nil, administratorToken)
	if recordedResponse.Code != http.StatusOK {
		t.Fatalf("administrator should see anyone's orders, got %d", recordedResponse.Code)
	}

	var listed []OrderRecord
	_ = json.Unmarshal(recordedResponse.Body.Bytes(), &listed)
	if len(listed) != 1 {
		t.Fatalf("expected 1 order in the response, got %d", len(listed))
	}
}

func TestListOrdersInvalidUserID(t *testing.T) {
	router, _ := buildOrdersRouter(t)
	bearerToken, _ := authentication.SignToken([]byte(testSigningSecretLiteral), 1, "u1@example.com", authentication.RoleAdministrator, testTokenLifetime)
	recordedResponse := performJSONOrderRequest(router, http.MethodGet, "/orders/"+strconv.Itoa(-1), nil, bearerToken)
	if recordedResponse.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid user id, got %d", recordedResponse.Code)
	}
}
