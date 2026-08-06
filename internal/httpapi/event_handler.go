package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/krishgondaliya/eventrail-ingestion/internal/ingestion"
)

type CreateEventRequest struct {
	EventType string          `json:"event_type"`
	Source    string          `json:"source"`
	Payload   json.RawMessage `json:"payload"`
}

type CreateEventResponse struct {
	ID      string `json:"id"`
	Created bool   `json:"created"`
}

type PersistEventFunc func(
	ctx context.Context,
	input ingestion.EventInput,
	idempotencyKey string,
) (ingestion.PersistResult, error)

func NewCreateEventHandler(persist PersistEventFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		var req CreateEventRequest
		if err := DecodeJSONRequest(w, r, &req); err != nil {
			if IsRequestBodyTooLarge(err) {
				http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
				return
			}
			if errors.Is(err, ErrExtraJSONValue) {
				http.Error(w, "request body must contain exactly one JSON value", http.StatusBadRequest)
				return
			}
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}

		if req.EventType == "" || req.Source == "" || isMissingJSONPayload(req.Payload) {
			http.Error(w, "event_type, source, and payload are required", http.StatusBadRequest)
			return
		}

		result, err := persist(r.Context(), ingestion.EventInput{
			EventType: req.EventType,
			Source:    req.Source,
			Payload:   req.Payload,
		}, r.Header.Get("Idempotency-Key"))
		if err != nil {
			if errors.Is(err, ingestion.ErrIdempotencyConflict) {
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
