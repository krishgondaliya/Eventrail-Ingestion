package ingestion

import (
	"encoding/json"
	"regexp"
	"testing"
)

func TestComputeRequestHashEquivalentRequests(t *testing.T) {
	tests := []struct {
		name  string
		left  string
		right string
	}{
		{
			name:  "object key order",
			left:  `{"invoice_id":"INV-2048","amount":500}`,
			right: `{"amount":500,"invoice_id":"INV-2048"}`,
		},
		{
			name: "insignificant whitespace",
			left: `{"invoice_id":"INV-2048","amount":500}`,
			right: `{
				"invoice_id": "INV-2048",
				"amount": 500
			}`,
		},
		{
			name:  "nested object key order",
			left:  `{"invoice":{"id":"INV-2048","customer":{"id":"C-1","tier":"pro"}}}`,
			right: `{"invoice":{"customer":{"tier":"pro","id":"C-1"},"id":"INV-2048"}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			left := mustComputeRequestHash(t, "invoice.created", "billing", tt.left)
			right := mustComputeRequestHash(t, "invoice.created", "billing", tt.right)

			if left != right {
				t.Fatalf("expected hashes to match:\nleft:  %s\nright: %s", left, right)
			}
		})
	}
}

func TestComputeRequestHashRepeatedCallsAreStable(t *testing.T) {
	payload := `{"invoice_id":"INV-2048","amount":500}`
	first := mustComputeRequestHash(t, "invoice.created", "billing", payload)

	for i := 0; i < 5; i++ {
		next := mustComputeRequestHash(t, "invoice.created", "billing", payload)
		if next != first {
			t.Fatalf("expected repeated call %d to return %s, got %s", i, first, next)
		}
	}
}

func TestComputeRequestHashMeaningfulDifferences(t *testing.T) {
	tests := []struct {
		name       string
		eventTypeA string
		sourceA    string
		payloadA   string
		eventTypeB string
		sourceB    string
		payloadB   string
	}{
		{
			name:       "different payload value",
			eventTypeA: "invoice.created",
			sourceA:    "billing",
			payloadA:   `{"amount":500}`,
			eventTypeB: "invoice.created",
			sourceB:    "billing",
			payloadB:   `{"amount":600}`,
		},
		{
			name:       "different event type",
			eventTypeA: "invoice.created",
			sourceA:    "billing",
			payloadA:   `{"amount":500}`,
			eventTypeB: "invoice.paid",
			sourceB:    "billing",
			payloadB:   `{"amount":500}`,
		},
		{
			name:       "different source",
			eventTypeA: "invoice.created",
			sourceA:    "billing",
			payloadA:   `{"amount":500}`,
			eventTypeB: "invoice.created",
			sourceB:    "payments",
			payloadB:   `{"amount":500}`,
		},
		{
			name:       "array order",
			eventTypeA: "invoice.created",
			sourceA:    "billing",
			payloadA:   `{"items":["a","b"]}`,
			eventTypeB: "invoice.created",
			sourceB:    "billing",
			payloadB:   `{"items":["b","a"]}`,
		},
		{
			name:       "numeric lexical spelling",
			eventTypeA: "invoice.created",
			sourceA:    "billing",
			payloadA:   `{"amount":500}`,
			eventTypeB: "invoice.created",
			sourceB:    "billing",
			payloadB:   `{"amount":500.0}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := mustComputeRequestHash(t, tt.eventTypeA, tt.sourceA, tt.payloadA)
			b := mustComputeRequestHash(t, tt.eventTypeB, tt.sourceB, tt.payloadB)

			if a == b {
				t.Fatalf("expected hashes to differ, both were %s", a)
			}
		})
	}
}

func TestComputeRequestHashInvalidPayloads(t *testing.T) {
	tests := []struct {
		name    string
		payload string
	}{
		{
			name:    "empty payload",
			payload: "",
		},
		{
			name:    "malformed JSON",
			payload: `{"amount":`,
		},
		{
			name:    "multiple JSON values",
			payload: `{"amount":500} {"amount":600}`,
		},
		{
			name:    "valid JSON followed by invalid trailing text",
			payload: `{"amount":500} nope`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if hash, err := computeRequestHash("invoice.created", "billing", json.RawMessage(tt.payload)); err == nil {
				t.Fatalf("expected error, got hash %s", hash)
			}
		})
	}
}

func TestComputeRequestHashOutputFormat(t *testing.T) {
	hash := mustComputeRequestHash(t, "invoice.created", "billing", `{"amount":500}`)

	if len(hash) != 64 {
		t.Fatalf("expected hash length 64, got %d", len(hash))
	}

	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(hash) {
		t.Fatalf("expected lowercase hex SHA-256 hash, got %s", hash)
	}
}

func mustComputeRequestHash(t *testing.T, eventType string, source string, payload string) string {
	t.Helper()

	hash, err := computeRequestHash(eventType, source, json.RawMessage(payload))
	if err != nil {
		t.Fatalf("computeRequestHash returned error: %v", err)
	}
	return hash
}
