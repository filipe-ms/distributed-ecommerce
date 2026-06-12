// Package tlsserver wraps the small amount of plumbing every service needs
// to expose itself over HTTPS and to make outbound HTTPS calls to its
// neighbours. It centralises three things:
//
//   1. Building an *http.Server with conservative timeouts so a slow client
//      cannot pin a connection open forever.
//   2. Running that server with graceful shutdown driven by a context.
//   3. Producing an *http.Client that skips certificate verification for
//      internal traffic (we use a single self-signed certificate across all
//      services, so verification would always fail anyway). The shortcut is
//      called out as a limitation in the project report.
package tlsserver

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// readHeaderTimeout is the only deadline we set unconditionally. Without it,
// a malicious client can leave the headers half-written and consume server
// resources indefinitely (the classic Slowloris pattern).
const readHeaderTimeout = 10 * time.Second

// readTimeout and writeTimeout are wider because the JSON payloads in this
// project are small but a developer poking at the gateway with curl over a
// flaky link should still succeed.
const readTimeout = 30 * time.Second
const writeTimeout = 30 * time.Second

// idleTimeout reclaims keep-alive connections that nothing is using.
const idleTimeout = 60 * time.Second

// shutdownGracePeriod gives in-flight handlers a moment to finish before the
// server forcibly closes them. Five seconds is comfortable for the
// short-lived requests in this project.
const shutdownGracePeriod = 5 * time.Second

// ListenAndServe runs an HTTPS server with the supplied handler. It blocks
// until shutdownContext is cancelled (typically by SIGTERM), then performs a
// graceful shutdown. The function returns nil on a clean shutdown and any
// non-shutdown error from http.Server's ListenAndServeTLS.
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

// InsecureInternalClient is the HTTP client services use to call each other
// across the docker-compose network. It deliberately disables certificate
// verification because our internal certificates are self-signed and shared
// across services; production deployments would use a CA-signed chain plus
// mTLS, which we mention in the report's limitations section.
func InsecureInternalClient(perRequestTimeout time.Duration) *http.Client {
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true, // intentional: see package comment
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
