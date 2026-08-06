package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/krishgondaliya/eventrail-ingestion/internal/httpapi"
)

func newReplayHandler(queryEvents replayQueryFunc, publish replayPublishFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		var req ReplayRequest
		if err := httpapi.DecodeJSONRequest(w, r, &req); err != nil {
			if httpapi.IsRequestBodyTooLarge(err) {
				http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}

		if req.From == "" || req.To == "" {
			http.Error(w, "from and to are required (RFC3339)", http.StatusBadRequest)
			return
		}
		if req.Cursor != nil {
			if strings.TrimSpace(req.Cursor.CreatedAt) == "" || strings.TrimSpace(req.Cursor.EventID) == "" {
				http.Error(w, "cursor created_at and event_id are required together", http.StatusBadRequest)
				return
			}
		}

		fromT, err := time.Parse(time.RFC3339, req.From)
		if err != nil {
			http.Error(w, "from must be RFC3339", http.StatusBadRequest)
			return
		}
		toT, err := time.Parse(time.RFC3339, req.To)
		if err != nil {
			http.Error(w, "to must be RFC3339", http.StatusBadRequest)
			return
		}
		var cursorCreatedAt time.Time
		if req.Cursor != nil {
			cursorCreatedAt, err = time.Parse(time.RFC3339, req.Cursor.CreatedAt)
			if err != nil {
				http.Error(w, "cursor created_at must be RFC3339", http.StatusBadRequest)
				return
			}
		}

		limit := req.Limit
		if limit <= 0 {
			limit = 1000
		}
		if limit > 5000 {
			limit = 5000
		}

		query := `
			SELECT id, event_type, source, payload, created_at
			FROM events
			WHERE created_at >= $1 AND created_at <= $2`
		args := []interface{}{fromT, toT}
		nextArg := 3

		if req.Source != "" {
			query += " AND source = $" + strconv.Itoa(nextArg)
			args = append(args, req.Source)
			nextArg++
		}

		if req.EventType != "" {
			query += " AND event_type = $" + strconv.Itoa(nextArg)
			args = append(args, req.EventType)
			nextArg++
		}

		if req.Cursor != nil {
			query += " AND (created_at > $" + strconv.Itoa(nextArg) +
				" OR (created_at = $" + strconv.Itoa(nextArg) +
				" AND id > $" + strconv.Itoa(nextArg+1) + "::uuid))"
			args = append(args, cursorCreatedAt, req.Cursor.EventID)
			nextArg += 2
		}

		query += " ORDER BY created_at ASC, id ASC LIMIT " + strconv.Itoa(limit+1)

		rows, err := queryEvents(r.Context(), query, args...)
		if err != nil {
			http.Error(w, "failed to query events", http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		published := 0
		hasMore := false
		var nextCursor *ReplayCursor
		for rows.Next() {
			var id, eventType, source string
			var payload []byte
			var createdAt time.Time
			if err := rows.Scan(&id, &eventType, &source, &payload, &createdAt); err != nil {
				http.Error(w, "failed to read events", http.StatusInternalServerError)
				return
			}
			if published >= limit {
				hasMore = true
				break
			}

			_, err := publish(r.Context(), map[string]interface{}{
				"event_id":   id,
				"event_type": eventType,
				"source":     source,
				"payload":    string(payload),
				"retry":      "0",
				"created_at": createdAt.UTC().Format(time.RFC3339),
				"replay":     "1",
			})
			if err != nil {
				log.Printf("replay publish failed for %s: %v", id, err)
				http.Error(w, "failed to publish replay event", http.StatusServiceUnavailable)
				return
			}
			published++
			nextCursor = &ReplayCursor{
				CreatedAt: createdAt.UTC().Format(time.RFC3339Nano),
				EventID:   id,
			}
		}
		if err := rows.Err(); err != nil {
			http.Error(w, "failed to read events", http.StatusInternalServerError)
			return
		}
		if !hasMore {
			nextCursor = nil
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ReplayResponse{
			Published:  published,
			HasMore:    hasMore,
			NextCursor: nextCursor,
		})
	}
}
