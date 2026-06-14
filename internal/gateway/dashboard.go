package gateway

import (
	"bytes"
	"embed"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/filipe-ms/distributed-ecommerce/internal/httpjson"
)

//go:embed web/dashboard.html web/index.html web/estoque.html
var dashboardEmbeddedFiles embed.FS

// dashboardStatusResponse é o JSON que o front consome. Junta o
// snapshot do heartbeat com a lista de eventos recentes pra evitar que
// o navegador faça duas requisições por refresh.
type dashboardStatusResponse struct {
	Services []ServiceStatusSnapshot `json:"services"`
	Events   []Event                 `json:"events"`
}

// adminToggleResponse é o JSON que cada serviço devolve no /admin/toggle.
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

// writeStorefrontHTMLHandler serve o front de loja (index.html) na raiz.
func (server *Server) writeStorefrontHTMLHandler() http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, _ *http.Request) {
		htmlBytes, readError := dashboardEmbeddedFiles.ReadFile("web/index.html")
		if readError != nil {
			httpjson.WriteError(responseWriter, http.StatusInternalServerError, "storefront asset not embedded")
			return
		}
		responseWriter.Header().Set("Content-Type", "text/html; charset=utf-8")
		responseWriter.Header().Set("Cache-Control", "no-store")
		responseWriter.WriteHeader(http.StatusOK)
		_, _ = responseWriter.Write(htmlBytes)
	}
}

// writeStockHTMLHandler serve a página de estoque (cadastro e listagem
// com nome + quantidade).
func (server *Server) writeStockHTMLHandler() http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, _ *http.Request) {
		htmlBytes, readError := dashboardEmbeddedFiles.ReadFile("web/estoque.html")
		if readError != nil {
			httpjson.WriteError(responseWriter, http.StatusInternalServerError, "stock asset not embedded")
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

// writeAdminToggleProxyHandler repassa o POST do botão Kill/Revive
// pro /admin/toggle do serviço escolhido. O navegador não precisa
// saber a URL interna de cada serviço — só o nome.
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
