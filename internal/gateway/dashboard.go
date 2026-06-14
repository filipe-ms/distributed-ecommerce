package gateway

import (
	"bytes"
	"embed"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/filipe-ms/distributed-ecommerce/internal/httpjson"
)

//go:embed web/dashboard.html
var dashboardEmbeddedFiles embed.FS

// dashboardStatusResponse is the JSON shape consumed by the front-end. It is
// an aggregate of the heartbeat registry's snapshot and the recent events
// buffer so the browser does not need to make two requests per refresh.
type dashboardStatusResponse struct {
	Services []ServiceStatusSnapshot `json:"services"`
	Events   []Event                 `json:"events"`
}

// adminToggleResponse mirrors the JSON shape exposed by every backing
// service's POST /admin/toggle endpoint.
type adminToggleResponse struct {
	Killed bool `json:"killed"`
}

func (server *Server) writeDashboardHTMLHandler() http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, _ *http.Request) {
		htmlBytes, readError := dashboardEmbeddedFiles.ReadFile("web/dashboard.html")
		if readError != nil {
			httpjson.WriteError(responseWriter, http.StatusInternalServerError, "dashboard asset not embedded")
			return
		}
		responseWriter.Header().Set("Content-Type", "text/html; charset=utf-8")
		responseWriter.Header().Set("Cache-Control", "no-store")
		responseWriter.WriteHeader(http.StatusOK)
		_, _ = responseWriter.Write(htmlBytes)
	}
}

func (server *Server) writeDashboardStatusHandler() http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, _ *http.Request) {
		response := dashboardStatusResponse{
			Services: server.heartbeatRegistry.Snapshot(),
			Events:   server.heartbeatRegistry.EventLog(),
		}
		httpjson.WriteJSON(responseWriter, http.StatusOK, response)
	}
}

// writeAdminToggleProxyHandler proxies POST /administration/toggle/{service}
// to the corresponding backing service's POST /admin/toggle endpoint. Each
// "Kill" / "Revive" button on the dashboard makes one of these calls; the
// gateway is the single place that knows the per-service base URLs so the
// browser only needs to know the service name.
func (server *Server) writeAdminToggleProxyHandler() http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, incomingRequest *http.Request) {
		serviceName := chi.URLParam(incomingRequest, "serviceName")
		baseURL, found := server.heartbeatRegistry.LookupBaseURL(serviceName)
		if !found {
			httpjson.WriteError(responseWriter, http.StatusNotFound, "unknown service: "+serviceName)
			return
		}

		outboundRequest, buildError := http.NewRequestWithContext(
			incomingRequest.Context(),
			http.MethodPost,
			baseURL+"/admin/toggle",
			bytes.NewReader(nil),
		)
		if buildError != nil {
			httpjson.WriteError(responseWriter, http.StatusBadGateway, "could not build toggle request")
			return
		}

		downstreamResponse, sendError := server.internalClient.Do(outboundRequest)
		if sendError != nil {
			httpjson.WriteError(responseWriter, http.StatusBadGateway, "toggle call failed: "+sendError.Error())
			return
		}
		defer func() { _ = downstreamResponse.Body.Close() }()

		responseWriter.Header().Set("Content-Type", "application/json; charset=utf-8")
		responseWriter.WriteHeader(downstreamResponse.StatusCode)
		_, _ = io.Copy(responseWriter, downstreamResponse.Body)
	}
}
