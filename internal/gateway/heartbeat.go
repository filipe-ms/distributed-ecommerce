package gateway

import (
	"context"
	"log/slog"
	"net/http"
	"sort"
	"sync"
	"time"
)

// Tempo entre cada rodada de heartbeat. O enunciado pede 5 segundos.
const HeartbeatPollInterval = 5 * time.Second

// Quantas falhas seguidas pra marcar o serviço como DOWN. O enunciado
// fala em "até 2 tentativas".
const FailuresBeforeMarkingDown = 2

// Tempo máximo de cada probe individual. Mantemos abaixo do intervalo
// pra um serviço travado não atrasar os outros.
const healthProbeTimeout = 2 * time.Second

// MonitoredService descreve um serviço que o gateway vai vigiar.
type MonitoredService struct {
	Name    string
	BaseURL string
}

// ServiceStatusSnapshot é a visão somente-leitura de um serviço, usada
// pelo dashboard pra montar o quadro de status.
type ServiceStatusSnapshot struct {
	Name                 string    `json:"name"`
	BaseURL              string    `json:"baseUrl"`
	IsAvailable          bool      `json:"available"`
	ConsecutiveFailures  int       `json:"consecutiveFailures"`
	LastTransitionAt     time.Time `json:"lastTransitionAt"`
	LastSuccessfulProbe  time.Time `json:"lastSuccessfulProbe"`
}

// monitoredServiceState é o que o gateway guarda internamente sobre
// cada serviço.
type monitoredServiceState struct {
	configuration       MonitoredService
	consecutiveFailures int
	isAvailable         bool
	lastTransitionAt    time.Time
	lastSuccessfulProbe time.Time
}

// HeartbeatRegistry roda o loop de heartbeat e expõe métodos thread-safe
// pro proxy e pro dashboard lerem o status sem travar o loop.
type HeartbeatRegistry struct {
	mutex                  sync.RWMutex
	servicesByName         map[string]*monitoredServiceState
	logger                 *slog.Logger
	internalHTTPClient     *http.Client
	pollInterval           time.Duration
	failureThreshold       int
	eventRing              *EventRing
}

// NewHeartbeatRegistry cria o registry já com uma entrada por serviço
// monitorado. Todos começam marcados como disponíveis; a primeira
// rodada de probes confirma ou não.
func NewHeartbeatRegistry(monitoredServices []MonitoredService, internalHTTPClient *http.Client, logger *slog.Logger, eventRing *EventRing) *HeartbeatRegistry {
	servicesByName := make(map[string]*monitoredServiceState, len(monitoredServices))
	for _, current := range monitoredServices {
		servicesByName[current.Name] = &monitoredServiceState{
			configuration:    current,
			isAvailable:      true,
			lastTransitionAt: time.Time{},
		}
	}
	return &HeartbeatRegistry{
		servicesByName:     servicesByName,
		logger:             logger,
		internalHTTPClient: internalHTTPClient,
		pollInterval:       HeartbeatPollInterval,
		failureThreshold:   FailuresBeforeMarkingDown,
		eventRing:          eventRing,
	}
}

// SetPollInterval permite que os testes acelerem o loop. Tem que ser
// chamado antes do Run.
func (registry *HeartbeatRegistry) SetPollInterval(interval time.Duration) {
	registry.pollInterval = interval
}

// SetFailureThreshold também é só pros testes, pra exercitar o caminho
// de recuperação com um threshold diferente.
func (registry *HeartbeatRegistry) SetFailureThreshold(threshold int) {
	if threshold < 1 {
		threshold = 1
	}
	registry.failureThreshold = threshold
}

// Run trava na função fazendo um probe de cada serviço a cada tick,
// até o context ser cancelado. Probes saem em paralelo pra um serviço
// lento não atrasar os outros.
func (registry *HeartbeatRegistry) Run(loopContext context.Context) {
	ticker := time.NewTicker(registry.pollInterval)
	defer ticker.Stop()

	registry.tickOnce(loopContext) // primeira rodada já no início

	for {
		select {
		case <-loopContext.Done():
			return
		case <-ticker.C:
			registry.tickOnce(loopContext)
		}
	}
}

func (registry *HeartbeatRegistry) tickOnce(loopContext context.Context) {
	servicesSnapshot := registry.snapshotKeys()
	var probeWaitGroup sync.WaitGroup

	for _, serviceName := range servicesSnapshot {
		probeWaitGroup.Add(1)
		go func(currentServiceName string) {
			defer probeWaitGroup.Done()
			registry.runOneProbe(loopContext, currentServiceName)
		}(serviceName)
	}
	probeWaitGroup.Wait()
}

func (registry *HeartbeatRegistry) snapshotKeys() []string {
	registry.mutex.RLock()
	defer registry.mutex.RUnlock()
	keys := make([]string, 0, len(registry.servicesByName))
	for name := range registry.servicesByName {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	return keys
}

func (registry *HeartbeatRegistry) runOneProbe(loopContext context.Context, serviceName string) {
	registry.mutex.RLock()
	state, ok := registry.servicesByName[serviceName]
	if !ok {
		registry.mutex.RUnlock()
		return
	}
	probeURL := state.configuration.BaseURL + "/health"
	registry.mutex.RUnlock()

	probeContext, cancelProbeContext := context.WithTimeout(loopContext, healthProbeTimeout)
	defer cancelProbeContext()

	probeError := registry.performSingleProbe(probeContext, probeURL)
	registry.recordProbeOutcome(serviceName, probeError == nil, probeError)
}

func (registry *HeartbeatRegistry) performSingleProbe(probeContext context.Context, probeURL string) error {
	probeRequest, buildError := http.NewRequestWithContext(probeContext, http.MethodGet, probeURL, nil)
	if buildError != nil {
		return buildError
	}
	probeResponse, sendError := registry.internalHTTPClient.Do(probeRequest)
	if sendError != nil {
		return sendError
	}
	defer func() { _ = probeResponse.Body.Close() }()

	if probeResponse.StatusCode < 200 || probeResponse.StatusCode >= 300 {
		return &probeStatusError{StatusCode: probeResponse.StatusCode}
	}
	return nil
}

type probeStatusError struct{ StatusCode int }

func (failure *probeStatusError) Error() string {
	return http.StatusText(failure.StatusCode)
}

// recordProbeOutcome atualiza os contadores e, quando o serviço muda
// de estado, registra log e empurra o evento pro ring buffer.
func (registry *HeartbeatRegistry) recordProbeOutcome(serviceName string, probeSucceeded bool, probeError error) {
	registry.mutex.Lock()
	defer registry.mutex.Unlock()

	state, ok := registry.servicesByName[serviceName]
	if !ok {
		return
	}

	now := time.Now().UTC()

	if probeSucceeded {
		state.consecutiveFailures = 0
		state.lastSuccessfulProbe = now
		if !state.isAvailable {
			state.isAvailable = true
			state.lastTransitionAt = now
			registry.recordEventLocked(Event{OccurredAt: now, ServiceName: serviceName, Kind: EventKindServiceRecovered})
			if registry.logger != nil {
				registry.logger.Info("service recovered", "service", serviceName)
			}
		}
		return
	}

	state.consecutiveFailures++
	if state.isAvailable && state.consecutiveFailures >= registry.failureThreshold {
		state.isAvailable = false
		state.lastTransitionAt = now
		registry.recordEventLocked(Event{OccurredAt: now, ServiceName: serviceName, Kind: EventKindServiceDown})
		if registry.logger != nil {
			registry.logger.Warn("service marked down",
				"service", serviceName,
				"consecutive_failures", state.consecutiveFailures,
				"error", probeError)
		}
	}
}

func (registry *HeartbeatRegistry) recordEventLocked(newEvent Event) {
	if registry.eventRing != nil {
		registry.eventRing.Push(newEvent)
	}
}

// IsAvailable diz se o serviço está disponível agora. Se o nome não
// existir, devolve true (o proxy nunca bloqueia tráfego sem motivo).
func (registry *HeartbeatRegistry) IsAvailable(serviceName string) bool {
	registry.mutex.RLock()
	defer registry.mutex.RUnlock()
	state, ok := registry.servicesByName[serviceName]
	if !ok {
		return true
	}
	return state.isAvailable
}

// Snapshot devolve uma cópia do estado atual de todos os serviços, em
// ordem alfabética. Usado pelo dashboard.
func (registry *HeartbeatRegistry) Snapshot() []ServiceStatusSnapshot {
	registry.mutex.RLock()
	defer registry.mutex.RUnlock()
	output := make([]ServiceStatusSnapshot, 0, len(registry.servicesByName))
	for _, state := range registry.servicesByName {
		output = append(output, ServiceStatusSnapshot{
			Name:                state.configuration.Name,
			BaseURL:             state.configuration.BaseURL,
			IsAvailable:         state.isAvailable,
			ConsecutiveFailures: state.consecutiveFailures,
			LastTransitionAt:    state.lastTransitionAt,
			LastSuccessfulProbe: state.lastSuccessfulProbe,
		})
	}
	sort.Slice(output, func(leftIndex, rightIndex int) bool {
		return output[leftIndex].Name < output[rightIndex].Name
	})
	return output
}

// EventLog devolve os eventos recentes (DOWN/RECOVERED).
func (registry *HeartbeatRegistry) EventLog() []Event {
	if registry.eventRing == nil {
		return nil
	}
	return registry.eventRing.Snapshot()
}

// LookupBaseURL devolve a URL base de um serviço pelo nome. Usado pelo
// proxy do botão de toggle do dashboard.
func (registry *HeartbeatRegistry) LookupBaseURL(serviceName string) (string, bool) {
	registry.mutex.RLock()
	defer registry.mutex.RUnlock()
	state, ok := registry.servicesByName[serviceName]
	if !ok {
		return "", false
	}
	return state.configuration.BaseURL, true
}

// MarkSyntheticOutage existe pros testes, pra simular uma queda sem
// precisar esperar o intervalo do poll inteiro.
func (registry *HeartbeatRegistry) MarkSyntheticOutage(serviceName string) {
	registry.mutex.Lock()
	defer registry.mutex.Unlock()
	state, ok := registry.servicesByName[serviceName]
	if !ok {
		return
	}
	state.isAvailable = false
	state.consecutiveFailures = registry.failureThreshold
	state.lastTransitionAt = time.Now().UTC()
}
