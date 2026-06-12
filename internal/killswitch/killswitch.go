// Package killswitch implements the in-process flag every backing service
// uses to simulate an outage. The dashboard "Kill" button toggles the flag;
// while the flag is on, the middleware short-circuits every request — even
// /health — so the gateway's heartbeat detects the outage through exactly the
// same code path it would in a real failure.
package killswitch

import (
	"net/http"
	"sync/atomic"

	"github.com/filipe-ms/distributed-ecommerce/internal/httpjson"
)

// togglePath is the only endpoint that remains reachable while the kill
// switch is active. Anything else returns 503. We keep it as a constant so
// the middleware does not have to know about routing details.
const togglePath = "/admin/toggle"

// Switch holds a single atomic boolean. atomic.Bool gives us lock-free reads
// from the request path; the mutating operation (Toggle) only happens from
// the dashboard, so contention is essentially zero.
type Switch struct {
	isCurrentlyKilled atomic.Bool
}

// New constructs an unset Switch (the service starts in a healthy state).
func New() *Switch {
	return &Switch{}
}

// IsKilled reports the current state. Useful in tests and in the dashboard
// status response.
func (killSwitch *Switch) IsKilled() bool {
	return killSwitch.isCurrentlyKilled.Load()
}

// Toggle flips the flag and returns the new value. Implemented as a
// CAS-loop so concurrent toggles cannot race past each other.
func (killSwitch *Switch) Toggle() bool {
	for {
		previousValue := killSwitch.isCurrentlyKilled.Load()
		if killSwitch.isCurrentlyKilled.CompareAndSwap(previousValue, !previousValue) {
			return !previousValue
		}
	}
}

// Middleware short-circuits every incoming request with HTTP 503 while the
// switch is engaged, except for POST /admin/toggle so the dashboard always
// has a way back to a healthy state.
func (killSwitch *Switch) Middleware(nextHandler http.Handler) http.Handler {
	return http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		if killSwitch.IsKilled() && !(request.URL.Path == togglePath && request.Method == http.MethodPost) {
			httpjson.WriteError(responseWriter, http.StatusServiceUnavailable, "service is currently simulating an outage")
			return
		}
		nextHandler.ServeHTTP(responseWriter, request)
	})
}

// ToggleHandler is the HTTP handler for POST /admin/toggle. It flips the
// switch and returns the new state as JSON so the dashboard can update its
// per-service indicator without polling.
func (killSwitch *Switch) ToggleHandler(responseWriter http.ResponseWriter, _ *http.Request) {
	newKilledState := killSwitch.Toggle()
	httpjson.WriteJSON(responseWriter, http.StatusOK, map[string]bool{"killed": newKilledState})
}
