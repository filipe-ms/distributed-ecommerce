package gateway

import (
	"context"
	"log/slog"
	"net/http"
	"sort"
	"sync"
	"time"
)

// HeartbeatPollInterval is the cadence at which the gateway probes every
// downstream service. The assignment requires 5 seconds; we keep it as a
// constant so tests can shorten it without touching the public API.
const HeartbeatPollInterval = 5 * time.Second

// FailuresBeforeMarkingDown is the consecutive-failure threshold from the
// assignment. Two missed probes (10s of unreachability) is enough to flip a
// service to DOWN.
const FailuresBeforeMarkingDown = 2

// healthProbeTimeout caps how long a single /health probe is allowed to
// take. Keeping it well below HeartbeatPollInterval ensures one slow
// service cannot delay probes for the others.
const healthProbeTimeout = 2 * time.Second

// MonitoredService describes one service the gateway should poll.
type MonitoredService struct {
	Name    string // dashboard label and registry key
	BaseURL string // origin used to construct the probe URL
}

// ServiceStatusSnapshot is the read-only view of a service the dashboard
// uses to render its status grid.
type ServiceStatusSnapshot struct {
	Name                 string    `json:"name"`
	BaseURL              string    `json:"baseUrl"`
	IsAvailable          bool      `json:"available"`
	ConsecutiveFailures  int       `json:"consecutiveFailures"`
	LastTransitionAt     time.Time `json:"lastTransitionAt"`
	LastSuccessfulProbe  time.Time `json:"lastSuccessfulProbe"`
}

// monitoredServiceState is the gateway-private record kept per service.
type monitoredServiceState struct {
	configuration       MonitoredService
	consecutiveFailures int
	isAvailable         bool
	lastTransitionAt    time.Time
	lastSuccessfulProbe time.Time
}

// HeartbeatRegistry polls a fixed set of services on a ticker and exposes
// thread-safe accessors so the proxy and the dashboard can read the latest
// availability without blocking the polling loop.
type HeartbeatRegistry struct {
	mutex                  sync.RWMutex
	servicesByName         map[string]*monitoredServiceState
	logger                 *slog.Logger
	internalHTTPClient     *http.Client
	pollInterval           time.Duration
	failureThreshold       int
	eventRing              *EventRing
}

// NewHeartbeatRegistry builds a registry pre-populated with one entry per
// supplied service. Each service starts in the available state — we let the
// first probe round confirm or refute that.
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

// SetPollInterval lets tests speed up the loop. It must be called before Run.
func (registry *HeartbeatRegistry) SetPollInterval(interval time.Duration) {
	registry.pollInterval = interval
}

// SetFailureThreshold lets tests verify the recovery path with a
// non-default threshold. Call before Run.
func (registry *HeartbeatRegistry) SetFailureThreshold(threshold int) {
	if threshold < 1 {
		threshold = 1
	}
	registry.failureThreshold = threshold
}

// Run blocks until the supplied context is cancelled, polling every
// registered service on each tick. Probes are issued in parallel so a single
// hung service cannot delay the rest of the round.
func (registry *HeartbeatRegistry) Run(loopContext context.Context) {
	ticker := time.NewTicker(registry.pollInterval)
	defer ticker.Stop()

	registry.tickOnce(loopContext) // run an immediate pass instead of waiting one full interval

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

// recordProbeOutcome updates the per-service counters and, when a transition
// happens, logs it and pushes an event into the ring.
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

// IsAvailable reports whether the named service is currently considered up.
// Unknown service names return true so the proxy never accidentally blocks
// traffic for a service the registry simply does not know about.
func (registry *HeartbeatRegistry) IsAvailable(serviceName string) bool {
	registry.mutex.RLock()
	defer registry.mutex.RUnlock()
	state, ok := registry.servicesByName[serviceName]
	if !ok {
		return true
	}
	return state.isAvailable
}

// Snapshot returns a stable, sorted slice of every service's current status.
// The dashboard renders this directly.
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

// EventLog returns the recent events tracked by the registry's ring.
func (registry *HeartbeatRegistry) EventLog() []Event {
	if registry.eventRing == nil {
		return nil
	}
	return registry.eventRing.Snapshot()
}

// LookupBaseURL returns the registered base URL for a service, used by the
// admin toggle proxy. The bool is false when the name is unknown.
func (registry *HeartbeatRegistry) LookupBaseURL(serviceName string) (string, bool) {
	registry.mutex.RLock()
	defer registry.mutex.RUnlock()
	state, ok := registry.servicesByName[serviceName]
	if !ok {
		return "", false
	}
	return state.configuration.BaseURL, true
}

// MarkSyntheticOutage is exposed for tests so they can simulate the result
// of repeated probe failures without sleeping for the full poll interval.
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
