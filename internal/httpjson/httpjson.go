// Package httpjson provides a couple of small helpers that every service in
// this project uses to read and write JSON over HTTP. Centralising them keeps
// content-type, error shape and request-size limits consistent across the
// gateway and the four backing services.
package httpjson

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// MaximumRequestBodyBytes caps incoming JSON payloads so that a malicious or
// runaway client cannot exhaust memory by streaming a huge body. One megabyte
// is more than enough for the modest payloads in this assignment.
const MaximumRequestBodyBytes int64 = 1 << 20

// ErrorResponse is the canonical shape every service returns on a non-2xx
// response. Keeping it tiny and consistent keeps the gateway's reverse-proxy
// code simple — it can forward errors without rewriting them.
type ErrorResponse struct {
	Error string `json:"error"`
}

// WriteJSON serialises value as JSON and writes it to responseWriter alongside
// the supplied HTTP status code. It is a no-op for nil values, matching the
// convention used by 204-style endpoints.
func WriteJSON(responseWriter http.ResponseWriter, statusCode int, value any) {
	responseWriter.Header().Set("Content-Type", "application/json; charset=utf-8")
	responseWriter.WriteHeader(statusCode)
	if value == nil {
		return
	}
	encoder := json.NewEncoder(responseWriter)
	if encodingError := encoder.Encode(value); encodingError != nil {
		// At this point the headers are already on the wire, so we cannot
		// recover; logging is the responsibility of higher-level middleware.
		_ = encodingError
	}
}

// WriteError writes a plain JSON error response. It is the only way services
// in this project should return non-2xx bodies so that the dashboard and the
// gateway proxy can rely on a single response shape.
func WriteError(responseWriter http.ResponseWriter, statusCode int, message string) {
	WriteJSON(responseWriter, statusCode, ErrorResponse{Error: message})
}

// ReadJSON decodes the request body into target, enforcing both a hard size
// limit and "no unknown fields" decoding so that typos in client requests
// surface as 400s instead of being silently ignored.
//
// It deliberately produces a small, friendly error message — never the raw
// json.Unmarshal output — because that error is destined to land in the
// browser via the dashboard or curl in the README walkthrough.
func ReadJSON(request *http.Request, target any) error {
	request.Body = http.MaxBytesReader(nil, request.Body, MaximumRequestBodyBytes)

	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()

	if decodeError := decoder.Decode(target); decodeError != nil {
		return classifyDecodeError(decodeError)
	}

	// Reject payloads that have more than one top-level JSON value. This is
	// the same guard the standard library docs recommend.
	trailing := struct{}{}
	if extraValueError := decoder.Decode(&trailing); !errors.Is(extraValueError, io.EOF) {
		return errors.New("request body must contain a single JSON value")
	}
	return nil
}

func classifyDecodeError(decodeError error) error {
	var syntaxError *json.SyntaxError
	var unmarshalTypeError *json.UnmarshalTypeError
	switch {
	case errors.As(decodeError, &syntaxError):
		return fmt.Errorf("malformed JSON at byte %d", syntaxError.Offset)
	case errors.Is(decodeError, io.ErrUnexpectedEOF):
		return errors.New("malformed JSON: unexpected end of body")
	case errors.As(decodeError, &unmarshalTypeError):
		return fmt.Errorf("invalid value for field %q (expected %s)",
			unmarshalTypeError.Field, unmarshalTypeError.Type.String())
	case errors.Is(decodeError, io.EOF):
		return errors.New("request body is empty")
	default:
		return fmt.Errorf("could not decode request body: %w", decodeError)
	}
}
