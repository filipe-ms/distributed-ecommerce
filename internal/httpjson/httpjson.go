// Package httpjson tem alguns helpers pequenos que todos os serviços
// usam pra ler e escrever JSON via HTTP. Centralizar aqui mantém o
// content-type, o formato de erro e o limite de tamanho do corpo
// iguais em todo lugar.
package httpjson

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// MaximumRequestBodyBytes é o limite de tamanho do corpo da request.
// 1 MB é mais que suficiente pros payloads do trabalho.
const MaximumRequestBodyBytes int64 = 1 << 20

// ErrorResponse é o formato canônico de qualquer resposta de erro do
// projeto. Manter pequeno e consistente facilita a vida do gateway, que
// só repassa o erro sem reescrever.
type ErrorResponse struct {
	Error string `json:"error"`
}

// WriteJSON serializa value em JSON e escreve na resposta com o
// status code dado. Se value for nil, só escreve o status (estilo 204).
func WriteJSON(responseWriter http.ResponseWriter, statusCode int, value any) {
	responseWriter.Header().Set("Content-Type", "application/json; charset=utf-8")
	responseWriter.WriteHeader(statusCode)
	if value == nil {
		return
	}
	encoder := json.NewEncoder(responseWriter)
	if encodingError := encoder.Encode(value); encodingError != nil {
		// Os headers já saíram, então não tem como recuperar; o log
		// fica a cargo do middleware de cima.
		_ = encodingError
	}
}

// WriteError escreve uma resposta de erro JSON. É a única forma que
// os serviços usam pra responder não-2xx, pra ter um formato único.
func WriteError(responseWriter http.ResponseWriter, statusCode int, message string) {
	WriteJSON(responseWriter, statusCode, ErrorResponse{Error: message})
}

// ReadJSON decodifica o corpo da request em target. Aplica o limite de
// tamanho e rejeita campos desconhecidos, pra erro de digitação no
// cliente virar 400 em vez de ser ignorado em silêncio.
func ReadJSON(request *http.Request, target any) error {
	request.Body = http.MaxBytesReader(nil, request.Body, MaximumRequestBodyBytes)

	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()

	if decodeError := decoder.Decode(target); decodeError != nil {
		return classifyDecodeError(decodeError)
	}

	// Rejeita corpos com mais de um valor JSON. É a recomendação dos
	// docs da stdlib.
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
