package users

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/filipe-ms/distributed-ecommerce/internal/authentication"
	"github.com/filipe-ms/distributed-ecommerce/internal/killswitch"
)

const testSigningSecretLiteral = "users-test-secret"
const testTokenLifetime = time.Minute

func openTemporaryStore(t *testing.T) *Store {
	t.Helper()
	databaseFilePath := filepath.Join(t.TempDir(), "users.db")
	store, openError := OpenStore(databaseFilePath)
	if openError != nil {
		t.Fatalf("OpenStore failed: %v", openError)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func buildTestRouter(t *testing.T) (http.Handler, *Store) {
	t.Helper()
	userStore := openTemporaryStore(t)
	router := BuildRouter(userStore, killswitch.New(), []byte(testSigningSecretLiteral), testTokenLifetime)
	return router, userStore
}

func performJSONRequest(router http.Handler, method, path string, payload any, bearerToken string) *httptest.ResponseRecorder {
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

func TestRegistrationHappyPath(t *testing.T) {
	router, _ := buildTestRouter(t)
	body := map[string]string{
		"name":     "Alice",
		"email":    "alice@example.com",
		"password": "hunter2",
	}
	recordedResponse := performJSONRequest(router, http.MethodPost, "/users/register", body, "")

	if recordedResponse.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d (body=%s)", recordedResponse.Code, recordedResponse.Body.String())
	}
	var registered PublicUserView
	if decodeError := json.Unmarshal(recordedResponse.Body.Bytes(), &registered); decodeError != nil {
		t.Fatalf("response body was not a PublicUserView: %v", decodeError)
	}
	if registered.Email != "alice@example.com" || registered.Role != authentication.RoleUser {
		t.Fatalf("unexpected user: %+v", registered)
	}
}

func TestRegistrationRejectsDuplicateEmail(t *testing.T) {
	router, _ := buildTestRouter(t)
	body := map[string]string{
		"name":     "Alice",
		"email":    "alice@example.com",
		"password": "hunter2",
	}
	performJSONRequest(router, http.MethodPost, "/users/register", body, "")
	secondAttempt := performJSONRequest(router, http.MethodPost, "/users/register", body, "")
	if secondAttempt.Code != http.StatusConflict {
		t.Fatalf("expected 409 on duplicate email, got %d", secondAttempt.Code)
	}
}

func TestRegistrationRejectsShortPassword(t *testing.T) {
	router, _ := buildTestRouter(t)
	body := map[string]string{
		"name":     "Alice",
		"email":    "alice@example.com",
		"password": "abc",
	}
	recordedResponse := performJSONRequest(router, http.MethodPost, "/users/register", body, "")
	if recordedResponse.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on short password, got %d", recordedResponse.Code)
	}
}

func TestLoginHappyPath(t *testing.T) {
	router, _ := buildTestRouter(t)
	registrationBody := map[string]string{
		"name":     "Bob",
		"email":    "bob@example.com",
		"password": "hunter2",
	}
	performJSONRequest(router, http.MethodPost, "/users/register", registrationBody, "")

	loginBody := map[string]string{"email": "bob@example.com", "password": "hunter2"}
	recordedResponse := performJSONRequest(router, http.MethodPost, "/users/login", loginBody, "")
	if recordedResponse.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body=%s)", recordedResponse.Code, recordedResponse.Body.String())
	}
	var loginResponse loginResponsePayload
	if decodeError := json.Unmarshal(recordedResponse.Body.Bytes(), &loginResponse); decodeError != nil {
		t.Fatalf("response was not a login payload: %v", decodeError)
	}
	if loginResponse.Token == "" {
		t.Fatal("login response did not contain a token")
	}
	if !strings.Contains(loginResponse.Token, ".") {
		t.Fatalf("token does not look like a JWT: %q", loginResponse.Token)
	}
}

func TestLoginRejectsWrongPassword(t *testing.T) {
	router, _ := buildTestRouter(t)
	performJSONRequest(router, http.MethodPost, "/users/register", map[string]string{
		"name": "Bob", "email": "bob@example.com", "password": "hunter2",
	}, "")
	recordedResponse := performJSONRequest(router, http.MethodPost, "/users/login", map[string]string{
		"email": "bob@example.com", "password": "wrong",
	}, "")
	if recordedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", recordedResponse.Code)
	}
}

func TestLoginRejectsUnknownEmail(t *testing.T) {
	router, _ := buildTestRouter(t)
	recordedResponse := performJSONRequest(router, http.MethodPost, "/users/login", map[string]string{
		"email": "ghost@example.com", "password": "anything",
	}, "")
	if recordedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", recordedResponse.Code)
	}
}

func TestGetByIDRequiresAuthentication(t *testing.T) {
	router, _ := buildTestRouter(t)
	recordedResponse := performJSONRequest(router, http.MethodGet, "/users/1", nil, "")
	if recordedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", recordedResponse.Code)
	}
}

func TestGetByIDAllowsOwnerAndBlocksOthers(t *testing.T) {
	router, userStore := buildTestRouter(t)

	createdAlice, createError := userStore.CreateUser(context.Background(), "Alice", "alice@example.com", "hunter2", authentication.RoleUser)
	if createError != nil {
		t.Fatalf("could not create Alice: %v", createError)
	}
	createdBob, createError := userStore.CreateUser(context.Background(), "Bob", "bob@example.com", "hunter2", authentication.RoleUser)
	if createError != nil {
		t.Fatalf("could not create Bob: %v", createError)
	}

	tokenForAlice, _ := authentication.SignToken([]byte(testSigningSecretLiteral), createdAlice.ID, createdAlice.Email, authentication.RoleUser, testTokenLifetime)

	ownProfileResponse := performJSONRequest(router, http.MethodGet, "/users/"+stringFromInt(createdAlice.ID), nil, tokenForAlice)
	if ownProfileResponse.Code != http.StatusOK {
		t.Fatalf("expected 200 for own profile, got %d", ownProfileResponse.Code)
	}

	otherProfileResponse := performJSONRequest(router, http.MethodGet, "/users/"+stringFromInt(createdBob.ID), nil, tokenForAlice)
	if otherProfileResponse.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for other user's profile, got %d", otherProfileResponse.Code)
	}
}

func TestGetByIDAllowsAdministratorEverywhere(t *testing.T) {
	router, userStore := buildTestRouter(t)

	createdAdministrator, _ := userStore.CreateUser(context.Background(), "Boss", "boss@example.com", "hunter2", authentication.RoleAdministrator)
	createdAlice, _ := userStore.CreateUser(context.Background(), "Alice", "alice@example.com", "hunter2", authentication.RoleUser)

	tokenForAdministrator, _ := authentication.SignToken([]byte(testSigningSecretLiteral), createdAdministrator.ID, createdAdministrator.Email, authentication.RoleAdministrator, testTokenLifetime)
	recordedResponse := performJSONRequest(router, http.MethodGet, "/users/"+stringFromInt(createdAlice.ID), nil, tokenForAdministrator)
	if recordedResponse.Code != http.StatusOK {
		t.Fatalf("administrator should be allowed to fetch another profile, got %d", recordedResponse.Code)
	}
}

func TestSeedDefaultAdministratorOnlyOnce(t *testing.T) {
	store := openTemporaryStore(t)
	if seedError := store.SeedDefaultAdministratorIfEmpty(context.Background(), "admin@local", "admin123"); seedError != nil {
		t.Fatalf("first seed failed: %v", seedError)
	}
	if seedError := store.SeedDefaultAdministratorIfEmpty(context.Background(), "admin@local", "admin123"); seedError != nil {
		t.Fatalf("second seed should be a no-op, got %v", seedError)
	}
	count, _ := store.CountUsers(context.Background())
	if count != 1 {
		t.Fatalf("expected exactly one user after two seed attempts, got %d", count)
	}
}

func TestStoreCreateUserSurfacesUniqueViolation(t *testing.T) {
	store := openTemporaryStore(t)
	_, firstError := store.CreateUser(context.Background(), "A", "a@example.com", "hunter2", authentication.RoleUser)
	if firstError != nil {
		t.Fatalf("first insert failed: %v", firstError)
	}
	_, secondError := store.CreateUser(context.Background(), "A", "a@example.com", "hunter2", authentication.RoleUser)
	if !errors.Is(secondError, ErrEmailAlreadyRegistered) {
		t.Fatalf("expected ErrEmailAlreadyRegistered, got %v", secondError)
	}
}

func stringFromInt(value int) string {
	return strconv.Itoa(value)
}
