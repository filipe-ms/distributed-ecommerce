package gateway

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// startFakeService spins up a TLS test server whose /health response can be
// flipped between 200 and 500 from the test. The returned baseURL is the
// origin a HeartbeatRegistry would target.
type fakeService struct {
	server         *httptest.Server
	healthStatus   atomic.Int32
	healthRequests atomic.Int32
}

func startFakeService(t *testing.T) *fakeService {
	t.Helper()
	fake := &fakeService{}
	fake.healthStatus.Store(http.StatusOK)
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(responseWriter http.ResponseWriter, _ *http.Request) {
		fake.healthRequests.Add(1)
		responseWriter.WriteHeader(int(fake.healthStatus.Load()))
		_, _ = responseWriter.Write([]byte(`{"status":"ok"}`))
	})
	fake.server = httptest.NewTLSServer(mux)
	t.Cleanup(fake.server.Close)
	return fake
}

func (fake *fakeService) baseURL() string { return fake.server.URL }

func (fake *fakeService) markHealthy()   { fake.healthStatus.Store(http.StatusOK) }
func (fake *fakeService) markUnhealthy() { fake.healthStatus.Store(http.StatusInternalServerError) }

func newPermissiveTLSClient() *http.Client {
	return &http.Client{
		Timeout: 2 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
}

func TestHeartbeatMarksDownAfterConsecutiveFailures(t *testing.T) {
	fake := startFakeService(t)
	fake.markUnhealthy()

	registry := NewHeartbeatRegistry(
		[]MonitoredService{{Name: "fake", BaseURL: fake.baseURL()}},
		newPermissiveTLSClient(),
		nil,
		NewEventRing(10),
	)
	registry.SetFailureThreshold(2)

	// Manually run two probe rounds.
	registry.tickOnce(testContext(t))
	if !registry.IsAvailable("fake") {
		t.Fatalf("after the first failure the service should still be available (one strike)")
	}
	registry.tickOnce(testContext(t))
	if registry.IsAvailable("fake") {
		t.Fatalf("after the second consecutive failure the service should be marked DOWN")
	}

	events := registry.EventLog()
	if len(events) != 1 {
		t.Fatalf("expected exactly one DOWN event, got %d", len(events))
	}
	if events[0].Kind != EventKindServiceDown {
		t.Fatalf("expected DOWN kind, got %s", events[0].Kind)
	}
}

func TestHeartbeatRecoversOnFirstSuccessAfterDown(t *testing.T) {
	fake := startFakeService(t)
	fake.markUnhealthy()

	registry := NewHeartbeatRegistry(
		[]MonitoredService{{Name: "fake", BaseURL: fake.baseURL()}},
		newPermissiveTLSClient(),
		nil,
		NewEventRing(10),
	)
	registry.SetFailureThreshold(1)

	registry.tickOnce(testContext(t))
	if registry.IsAvailable("fake") {
		t.Fatalf("expected service marked DOWN after threshold=1 failure")
	}

	fake.markHealthy()
	registry.tickOnce(testContext(t))
	if !registry.IsAvailable("fake") {
		t.Fatalf("expected service to recover after first successful probe")
	}

	kinds := make([]EventKind, 0)
	for _, current := range registry.EventLog() {
		kinds = append(kinds, current.Kind)
	}
	if len(kinds) != 2 {
		t.Fatalf("expected DOWN then RECOVERED events, got %v", kinds)
	}
	// Newest first: RECOVERED before DOWN.
	if kinds[0] != EventKindServiceRecovered || kinds[1] != EventKindServiceDown {
		t.Fatalf("event order is wrong: %v", kinds)
	}
}

func TestHeartbeatSnapshotIsSortedByName(t *testing.T) {
	registry := NewHeartbeatRegistry(
		[]MonitoredService{
			{Name: "users", BaseURL: "https://users:5001"},
			{Name: "orders", BaseURL: "https://orders:5003"},
			{Name: "products-primary", BaseURL: "https://products-primary:5002"},
		},
		newPermissiveTLSClient(),
		nil,
		NewEventRing(10),
	)
	snapshot := registry.Snapshot()
	if len(snapshot) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(snapshot))
	}
	for index := 1; index < len(snapshot); index++ {
		if snapshot[index].Name < snapshot[index-1].Name {
			t.Fatalf("snapshot is not alphabetically sorted: %v", snapshot)
		}
	}
}

func TestHeartbeatLookupBaseURL(t *testing.T) {
	registry := NewHeartbeatRegistry(
		[]MonitoredService{{Name: "users", BaseURL: "https://users:5001"}},
		newPermissiveTLSClient(),
		nil,
		NewEventRing(10),
	)
	url, ok := registry.LookupBaseURL("users")
	if !ok || url != "https://users:5001" {
		t.Fatalf("LookupBaseURL returned (%q, %v)", url, ok)
	}
	if _, found := registry.LookupBaseURL("missing"); found {
		t.Fatalf("LookupBaseURL should report false for unknown service")
	}
}

func TestHeartbeatMarkSyntheticOutageSetsDown(t *testing.T) {
	registry := NewHeartbeatRegistry(
		[]MonitoredService{{Name: "users", BaseURL: "https://users:5001"}},
		newPermissiveTLSClient(),
		nil,
		NewEventRing(10),
	)
	registry.SetFailureThreshold(2)
	registry.MarkSyntheticOutage("users")
	if registry.IsAvailable("users") {
		t.Fatalf("MarkSyntheticOutage should flip availability to false")
	}
}
