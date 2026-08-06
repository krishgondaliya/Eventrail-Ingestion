package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
)

type persistEventFunc func(
	ctx context.Context,
	req CreateEventRequest,
	idempotencyKey string,
) (PersistEventResult, error)

func newCreateEventHandler(persist persistEventFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		decoder := json.NewDecoder(r.Body)

		var req CreateEventRequest
		if err := decoder.Decode(&req); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}

		var extra any
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			http.Error(w, "request body must contain exactly one JSON value", http.StatusBadRequest)
			return
		}

		if req.EventType == "" || req.Source == "" || isMissingJSONPayload(req.Payload) {
			http.Error(w, "event_type, source, and payload are required", http.StatusBadRequest)
			return
		}

		result, err := persist(r.Context(), req, r.Header.Get("Idempotency-Key"))
		if err != nil {
			if errors.Is(err, ErrIdempotencyConflict) {
				http.Error(w, "idempotency key was already used for a different request", http.StatusConflict)
				return
			}

			log.Printf("persist event failed: %v", err)
			http.Error(w, "failed to persist event", http.StatusInternalServerError)
			return
		}

		status := http.StatusOK
		if result.Created {
			status = http.StatusCreated
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if err := json.NewEncoder(w).Encode(CreateEventResponse{
			ID:      result.EventID,
			Created: result.Created,
		}); err != nil {
			log.Printf("encode create event response failed: %v", err)
		}
	}
}

func isMissingJSONPayload(payload json.RawMessage) bool {
	trimmed := bytes.TrimSpace(payload)
	return len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null"))
}
