package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type AITriageClient struct {
	baseURL string
	client  *http.Client
}

type triageRequest struct {
	EventType         string  `json:"event_type"`
	BusinessEventType *string `json:"business_event_type,omitempty"`
	Source            string  `json:"source"`
	Destination       string  `json:"destination"`
	HTTPStatus        *int    `json:"http_status"`
	Error             string  `json:"error"`
	AttemptCount      int     `json:"attempt_count"`
	SchemaVersion     *string `json:"schema_version,omitempty"`
}

type triageResponse struct {
	Category              string           `json:"category"`
	Summary               string           `json:"summary"`
	RecommendedActions    []string         `json:"recommended_actions"`
	RedriveRecommendation string           `json:"redrive_recommendation"`
	Citations             []triageCitation `json:"citations"`
	AnalysisMode          string           `json:"analysis_mode"`
}

type triageCitation struct {
	RunbookID  string `json:"runbook_id"`
	ChunkID    string `json:"chunk_id"`
	Title      string `json:"title"`
	SourcePath string `json:"source_path"`
}

func NewAITriageClient(baseURL string, client *http.Client) *AITriageClient {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	return &AITriageClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  client,
	}
}

func (c *AITriageClient) Triage(ctx context.Context, request triageRequest) (triageResponse, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return triageResponse{}, fmt.Errorf("marshal triage request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/triage", bytes.NewReader(body))
	if err != nil {
		return triageResponse{}, fmt.Errorf("create triage request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return triageResponse{}, fmt.Errorf("call AI triage service: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return triageResponse{}, fmt.Errorf("AI triage service returned %s", resp.Status)
	}

	var result triageResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return triageResponse{}, fmt.Errorf("decode AI triage response: %w", err)
	}
	return result, nil
}
