package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthyRequestSucceeds(t *testing.T) {
	handler := newReceiptServer().routes()
	resp := postReceipt(t, handler, sampleReceiptBody(), "")

	if resp.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %q", resp.Code, resp.Body.String())
	}
}

func TestMissingInvoiceIDReturnsBadRequest(t *testing.T) {
	handler := newReceiptServer().routes()
	resp := postReceipt(t, handler, `{"amount":500,"currency":"USD"}`, "")

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", resp.Code)
	}
}

func TestTemporaryModeReturnsServerError(t *testing.T) {
	handler := newReceiptServer().routes()
	setMode(t, handler, modeTemporaryFailure, 0)

	resp := postReceipt(t, handler, sampleReceiptBody(), "")
	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("expected status 500, got %d", resp.Code)
	}
}

func TestValidationModeReturnsBadRequest(t *testing.T) {
	handler := newReceiptServer().routes()
	setMode(t, handler, modeValidationFailure, 0)

	resp := postReceipt(t, handler, sampleReceiptBody(), "")
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", resp.Code)
	}
}

func TestFailNextFailsThenRecovers(t *testing.T) {
	handler := newReceiptServer().routes()
	setMode(t, handler, modeHealthy, 2)

	for i := 0; i < 2; i++ {
		resp := postReceipt(t, handler, sampleReceiptBody(), "")
		if resp.Code != http.StatusInternalServerError {
			t.Fatalf("expected forced failure %d to return 500, got %d", i+1, resp.Code)
		}
	}

	resp := postReceipt(t, handler, sampleReceiptBody(), "")
	if resp.Code != http.StatusOK {
		t.Fatalf("expected recovered request to return 200, got %d", resp.Code)
	}
}

func TestIdempotencyKeyDoesNotApplyDuplicateReceipt(t *testing.T) {
	handler := newReceiptServer().routes()

	first := postReceipt(t, handler, sampleReceiptBody(), "receipt-1")
	if first.Code != http.StatusOK {
		t.Fatalf("expected first request status 200, got %d", first.Code)
	}
	second := postReceipt(t, handler, sampleReceiptBody(), "receipt-1")
	if second.Code != http.StatusOK {
		t.Fatalf("expected duplicate request status 200, got %d", second.Code)
	}

	stats := getStats(t, handler)
	if stats.SuccessfulReceipts != 1 {
		t.Fatalf("expected one successful receipt, got %d", stats.SuccessfulReceipts)
	}
	if stats.DuplicateRequests != 1 {
		t.Fatalf("expected one duplicate request, got %d", stats.DuplicateRequests)
	}
}

func TestStatsReflectRequestsAndDuplicates(t *testing.T) {
	handler := newReceiptServer().routes()

	postReceipt(t, handler, sampleReceiptBody(), "receipt-1")
	postReceipt(t, handler, sampleReceiptBody(), "receipt-1")
	postReceipt(t, handler, `{"amount":500,"currency":"USD"}`, "")

	stats := getStats(t, handler)
	if stats.TotalRequests != 3 {
		t.Fatalf("expected total requests 3, got %d", stats.TotalRequests)
	}
	if stats.SuccessfulReceipts != 1 {
		t.Fatalf("expected successful receipts 1, got %d", stats.SuccessfulReceipts)
	}
	if stats.DuplicateRequests != 1 {
		t.Fatalf("expected duplicate requests 1, got %d", stats.DuplicateRequests)
	}
	if stats.FailedRequests != 1 {
		t.Fatalf("expected failed requests 1, got %d", stats.FailedRequests)
	}
}

func postReceipt(t *testing.T, handler http.Handler, body string, idempotencyKey string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/receipts", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	return resp
}

func setMode(t *testing.T, handler http.Handler, mode string, failNext int) {
	t.Helper()

	body, err := json.Marshal(modeRequest{
		Mode:     mode,
		FailNext: failNext,
	})
	if err != nil {
		t.Fatalf("marshal mode request: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/control/mode", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("set mode returned status %d with body %q", resp.Code, resp.Body.String())
	}
}

func getStats(t *testing.T, handler http.Handler) statsResponse {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, "/stats", nil)
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("stats returned status %d", resp.Code)
	}

	var stats statsResponse
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		t.Fatalf("decode stats: %v", err)
	}
	return stats
}

func sampleReceiptBody() string {
	return `{"invoice_id":"INV-2048","amount":500,"currency":"USD"}`
}
