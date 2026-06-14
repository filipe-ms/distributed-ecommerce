package gateway

import (
	"context"
	"testing"
)

// testContext devolve um context vinculado ao tempo de vida do teste.
// Usado pelos testes de heartbeat pra nunca vazar goroutines depois
// que o binário de teste terminar.
func testContext(t *testing.T) context.Context {
	t.Helper()
	rootContext, cancelRootContext := context.WithCancel(context.Background())
	t.Cleanup(cancelRootContext)
	return rootContext
}
