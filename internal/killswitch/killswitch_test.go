package killswitch

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewSwitchStartsHealthy(t *testing.T) {
	killSwitch := New()
	if killSwitch.IsKilled() {
		t.Fatal("a freshly constructed switch should not start in the killed state")
	}
}

func TestToggleFlipsAndReturnsNewValue(t *testing.T) {
	killSwitch := New()

	if newValue := killSwitch.Toggle(); newValue != true {
		t.Fatalf("first toggle should return true, got %v", newValue)
	}
	if !killSwitch.IsKilled() {
		t.Fatal("IsKilled should report true after first toggle")
	}
	if newValue := killSwitch.Toggle(); newValue != false {
		t.Fatalf("second toggle should return false, got %v", newValue)
	}
	if killSwitch.IsKilled() {
		t.Fatal("IsKilled should report false after second toggle")
	}
}

func TestMiddlewarePassesThroughWhenHealthy(t *testing.T) {
	killSwitch := New()
	innerCalled := false
	innerHandler := http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		innerCalled = true
		responseWriter.WriteHeader(http.StatusOK)
	})

	wrappedHandler := killSwitch.Middleware(innerHandler)

	request := httptest.NewRequest(http.MethodGet, "/health", nil)
	recordedResponse := httptest.NewRecorder()
	wrappedHandler.ServeHTTP(recordedResponse, request)

	if !innerCalled {
		t.Fatal("inner handler should run when switch is off")
	}
	if recordedResponse.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recordedResponse.Code)
	}
}

func TestMiddlewareReturnsServiceUnavailableWhenKilled(t *testing.T) {
	killSwitch := New()
	killSwitch.Toggle()

	innerHandler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("inner handler must not run when the switch is engaged")
	})
	wrappedHandler := killSwitch.Middleware(innerHandler)

	request := httptest.NewRequest(http.MethodGet, "/health", nil)
	recordedResponse := httptest.NewRecorder()
	wrappedHandler.ServeHTTP(recordedResponse, request)

	if recordedResponse.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", recordedResponse.Code)
	}
	if !strings.Contains(recordedResponse.Body.String(), "outage") {
		t.Fatalf("body should mention outage, got %q", recordedResponse.Body.String())
	}
}

func TestMiddlewareKeepsTogglePathReachableWhenKilled(t *testing.T) {
	killSwitch := New()
	killSwitch.Toggle()

	innerCalled := false
	innerHandler := http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		innerCalled = true
		responseWriter.WriteHeader(http.StatusOK)
	})
	wrappedHandler := killSwitch.Middleware(innerHandler)

	request := httptest.NewRequest(http.MethodPost, "/admin/toggle", nil)
	recordedResponse := httptest.NewRecorder()
	wrappedHandler.ServeHTTP(recordedResponse, request)

	if !innerCalled {
		t.Fatal("middleware should still let POST /admin/toggle reach the handler when killed")
	}
	if recordedResponse.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recordedResponse.Code)
	}
}

func TestToggleHandlerReturnsCurrentState(t *testing.T) {
	killSwitch := New()
	request := httptest.NewRequest(http.MethodPost, "/admin/toggle", nil)
	recordedResponse := httptest.NewRecorder()

	killSwitch.ToggleHandler(recordedResponse, request)

	if recordedResponse.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recordedResponse.Code)
	}
	body := recordedResponse.Body.String()
	if !strings.Contains(body, `"killed":true`) {
		t.Fatalf("expected killed=true after toggling on, got body %q", body)
	}
}
