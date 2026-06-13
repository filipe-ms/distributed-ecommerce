// Package gateway implements the API gateway: a small reverse proxy that
// validates URL prefixes, forwards JWTs untouched, short-circuits with 503
// when a downstream service is marked unavailable, coordinates writes across
// the two product replicas, and serves a small monitoring dashboard.
package gateway

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/filipe-ms/distributed-ecommerce/internal/httpjson"
)

// hopByHopHeaders are stripped on the way in and out: they describe the
// transport hop, not the message, so leaking them across a proxy hop confuses
// keep-alive negotiation. The list is the canonical one from RFC 7230 §6.1.
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

// ServiceRoute is the gateway's notion of a downstream microservice. The
// Name doubles as the registry key used by the heartbeat for availability
// look-ups; the BaseURL is the absolute origin (scheme + host + port) the
// proxy targets.
type ServiceRoute struct {
	Name    string
	BaseURL string
}

// availabilityProbe is the minimum interface the proxy needs from the
// heartbeat registry. Defining it here, instead of importing the concrete
// registry, keeps the test for the proxy from depending on the heartbeat.
type availabilityProbe interface {
	IsAvailable(serviceName string) bool
}

// ProxyClient is a reverse proxy that knows about service availability. A
// single instance is shared between every route handler in the gateway.
type ProxyClient struct {
	internalHTTPClient   *http.Client
	availabilityRegistry availabilityProbe
}

// NewProxyClient builds a ProxyClient. availabilityRegistry may be nil when
// running in unit tests where no heartbeat exists; in that case every route
// is treated as available.
func NewProxyClient(internalHTTPClient *http.Client, availabilityRegistry availabilityProbe) *ProxyClient {
	return &ProxyClient{
		internalHTTPClient:   internalHTTPClient,
		availabilityRegistry: availabilityRegistry,
	}
}

// HandlerFor returns an http.HandlerFunc that forwards every request whose
// path starts with /api/<service>/ to the supplied route. The /api prefix is
// stripped before the request is sent downstream, so the backing services
// only see their native routes (e.g. /users/login, /orders/{userId}).
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

// forwardRequest is the actual proxy step. It copies method/body/headers
// (except for the hop-by-hop set) into a new outbound request and streams
// the response back to the original caller.
func (proxy *ProxyClient) forwardRequest(responseWriter http.ResponseWriter, incomingRequest *http.Request, fullTargetURL string) {
	// Preserve the query string. r.URL.RawQuery is present on every chi
	// request; appending it unconditionally even when empty produces a bare
	// "?" which curl tolerates but http.NewRequest does not strip, so we
	// guard the join.
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
