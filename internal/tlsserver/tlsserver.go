// Package tlsserver tem o boilerplate que cada serviço precisa pra
// expor HTTPS e fazer chamadas HTTPS pros vizinhos. Centraliza três
// coisas:
//
//  1. Montar um *http.Server com timeouts conservadores pra um cliente
//     lento não prender uma conexão pra sempre.
//  2. Rodar esse servidor com graceful shutdown via context.
//  3. Devolver um *http.Client que ignora a verificação de
//     certificado pro tráfego interno (a gente usa o mesmo cert
//     auto-assinado em todos os containers, então a verificação
//     falharia sempre). Isso tá listado como limitação no relatório.
package tlsserver

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// readHeaderTimeout é o único deadline que vale sempre. Sem ele um
// cliente malicioso pode deixar os headers metade escritos e segurar
// o servidor indefinidamente (Slowloris).
const readHeaderTimeout = 10 * time.Second

// readTimeout e writeTimeout são mais largos porque os payloads aqui
// são pequenos, mas alguém testando o gateway com curl numa rede
// instável precisa conseguir terminar a request.
const readTimeout = 30 * time.Second
const writeTimeout = 30 * time.Second

// idleTimeout libera as conexões keep-alive que ninguém tá usando.
const idleTimeout = 60 * time.Second

// shutdownGracePeriod dá um tempinho pras requests em andamento
// terminarem antes do servidor fechar. 5 segundos basta.
const shutdownGracePeriod = 5 * time.Second

// ListenAndServe sobe o servidor HTTPS com o handler dado. Trava até
// shutdownContext ser cancelado (geralmente por SIGTERM) e aí faz o
// shutdown graceful. Devolve nil em desligamento limpo, ou o erro do
// http.Server.
func ListenAndServe(shutdownContext context.Context, listenAddress string, requestHandler http.Handler, certificateFilePath, keyFilePath string) error {
	httpsServer := &http.Server{
		Addr:              listenAddress,
		Handler:           requestHandler,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		TLSConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
	}

	serverErrorChannel := make(chan error, 1)
	go func() {
		serverErrorChannel <- httpsServer.ListenAndServeTLS(certificateFilePath, keyFilePath)
	}()

	select {
	case <-shutdownContext.Done():
		gracefulContext, cancelGracefulContext := context.WithTimeout(context.Background(), shutdownGracePeriod)
		defer cancelGracefulContext()
		if shutdownError := httpsServer.Shutdown(gracefulContext); shutdownError != nil {
			return fmt.Errorf("graceful shutdown: %w", shutdownError)
		}
		return nil
	case startupError := <-serverErrorChannel:
		if errors.Is(startupError, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("listen and serve TLS: %w", startupError)
	}
}

// InsecureInternalClient devolve o http.Client que os serviços usam
// pra falar uns com os outros pela rede do docker-compose. Desliga a
// verificação de cert porque os certs internos são auto-assinados e
// compartilhados. Em produção a gente usaria CA + mTLS, e isso tá
// citado nas limitações do relatório.
func InsecureInternalClient(perRequestTimeout time.Duration) *http.Client {
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true, // intencional, ver comentário do pacote
			MinVersion:         tls.VersionTLS12,
		},
		MaxIdleConns:        50,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     idleTimeout,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   perRequestTimeout,
	}
}
