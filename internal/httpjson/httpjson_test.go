package httpjson

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWriteJSONEmitsCorrectContentTypeAndStatus(t *testing.T) {
	recordedResponse := httptest.NewRecorder()
	WriteJSON(recordedResponse, http.StatusCreated, map[string]string{"hello": "world"})

	if recordedResponse.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d", http.StatusCreated, recordedResponse.Code)
	}
	contentType := recordedResponse.Header().Get("Content-Type")
	if !strings.HasPrefix(contentType, "application/json") {
		t.Fatalf("expected JSON Content-Type, got %q", contentType)
	}
	if !strings.Contains(recordedResponse.Body.String(), `"hello":"world"`) {
		t.Fatalf("body did not contain payload: %q", recordedResponse.Body.String())
	}
}

func TestWriteJSONWithNilValueWritesNoBody(t *testing.T) {
	recordedResponse := httptest.NewRecorder()
	WriteJSON(recordedResponse, http.StatusNoContent, nil)
	if recordedResponse.Body.Len() != 0 {
		t.Fatalf("expected empty body, got %q", recordedResponse.Body.String())
	}
}

func TestWriteErrorProducesCanonicalShape(t *testing.T) {
	recordedResponse := httptest.NewRecorder()
	WriteError(recordedResponse, http.StatusBadRequest, "bad input")

	if recordedResponse.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", recordedResponse.Code)
	}
	var decoded ErrorResponse
	if decodeError := json.Unmarshal(recordedResponse.Body.Bytes(), &decoded); decodeError != nil {
		t.Fatalf("response body was not valid JSON: %v", decodeError)
	}
	if decoded.Error != "bad input" {
		t.Fatalf("expected error %q, got %q", "bad input", decoded.Error)
	}
}

func TestReadJSONHappyPath(t *testing.T) {
	type registrationPayload struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	body := strings.NewReader(`{"name":"Alice","email":"alice@example.com"}`)
	request := httptest.NewRequest(http.MethodPost, "/", body)

	var decoded registrationPayload
	if readError := ReadJSON(request, &decoded); readError != nil {
		t.Fatalf("expected success, got error: %v", readError)
	}
	if decoded.Name != "Alice" || decoded.Email != "alice@example.com" {
		t.Fatalf("decoded value mismatched input: %+v", decoded)
	}
}

func TestReadJSONRejectsUnknownFields(t *testing.T) {
	type narrowPayload struct {
		Name string `json:"name"`
	}
	body := strings.NewReader(`{"name":"Alice","unexpected":"field"}`)
	request := httptest.NewRequest(http.MethodPost, "/", body)

	var decoded narrowPayload
	if readError := ReadJSON(request, &decoded); readError == nil {
		t.Fatalf("expected an error for unknown field, got nil")
	}
}

func TestReadJSONRejectsMultipleTopLevelValues(t *testing.T) {
	type narrowPayload struct {
		Value int `json:"value"`
	}
	body := strings.NewReader(`{"value":1}{"value":2}`)
	request := httptest.NewRequest(http.MethodPost, "/", body)

	var decoded narrowPayload
	readError := ReadJSON(request, &decoded)
	if readError == nil {
		t.Fatalf("expected an error for multiple top-level values, got nil")
	}
	if !strings.Contains(readError.Error(), "single JSON value") {
		t.Fatalf("expected message about single JSON value, got %q", readError.Error())
	}
}

func TestReadJSONRejectsOversizedBody(t *testing.T) {
	type narrowPayload struct {
		Padding string `json:"padding"`
	}
	hugeJSON := bytes.NewBufferString(`{"padding":"`)
	hugeJSON.Write(bytes.Repeat([]byte("A"), int(MaximumRequestBodyBytes)+1024))
	hugeJSON.WriteString(`"}`)
	request := httptest.NewRequest(http.MethodPost, "/", hugeJSON)

	var decoded narrowPayload
	if readError := ReadJSON(request, &decoded); readError == nil {
		t.Fatalf("expected an error for oversized body, got nil")
	}
}
