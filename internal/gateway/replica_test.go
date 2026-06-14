package gateway

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

type recordingReplica struct {
	server         *httptest.Server
	statusToReturn atomic.Int32
	hitCount       atomic.Int32
	lastBody       atomic.Value // string
}

func startRecordingReplica(t *testing.T) *recordingReplica {
	t.Helper()
	replica := &recordingReplica{}
	replica.statusToReturn.Store(http.StatusCreated)
	mux := http.NewServeMux()
	mux.HandleFunc("/products", func(responseWriter http.ResponseWriter, request *http.Request) {
		bodyBytes, _ := io.ReadAll(request.Body)
		replica.lastBody.Store(string(bodyBytes))
		replica.hitCount.Add(1)
		responseWriter.WriteHeader(int(replica.statusToReturn.Load()))
		_, _ = responseWriter.Write([]byte(`{"id":1,"name":"echo"}`))
	})
	mux.HandleFunc("/products/", func(responseWriter http.ResponseWriter, request *http.Request) {
		_ = request
		replica.hitCount.Add(1)
		responseWriter.WriteHeader(int(replica.statusToReturn.Load()))
		_, _ = responseWriter.Write([]byte(`{"id":1,"name":"echo"}`))
	})
	replica.server = httptest.NewTLSServer(mux)
	t.Cleanup(replica.server.Close)
	return replica
}

type fixedAvailability struct {
	availableServices map[string]bool
}

func (probe fixedAvailability) IsAvailable(serviceName string) bool {
	return probe.availableServices[serviceName]
}

func TestReplicaWriteHitsBothReplicas(t *testing.T) {
	primary := startRecordingReplica(t)
	replica := startRecordingReplica(t)

	manager := NewProductReplicaManager(
		primary.server.URL, replica.server.URL,
		"products-primary", "products-replica",
		newPermissiveTLSClient(),
		fixedAvailability{availableServices: map[string]bool{
			"products-primary": true,
			"products-replica": true,
		}},
		nil,
	)

	requestBody := `{"name":"Coffee","price":9.99,"description":"Arabica"}`
	request := httptest.NewRequest(http.MethodPost, "/api/products", bytes.NewBufferString(requestBody))
	request.Header.Set("Content-Type", "application/json")
	recordedResponse := httptest.NewRecorder()

	manager.HandleWrite(recordedResponse, request)

	if recordedResponse.Code < 200 || recordedResponse.Code >= 300 {
		t.Fatalf("expected 2xx, got %d (body=%s)", recordedResponse.Code, recordedResponse.Body.String())
	}
	if primary.hitCount.Load() != 1 || replica.hitCount.Load() != 1 {
		t.Fatalf("expected each replica to be hit once, got primary=%d replica=%d",
			primary.hitCount.Load(), replica.hitCount.Load())
	}
	if primary.lastBody.Load().(string) != requestBody {
		t.Fatalf("primary received different body: %q", primary.lastBody.Load().(string))
	}
	if replica.lastBody.Load().(string) != requestBody {
		t.Fatalf("replica received different body: %q", replica.lastBody.Load().(string))
	}
}

func TestReplicaWriteFailsWhenOneReplicaFails(t *testing.T) {
	primary := startRecordingReplica(t)
	replica := startRecordingReplica(t)
	replica.statusToReturn.Store(http.StatusInternalServerError)

	manager := NewProductReplicaManager(
		primary.server.URL, replica.server.URL,
		"products-primary", "products-replica",
		newPermissiveTLSClient(),
		fixedAvailability{availableServices: map[string]bool{
			"products-primary": true,
			"products-replica": true,
		}},
		nil,
	)

	request := httptest.NewRequest(http.MethodPost, "/api/products", bytes.NewBufferString(`{"name":"x","price":1,"description":""}`))
	recordedResponse := httptest.NewRecorder()

	manager.HandleWrite(recordedResponse, request)
	if recordedResponse.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 when one replica fails, got %d", recordedResponse.Code)
	}
	if !strings.Contains(recordedResponse.Body.String(), "products-replica") {
		t.Fatalf("error body should name the failed replica, got %q", recordedResponse.Body.String())
	}
}

func TestReplicaReadAlternatesBetweenReplicas(t *testing.T) {
	primary := startRecordingReplica(t)
	replica := startRecordingReplica(t)

	manager := NewProductReplicaManager(
		primary.server.URL, replica.server.URL,
		"products-primary", "products-replica",
		newPermissiveTLSClient(),
		fixedAvailability{availableServices: map[string]bool{
			"products-primary": true,
			"products-replica": true,
		}},
		nil,
	)

	for index := 0; index < 4; index++ {
		request := httptest.NewRequest(http.MethodGet, "/api/products", nil)
		recordedResponse := httptest.NewRecorder()
		manager.HandleRead(recordedResponse, request)
		if recordedResponse.Code != http.StatusCreated {
			t.Fatalf("read %d returned %d", index, recordedResponse.Code)
		}
	}

	totalHits := primary.hitCount.Load() + replica.hitCount.Load()
	if totalHits != 4 {
		t.Fatalf("expected 4 total hits, got %d", totalHits)
	}
	if primary.hitCount.Load() == 0 || replica.hitCount.Load() == 0 {
		t.Fatalf("expected round-robin to hit both replicas, got primary=%d replica=%d",
			primary.hitCount.Load(), replica.hitCount.Load())
	}
}

func TestReplicaReadFallsBackWhenPrimaryUnavailable(t *testing.T) {
	primary := startRecordingReplica(t)
	replica := startRecordingReplica(t)

	manager := NewProductReplicaManager(
		primary.server.URL, replica.server.URL,
		"products-primary", "products-replica",
		newPermissiveTLSClient(),
		fixedAvailability{availableServices: map[string]bool{
			"products-primary": false,
			"products-replica": true,
		}},
		nil,
	)

	for index := 0; index < 3; index++ {
		request := httptest.NewRequest(http.MethodGet, "/api/products", nil)
		recordedResponse := httptest.NewRecorder()
		manager.HandleRead(recordedResponse, request)
		if recordedResponse.Code != http.StatusCreated {
			t.Fatalf("read %d returned %d", index, recordedResponse.Code)
		}
	}
	if primary.hitCount.Load() != 0 {
		t.Fatalf("primary marked unavailable should receive no traffic, got %d", primary.hitCount.Load())
	}
	if replica.hitCount.Load() != 3 {
		t.Fatalf("replica should have served all 3 reads, got %d", replica.hitCount.Load())
	}
}

func TestReplicaReadReturns503WhenBothUnavailable(t *testing.T) {
	primary := startRecordingReplica(t)
	replica := startRecordingReplica(t)

	manager := NewProductReplicaManager(
		primary.server.URL, replica.server.URL,
		"products-primary", "products-replica",
		newPermissiveTLSClient(),
		fixedAvailability{availableServices: map[string]bool{
			"products-primary": false,
			"products-replica": false,
		}},
		nil,
	)

	request := httptest.NewRequest(http.MethodGet, "/api/products", nil)
	recordedResponse := httptest.NewRecorder()
	manager.HandleRead(recordedResponse, request)

	if recordedResponse.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when both replicas are unavailable, got %d", recordedResponse.Code)
	}
}
