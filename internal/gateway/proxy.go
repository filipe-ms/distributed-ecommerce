// Package gateway implementa o API Gateway: um reverse proxy que
// valida o prefixo da URL, repassa o JWT, retorna 503 quando o serviço
// downstream está fora do ar (segundo o heartbeat), coordena as
// escritas nas duas réplicas de produtos e serve o dashboard.
package gateway

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/filipe-ms/distributed-ecommerce/internal/httpjson"
)

// Headers que descrevem a conexão (não a mensagem) e por isso não
// devem ser repassados em um proxy. Lista padrão da RFC 7230 §6.1.
var hopByHopHeaders = []string{
	"Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"TE",
	"Trailer",
	"Transfer-Encoding",
	"Upgrade",
}

// ServiceRoute representa um serviço que o gateway conhece.
// Name é a chave usada no heartbeat; BaseURL é o endereço do serviço.
type ServiceRoute struct {
	Name    string
	BaseURL string
}

// availabilityProbe é a interface mínima que o proxy precisa do
// heartbeat. Definir aqui evita um import circular nos testes.
type availabilityProbe interface {
	IsAvailable(serviceName string) bool
}

// ProxyClient é o reverse proxy do gateway. Sabe repassar requests e
// olha o heartbeat antes de mandar pra frente.
type ProxyClient struct {
	internalHTTPClient   *http.Client
	availabilityRegistry availabilityProbe
}

// NewProxyClient monta um ProxyClient. availabilityRegistry pode ser
// nil em testes — nesse caso o proxy considera todos os serviços como
// disponíveis.
func NewProxyClient(internalHTTPClient *http.Client, availabilityRegistry availabilityProbe) *ProxyClient {
	return &ProxyClient{
		internalHTTPClient:   internalHTTPClient,
		availabilityRegistry: availabilityRegistry,
	}
}

// HandlerFor devolve um http.HandlerFunc que repassa toda request com
// prefixo /api/<serviço>/ para o serviço de destino. O /api é tirado
// antes de mandar pro downstream.
func (proxy *ProxyClient) HandlerFor(route ServiceRoute) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, incomingRequest *http.Request) {
		if proxy.availabilityRegistry != nil && !proxy.availabilityRegistry.IsAvailable(route.Name) {
			httpjson.WriteError(responseWriter, http.StatusServiceUnavailable,
				fmt.Sprintf("%s service is currently unavailable", route.Name))
			return
		}
		downstreamPath := strings.TrimPrefix(incomingRequest.URL.Path, "/api")
		if downstreamPath == "" {
			downstreamPath = "/"
		}
		proxy.forwardRequest(responseWriter, incomingRequest, route.BaseURL+downstreamPath)
	}
}

// forwardRequest faz o trabalho do proxy de fato: copia método, corpo
// e headers (tirando os hop-by-hop) numa nova request, e devolve a
// resposta do downstream pro cliente original.
func (proxy *ProxyClient) forwardRequest(responseWriter http.ResponseWriter, incomingRequest *http.Request, fullTargetURL string) {
	if incomingRequest.URL.RawQuery != "" {
		fullTargetURL = fullTargetURL + "?" + incomingRequest.URL.RawQuery
	}

	outboundRequest, buildError := http.NewRequestWithContext(
		incomingRequest.Context(),
		incomingRequest.Method,
		fullTargetURL,
		incomingRequest.Body,
	)
	if buildError != nil {
		httpjson.WriteError(responseWriter, http.StatusBadGateway, "could not build downstream request")
		return
	}
	copyRequestHeadersStrippingHopByHop(incomingRequest.Header, outboundRequest.Header)

	downstreamResponse, sendError := proxy.internalHTTPClient.Do(outboundRequest)
	if sendError != nil {
		httpjson.WriteError(responseWriter, http.StatusBadGateway, "downstream call failed: "+sendError.Error())
		return
	}
	defer func() { _ = downstreamResponse.Body.Close() }()

	copyResponseHeadersStrippingHopByHop(downstreamResponse.Header, responseWriter.Header())
	responseWriter.WriteHeader(downstreamResponse.StatusCode)
	_, _ = io.Copy(responseWriter, downstreamResponse.Body)
}

func copyRequestHeadersStrippingHopByHop(source, destination http.Header) {
	for headerName, headerValues := range source {
		if isHopByHop(headerName) {
			continue
		}
		for _, value := range headerValues {
			destination.Add(headerName, value)
		}
	}
}

func copyResponseHeadersStrippingHopByHop(source, destination http.Header) {
	for headerName, headerValues := range source {
		if isHopByHop(headerName) {
			continue
		}
		for _, value := range headerValues {
			destination.Add(headerName, value)
		}
	}
}

func isHopByHop(headerName string) bool {
	for _, candidate := range hopByHopHeaders {
		if strings.EqualFold(candidate, headerName) {
			return true
		}
	}
	return false
}
