package gateway

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/filipe-ms/distributed-ecommerce/internal/httpjson"
)

// O enunciado pede duas réplicas. Generalizar pra mais réplicas
// envolveria coisas como quórum e read-repair, que estão fora do escopo.
const productReplicaCount = 2

// ProductReplicaManager espalha as escritas pras duas réplicas e faz
// round-robin nas leituras. A consistência é forte: se uma das duas
// falhar na escrita, o gateway responde 5xx.
type ProductReplicaManager struct {
	replicaBaseURLs     [productReplicaCount]string
	replicaServiceNames [productReplicaCount]string
	internalHTTPClient  *http.Client
	availability        availabilityProbe
	logger              *slog.Logger
	roundRobinCounter   atomic.Uint64
}

// replicaWriteOutcome guarda o resultado de uma escrita em uma réplica.
type replicaWriteOutcome struct {
	ReplicaIndex    int
	StatusCode      int
	ResponseBody    []byte
	ResponseHeaders http.Header
	FailureReason   error
}

// NewProductReplicaManager cria o manager já apontando pras duas
// réplicas. Os nomes ("products-primary"/"products-replica") também
// são as chaves usadas no heartbeat.
func NewProductReplicaManager(
	primaryBaseURL, replicaBaseURL string,
	primaryServiceName, replicaServiceName string,
	internalHTTPClient *http.Client,
	availabilityRegistry availabilityProbe,
	logger *slog.Logger,
) *ProductReplicaManager {
	if logger == nil {
		logger = slog.Default()
	}
	return &ProductReplicaManager{
		replicaBaseURLs:     [productReplicaCount]string{primaryBaseURL, replicaBaseURL},
		replicaServiceNames: [productReplicaCount]string{primaryServiceName, replicaServiceName},
		internalHTTPClient:  internalHTTPClient,
		availability:        availabilityRegistry,
		logger:              logger,
	}
}

// HandleRead atende qualquer GET em /api/products. Escolhe uma réplica
// no round-robin; se a escolhida estiver fora, vai pra outra. Se as
// duas estiverem fora, responde 503.
func (manager *ProductReplicaManager) HandleRead(responseWriter http.ResponseWriter, incomingRequest *http.Request) {
	startingIndex := int(manager.roundRobinCounter.Add(1) % productReplicaCount)
	for offset := 0; offset < productReplicaCount; offset++ {
		candidateIndex := (startingIndex + offset) % productReplicaCount
		if !manager.isReplicaAvailable(candidateIndex) {
			continue
		}
		manager.forwardSingleRequest(responseWriter, incomingRequest, candidateIndex)
		return
	}
	httpjson.WriteError(responseWriter, http.StatusServiceUnavailable,
		"all product replicas are currently unavailable")
}

// HandleWrite atende qualquer escrita em /api/products. Manda pra
// duas réplicas em paralelo e só responde sucesso se as duas
// devolverem 2xx. Em falha parcial, devolve 500 e loga quem falhou.
func (manager *ProductReplicaManager) HandleWrite(responseWriter http.ResponseWriter, incomingRequest *http.Request) {
	bufferedBody, readError := io.ReadAll(io.LimitReader(incomingRequest.Body, httpjson.MaximumRequestBodyBytes+1))
	if readError != nil {
		httpjson.WriteError(responseWriter, http.StatusBadRequest, "could not read request body")
		return
	}
	_ = incomingRequest.Body.Close()
	if int64(len(bufferedBody)) > httpjson.MaximumRequestBodyBytes {
		httpjson.WriteError(responseWriter, http.StatusRequestEntityTooLarge, "request body too large")
		return
	}

	outcomeChannel := make(chan replicaWriteOutcome, productReplicaCount)
	for replicaIndex := 0; replicaIndex < productReplicaCount; replicaIndex++ {
		go func(currentReplicaIndex int) {
			outcomeChannel <- manager.callReplicaForWrite(
				incomingRequest.Context(),
				currentReplicaIndex,
				incomingRequest,
				bufferedBody,
			)
		}(replicaIndex)
	}

	collectedOutcomes := make([]replicaWriteOutcome, 0, productReplicaCount)
	for outcomeIndex := 0; outcomeIndex < productReplicaCount; outcomeIndex++ {
		collectedOutcomes = append(collectedOutcomes, <-outcomeChannel)
	}

	if anyOutcomeFailed(collectedOutcomes) {
		manager.logFailures(collectedOutcomes)
		manager.respondWithReplicaInconsistency(responseWriter, collectedOutcomes)
		return
	}

	// As duas réplicas aceitaram. Como a entrada foi a mesma, qualquer
	// uma das duas respostas serve.
	winningOutcome := collectedOutcomes[0]
	for headerName, headerValues := range winningOutcome.ResponseHeaders {
		if isHopByHop(headerName) {
			continue
		}
		for _, value := range headerValues {
			responseWriter.Header().Add(headerName, value)
		}
	}
	responseWriter.WriteHeader(winningOutcome.StatusCode)
	_, _ = responseWriter.Write(winningOutcome.ResponseBody)
}

func (manager *ProductReplicaManager) callReplicaForWrite(
	callContext context.Context,
	replicaIndex int,
	originalRequest *http.Request,
	bufferedBody []byte,
) replicaWriteOutcome {
	outcome := replicaWriteOutcome{ReplicaIndex: replicaIndex}

	if !manager.isReplicaAvailable(replicaIndex) {
		outcome.FailureReason = fmt.Errorf("replica %s marked unavailable", manager.replicaServiceNames[replicaIndex])
		return outcome
	}

	downstreamPath := strings.TrimPrefix(originalRequest.URL.Path, "/api")
	fullTargetURL := manager.replicaBaseURLs[replicaIndex] + downstreamPath
	if originalRequest.URL.RawQuery != "" {
		fullTargetURL = fullTargetURL + "?" + originalRequest.URL.RawQuery
	}

	outboundRequest, buildError := http.NewRequestWithContext(callContext, originalRequest.Method, fullTargetURL, bytes.NewReader(bufferedBody))
	if buildError != nil {
		outcome.FailureReason = fmt.Errorf("building replica request: %w", buildError)
		return outcome
	}
	for headerName, headerValues := range originalRequest.Header {
		if isHopByHop(headerName) {
			continue
		}
		for _, value := range headerValues {
			outboundRequest.Header.Add(headerName, value)
		}
	}

	downstreamResponse, sendError := manager.internalHTTPClient.Do(outboundRequest)
	if sendError != nil {
		outcome.FailureReason = fmt.Errorf("calling replica %s: %w", manager.replicaServiceNames[replicaIndex], sendError)
		return outcome
	}
	defer func() { _ = downstreamResponse.Body.Close() }()

	bodyBytes, readError := io.ReadAll(downstreamResponse.Body)
	if readError != nil {
		outcome.FailureReason = fmt.Errorf("reading replica response body: %w", readError)
		return outcome
	}
	outcome.StatusCode = downstreamResponse.StatusCode
	outcome.ResponseBody = bodyBytes
	outcome.ResponseHeaders = downstreamResponse.Header
	return outcome
}

func (manager *ProductReplicaManager) forwardSingleRequest(
	responseWriter http.ResponseWriter,
	incomingRequest *http.Request,
	replicaIndex int,
) {
	downstreamPath := strings.TrimPrefix(incomingRequest.URL.Path, "/api")
	fullTargetURL := manager.replicaBaseURLs[replicaIndex] + downstreamPath
	if incomingRequest.URL.RawQuery != "" {
		fullTargetURL = fullTargetURL + "?" + incomingRequest.URL.RawQuery
	}

	outboundRequest, buildError := http.NewRequestWithContext(incomingRequest.Context(), incomingRequest.Method, fullTargetURL, incomingRequest.Body)
	if buildError != nil {
		httpjson.WriteError(responseWriter, http.StatusBadGateway, "could not build replica request")
		return
	}
	copyRequestHeadersStrippingHopByHop(incomingRequest.Header, outboundRequest.Header)

	downstreamResponse, sendError := manager.internalHTTPClient.Do(outboundRequest)
	if sendError != nil {
		httpjson.WriteError(responseWriter, http.StatusBadGateway, "replica call failed: "+sendError.Error())
		return
	}
	defer func() { _ = downstreamResponse.Body.Close() }()

	copyResponseHeadersStrippingHopByHop(downstreamResponse.Header, responseWriter.Header())
	responseWriter.WriteHeader(downstreamResponse.StatusCode)
	_, _ = io.Copy(responseWriter, downstreamResponse.Body)
}

func (manager *ProductReplicaManager) isReplicaAvailable(replicaIndex int) bool {
	if manager.availability == nil {
		return true
	}
	return manager.availability.IsAvailable(manager.replicaServiceNames[replicaIndex])
}

func (manager *ProductReplicaManager) logFailures(outcomes []replicaWriteOutcome) {
	for _, outcome := range outcomes {
		if outcome.FailureReason != nil || outcome.StatusCode < 200 || outcome.StatusCode >= 300 {
			manager.logger.Error("product replica write failed",
				"replica_index", outcome.ReplicaIndex,
				"replica", manager.replicaServiceNames[outcome.ReplicaIndex],
				"status_code", outcome.StatusCode,
				"error", outcome.FailureReason)
		}
	}
}

func (manager *ProductReplicaManager) respondWithReplicaInconsistency(responseWriter http.ResponseWriter, outcomes []replicaWriteOutcome) {
	failedReplicaNames := make([]string, 0, len(outcomes))
	for _, outcome := range outcomes {
		if outcome.FailureReason != nil || outcome.StatusCode < 200 || outcome.StatusCode >= 300 {
			failedReplicaNames = append(failedReplicaNames, manager.replicaServiceNames[outcome.ReplicaIndex])
		}
	}
	httpjson.WriteError(responseWriter, http.StatusInternalServerError,
		fmt.Sprintf("product replication failed on: %s", strings.Join(failedReplicaNames, ", ")))
}

func anyOutcomeFailed(outcomes []replicaWriteOutcome) bool {
	for _, outcome := range outcomes {
		if outcome.FailureReason != nil || outcome.StatusCode < 200 || outcome.StatusCode >= 300 {
			return true
		}
	}
	return false
}
