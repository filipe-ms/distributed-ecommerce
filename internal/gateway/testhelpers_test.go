package gateway

import (
	"context"
	"testing"
)

// testContext returns a background context tied to the test's lifetime. It
// is used by the heartbeat tests so they never leak goroutines past the
// test binary's exit.
func testContext(t *testing.T) context.Context {
	t.Helper()
	rootContext, cancelRootContext := context.WithCancel(context.Background())
	t.Cleanup(cancelRootContext)
	return rootContext
}
