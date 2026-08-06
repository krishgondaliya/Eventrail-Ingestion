package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"sync"
)

const (
	modeHealthy           = "healthy"
	modeTemporaryFailure  = "temporary_failure"
	modeValidationFailure = "validation_failure"
)

type receiptServer struct {
	mu sync.Mutex

	mode       string
	failNext   int
	seenKeys   map[string]struct{}
	total      int
	successful int
	duplicates int
	failed     int
}

type receiptRequest struct {
	InvoiceID string `json:"invoice_id"`
	Amount    int    `json:"amount"`
	Currency  string `json:"currency"`
}

type modeRequest struct {
	Mode     string `json:"mode"`
	FailNext int    `json:"fail_next"`
}

type statsResponse struct {
	Mode               string `json:"mode"`
	RemainingFailures  int    `json:"remaining_forced_failures"`
	TotalRequests      int    `json:"total_requests"`
	SuccessfulReceipts int    `json:"successful_receipts"`
	DuplicateRequests  int    `json:"duplicate_requests"`
	FailedRequests     int    `json:"failed_requests"`
}

func main() {
	server := newReceiptServer()
	log.Println("mock destination starting on :8081")
	log.Fatal(http.ListenAndServe(":8081", server.routes()))
}

func newReceiptServer() *receiptServer {
	return &receiptServer{
		mode:     modeHealthy,
		seenKeys: make(map[string]struct{}),
	}
}

func (s *receiptServer) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/receipts", s.handleReceipts)
	mux.HandleFunc("/control/mode", s.handleControlMode)
	mux.HandleFunc("/stats", s.handleStats)
	return mux
}

func (s *receiptServer) handleReceipts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var req receiptRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.recordFailure()
		writeJSON(w, http.StatusBadRequest, map[string]string{"status": "failed", "error": "invalid JSON body"})
		return
	}
	if strings.TrimSpace(req.InvoiceID) == "" {
		s.recordFailure()
		writeJSON(w, http.StatusBadRequest, map[string]string{"status": "failed", "error": "invoice_id is required"})
		return
	}

	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	status, duplicate := s.applyReceipt(idempotencyKey)
	switch status {
	case http.StatusOK:
		response := map[string]interface{}{
			"status":     "accepted",
			"invoice_id": req.InvoiceID,
			"duplicate":  duplicate,
		}
		writeJSON(w, http.StatusOK, response)
	case http.StatusBadRequest:
		writeJSON(w, http.StatusBadRequest, map[string]string{"status": "failed", "error": "validation failure"})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"status": "failed", "error": "temporary failure"})
	}
}

func (s *receiptServer) handleControlMode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var req modeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"status": "failed", "error": "invalid JSON body"})
		return
	}
	if err := validateModeRequest(req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"status": "failed", "error": err.Error()})
		return
	}

	s.mu.Lock()
	s.mode = req.Mode
	s.failNext = req.FailNext
	s.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated", "mode": req.Mode})
}

func (s *receiptServer) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	s.mu.Lock()
	stats := statsResponse{
		Mode:               s.mode,
		RemainingFailures:  s.failNext,
		TotalRequests:      s.total,
		SuccessfulReceipts: s.successful,
		DuplicateRequests:  s.duplicates,
		FailedRequests:     s.failed,
	}
	s.mu.Unlock()

	writeJSON(w, http.StatusOK, stats)
}

func (s *receiptServer) applyReceipt(idempotencyKey string) (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.total++
	if s.failNext > 0 {
		s.failNext--
		s.failed++
		return http.StatusInternalServerError, false
	}

	switch s.mode {
	case modeTemporaryFailure:
		s.failed++
		return http.StatusInternalServerError, false
	case modeValidationFailure:
		s.failed++
		return http.StatusBadRequest, false
	}

	if idempotencyKey != "" {
		if _, ok := s.seenKeys[idempotencyKey]; ok {
			s.duplicates++
			return http.StatusOK, true
		}
		s.seenKeys[idempotencyKey] = struct{}{}
	}

	s.successful++
	return http.StatusOK, false
}

func (s *receiptServer) recordFailure() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.total++
	s.failed++
}

func validateModeRequest(req modeRequest) error {
	if req.FailNext < 0 {
		return errors.New("fail_next must be nonnegative")
	}
	switch req.Mode {
	case modeHealthy, modeTemporaryFailure, modeValidationFailure:
		return nil
	default:
		return errors.New("unsupported mode")
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Printf("encode response failed: %v", err)
	}
}
