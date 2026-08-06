package ingestion

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

type requestHashInput struct {
	EventType string `json:"event_type"`
	Source    string `json:"source"`
	Payload   any    `json:"payload"`
}

// computeRequestHash canonicalizes the logical request before hashing so
// idempotency conflict detection is not affected by JSON whitespace or object
// key ordering. JSON number spelling is preserved in Phase 1A, so 500 and
// 500.0 intentionally hash differently.
func computeRequestHash(eventType string, source string, payload json.RawMessage) (string, error) {
	if len(bytes.TrimSpace(payload)) == 0 {
		return "", errors.New("payload must contain exactly one JSON value")
	}

	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()

	var decodedPayload any
	if err := decoder.Decode(&decodedPayload); err != nil {
		return "", fmt.Errorf("invalid payload JSON: %w", err)
	}

	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return "", errors.New("payload must contain exactly one JSON value")
	} else if !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("invalid trailing JSON content: %w", err)
	}

	hashInput := requestHashInput{
		EventType: eventType,
		Source:    source,
		Payload:   decodedPayload,
	}

	canonicalBytes, err := json.Marshal(hashInput)
	if err != nil {
		return "", fmt.Errorf("marshal request hash input: %w", err)
	}

	sum := sha256.Sum256(canonicalBytes)
	return hex.EncodeToString(sum[:]), nil
}
