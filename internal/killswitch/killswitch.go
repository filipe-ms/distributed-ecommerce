// Package killswitch implementa a flag em memória que cada serviço
// usa pra simular uma queda. O botão "Kill" do dashboard liga a flag;
// enquanto ela tá ligada o middleware curto-circuita as requests
// (até o /health), então o heartbeat do gateway detecta a queda
// pelo mesmo caminho de uma falha real.
package killswitch

import (
	"net/http"
	"sync/atomic"

	"github.com/filipe-ms/distributed-ecommerce/internal/httpjson"
)

// togglePath é a única rota que continua respondendo enquanto o
// kill switch tá ligado. Tudo o mais devolve 503.
const togglePath = "/admin/toggle"

// Switch guarda um único booleano atômico. atomic.Bool dá leitura sem
// lock no caminho da request; a escrita só acontece quando o
// dashboard manda toggle, então praticamente não tem contenção.
type Switch struct {
	isCurrentlyKilled        atomic.Bool
	afterEngageCallback      func()
}

// New cria um Switch desligado (serviço começa saudável).
func New() *Switch {
	return &Switch{}
}

// SetAfterEngageCallback registra uma função que roda em uma goroutine
// depois que o switch é ligado. Em produção a gente usa pra agendar o
// shutdown do processo (depois que a resposta saiu pelo gateway), e o
// docker-compose reinicia o container. Em testes pode ficar nil.
func (killSwitch *Switch) SetAfterEngageCallback(callback func()) {
	killSwitch.afterEngageCallback = callback
}

// IsKilled diz se o switch tá ligado.
func (killSwitch *Switch) IsKilled() bool {
	return killSwitch.isCurrentlyKilled.Load()
}

// Toggle inverte a flag e devolve o novo valor. Loop com CAS pra dois
// toggles simultâneos não passarem um pelo outro.
func (killSwitch *Switch) Toggle() bool {
	for {
		previousValue := killSwitch.isCurrentlyKilled.Load()
		if killSwitch.isCurrentlyKilled.CompareAndSwap(previousValue, !previousValue) {
			return !previousValue
		}
	}
}

// Middleware curto-circuita toda request com 503 enquanto o switch
// tá ligado, com exceção do POST /admin/toggle pra sempre ter como
// voltar atrás.
func (killSwitch *Switch) Middleware(nextHandler http.Handler) http.Handler {
	return http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		if killSwitch.IsKilled() && !(request.URL.Path == togglePath && request.Method == http.MethodPost) {
			httpjson.WriteError(responseWriter, http.StatusServiceUnavailable, "service is currently simulating an outage")
			return
		}
		nextHandler.ServeHTTP(responseWriter, request)
	})
}

// ToggleHandler é o handler do POST /admin/toggle. Inverte a flag e
// devolve o novo estado em JSON. Se a flag virou pra "killed" e
// existe um callback registrado, ele é disparado em uma goroutine
// depois do flush, pra resposta sair antes do shutdown.
func (killSwitch *Switch) ToggleHandler(responseWriter http.ResponseWriter, _ *http.Request) {
	newKilledState := killSwitch.Toggle()
	httpjson.WriteJSON(responseWriter, http.StatusOK, map[string]bool{"killed": newKilledState})
	if newKilledState && killSwitch.afterEngageCallback != nil {
		if flusher, ok := responseWriter.(http.Flusher); ok {
			flusher.Flush()
		}
		go killSwitch.afterEngageCallback()
	}
}
