package authentication

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testSigningSecretLiteral = "test-secret-do-not-use-in-production"

func testSigningSecret() []byte { return []byte(testSigningSecretLiteral) }

func TestPasswordHashRoundtrip(t *testing.T) {
	hashed, hashError := HashPassword("hunter2")
	if hashError != nil {
		t.Fatalf("HashPassword failed: %v", hashError)
	}
	if hashed == "hunter2" {
		t.Fatalf("HashPassword returned plaintext")
	}
	if verifyError := VerifyPassword("hunter2", hashed); verifyError != nil {
		t.Fatalf("VerifyPassword rejected the correct password: %v", verifyError)
	}
}

func TestVerifyPasswordRejectsWrongPassword(t *testing.T) {
	hashed, _ := HashPassword("correct-horse")
	if verifyError := VerifyPassword("battery-staple", hashed); verifyError == nil {
		t.Fatal("VerifyPassword accepted a wrong password")
	}
}

func TestSignAndVerifyTokenRoundtrip(t *testing.T) {
	signed, signError := SignToken(testSigningSecret(), 42, "alice@example.com", RoleAdministrator, time.Minute)
	if signError != nil {
		t.Fatalf("SignToken failed: %v", signError)
	}

	parsedClaims, verifyError := VerifyToken(testSigningSecret(), signed)
	if verifyError != nil {
		t.Fatalf("VerifyToken failed: %v", verifyError)
	}
	if parsedClaims.UserID != 42 {
		t.Fatalf("UserID mismatch: got %d", parsedClaims.UserID)
	}
	if parsedClaims.Email != "alice@example.com" {
		t.Fatalf("Email mismatch: got %q", parsedClaims.Email)
	}
	if parsedClaims.Role != RoleAdministrator {
		t.Fatalf("Role mismatch: got %q", parsedClaims.Role)
	}
}

func TestVerifyTokenRejectsTokenSignedWithDifferentSecret(t *testing.T) {
	signed, _ := SignToken([]byte("secret-A"), 1, "alice@example.com", RoleUser, time.Minute)
	if _, verifyError := VerifyToken([]byte("secret-B"), signed); verifyError == nil {
		t.Fatal("VerifyToken accepted a token signed with a different secret")
	}
}

func TestVerifyTokenRejectsExpiredToken(t *testing.T) {
	signed, _ := SignToken(testSigningSecret(), 1, "alice@example.com", RoleUser, -time.Minute)
	if _, verifyError := VerifyToken(testSigningSecret(), signed); verifyError == nil {
		t.Fatal("VerifyToken accepted a token whose exp is in the past")
	}
}

func TestRequireValidTokenAttachesClaimsAndAllowsRequest(t *testing.T) {
	signed, _ := SignToken(testSigningSecret(), 7, "bob@example.com", RoleUser, time.Minute)

	var observedClaims *Claims
	innerHandler := http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		observedClaims = ClaimsFromContext(request.Context())
		responseWriter.WriteHeader(http.StatusOK)
	})

	wrappedHandler := RequireValidToken(testSigningSecret())(innerHandler)

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Authorization", "Bearer "+signed)
	recordedResponse := httptest.NewRecorder()

	wrappedHandler.ServeHTTP(recordedResponse, request)

	if recordedResponse.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body=%s)", recordedResponse.Code, recordedResponse.Body.String())
	}
	if observedClaims == nil {
		t.Fatal("middleware did not attach claims to context")
	}
	if observedClaims.UserID != 7 {
		t.Fatalf("UserID mismatch: got %d", observedClaims.UserID)
	}
}

func TestRequireValidTokenRejectsMissingAuthorizationHeader(t *testing.T) {
	wrappedHandler := RequireValidToken(testSigningSecret())(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	recordedResponse := httptest.NewRecorder()
	wrappedHandler.ServeHTTP(recordedResponse, request)

	if recordedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", recordedResponse.Code)
	}
}

func TestRequireValidTokenRejectsBearerWithGarbage(t *testing.T) {
	wrappedHandler := RequireValidToken(testSigningSecret())(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Authorization", "Bearer not-a-real-token")
	recordedResponse := httptest.NewRecorder()
	wrappedHandler.ServeHTTP(recordedResponse, request)

	if recordedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", recordedResponse.Code)
	}
}

func TestRequireAdministratorRoleAllowsAdministrators(t *testing.T) {
	signed, _ := SignToken(testSigningSecret(), 1, "boss@example.com", RoleAdministrator, time.Minute)

	innerCalled := false
	innerHandler := http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		innerCalled = true
		responseWriter.WriteHeader(http.StatusNoContent)
	})

	stack := RequireValidToken(testSigningSecret())(RequireAdministratorRole(innerHandler))

	request := httptest.NewRequest(http.MethodPost, "/products", nil)
	request.Header.Set("Authorization", "Bearer "+signed)
	recordedResponse := httptest.NewRecorder()
	stack.ServeHTTP(recordedResponse, request)

	if !innerCalled {
		t.Fatal("inner handler was not invoked for an administrator")
	}
	if recordedResponse.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", recordedResponse.Code)
	}
}

func TestRequireAdministratorRoleBlocksRegularUsers(t *testing.T) {
	signed, _ := SignToken(testSigningSecret(), 1, "alice@example.com", RoleUser, time.Minute)

	innerHandler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("inner handler must not run for regular users")
	})

	stack := RequireValidToken(testSigningSecret())(RequireAdministratorRole(innerHandler))

	request := httptest.NewRequest(http.MethodPost, "/products", nil)
	request.Header.Set("Authorization", "Bearer "+signed)
	recordedResponse := httptest.NewRecorder()
	stack.ServeHTTP(recordedResponse, request)

	if recordedResponse.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", recordedResponse.Code)
	}
	if !strings.Contains(recordedResponse.Body.String(), "administrator") {
		t.Fatalf("error body should mention administrator role, got %q", recordedResponse.Body.String())
	}
}
