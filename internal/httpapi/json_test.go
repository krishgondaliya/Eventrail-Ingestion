package httpapi

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeJSONRequestValidJSON(t *testing.T) {
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"name":"eventrail"}`))
	rr := httptest.NewRecorder()

	var got struct {
		Name string `json:"name"`
	}
	if err := decodeJSONRequest(rr, req, &got, 64); err != nil {
		t.Fatalf("decodeJSONRequest returned error: %v", err)
	}
	if got.Name != "eventrail" {
		t.Fatalf("expected decoded name eventrail, got %q", got.Name)
	}
}

func TestDecodeJSONRequestMalformedJSON(t *testing.T) {
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"name":`))
	rr := httptest.NewRecorder()

	var got struct {
		Name string `json:"name"`
	}
	err := decodeJSONRequest(rr, req, &got, 64)
	if err == nil {
		t.Fatal("expected malformed JSON error")
	}
	if IsRequestBodyTooLarge(err) {
		t.Fatalf("expected bad request error, got too-large error: %v", err)
	}
	if errors.Is(err, ErrExtraJSONValue) {
		t.Fatalf("expected malformed JSON error, got extra JSON value error")
	}
}

func TestDecodeJSONRequestTooLarge(t *testing.T) {
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"name":"eventrail"}`))
	rr := httptest.NewRecorder()

	var got struct {
		Name string `json:"name"`
	}
	err := decodeJSONRequest(rr, req, &got, 8)
	if err == nil {
		t.Fatal("expected request body too large error")
	}
	if !IsRequestBodyTooLarge(err) {
		t.Fatalf("expected too-large error, got %v", err)
	}
}
