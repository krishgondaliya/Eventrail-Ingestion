package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const maxAIResponseBytes = 1 << 20

var (
	ErrAIServiceUnavailable     = errors.New("AI service unavailable")
	ErrAIServiceInvalidResponse = errors.New("AI service invalid response")
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
	Provider              string           `json:"provider"`
	Model                 *string          `json:"model"`
}

type triageCitation struct {
	RunbookID  string `json:"runbook_id"`
	ChunkID    string `json:"chunk_id"`
	Title      string `json:"title"`
	SourcePath string `json:"source_path"`
}

type explainRequest struct {
	EventType         string                   `json:"event_type"`
	BusinessEventType *string                  `json:"business_event_type"`
	Source            string                   `json:"source"`
	Destination       string                   `json:"destination"`
	CurrentStatus     string                   `json:"current_status"`
	StatusHistory     []explainStatusHistory   `json:"status_history"`
	DeliveryAttempts  []explainDeliveryAttempt `json:"delivery_attempts"`
	RetryCount        int                      `json:"retry_count"`
	EnteredDLQ        bool                     `json:"entered_dlq"`
	RedriveCount      int                      `json:"redrive_count"`
	Delivered         bool                     `json:"delivered"`
}

type explainStatusHistory struct {
	Status     string  `json:"status"`
	OccurredAt *string `json:"occurred_at"`
}

type explainDeliveryAttempt struct {
	AttemptNumber int     `json:"attempt_number"`
	HTTPStatus    *int    `json:"http_status"`
	Outcome       string  `json:"outcome"`
	Error         *string `json:"error"`
	OccurredAt    *string `json:"occurred_at"`
}

type explainResponse struct {
	Headline           string            `json:"headline"`
	WhatHappened       string            `json:"what_happened"`
	BusinessImpact     string            `json:"business_impact"`
	NextAction         string            `json:"next_action"`
	RecommendedActions []string          `json:"recommended_actions"`
	RecoveryStatus     string            `json:"recovery_status"`
	Evidence           []explainEvidence `json:"evidence"`
	Citations          []triageCitation  `json:"citations"`
	AnalysisMode       string            `json:"analysis_mode"`
	Provider           string            `json:"provider"`
	Model              *string           `json:"model"`
}

type explainEvidence struct {
	Type        string `json:"type"`
	Description string `json:"description"`
}

func NewAITriageClient(baseURL string, client *http.Client) *AITriageClient {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
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

func (c *AITriageClient) Explain(ctx context.Context, request explainRequest) (explainResponse, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return explainResponse{}, fmt.Errorf("marshal explain request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/explain", bytes.NewReader(body))
	if err != nil {
		return explainResponse{}, fmt.Errorf("create explain request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return explainResponse{}, fmt.Errorf("%w: %v", ErrAIServiceUnavailable, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return explainResponse{}, fmt.Errorf("%w: AI explain service returned %s", ErrAIServiceUnavailable, resp.Status)
	}

	var result explainResponse
	decoder := json.NewDecoder(io.LimitReader(resp.Body, maxAIResponseBytes))
	if err := decoder.Decode(&result); err != nil {
		return explainResponse{}, fmt.Errorf("%w: decode AI explain response: %v", ErrAIServiceInvalidResponse, err)
	}
	if err := validateExplainResponse(result); err != nil {
		return explainResponse{}, fmt.Errorf("%w: %v", ErrAIServiceInvalidResponse, err)
	}
	return result, nil
}

func validateExplainResponse(response explainResponse) error {
	for name, value := range map[string]string{
		"headline":        response.Headline,
		"what_happened":   response.WhatHappened,
		"business_impact": response.BusinessImpact,
		"next_action":     response.NextAction,
		"recovery_status": response.RecoveryStatus,
		"analysis_mode":   response.AnalysisMode,
		"provider":        response.Provider,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	if len(response.RecommendedActions) == 0 || len(response.RecommendedActions) > 5 {
		return errors.New("recommended_actions must contain 1 to 5 items")
	}
	for _, action := range response.RecommendedActions {
		if strings.TrimSpace(action) == "" {
			return errors.New("recommended_actions must be nonempty")
		}
	}
	if !isSupportedRecoveryStatus(response.RecoveryStatus) {
		return fmt.Errorf("unsupported recovery_status %q", response.RecoveryStatus)
	}
	if !isSupportedAnalysisMode(response.AnalysisMode) {
		return fmt.Errorf("unsupported analysis_mode %q", response.AnalysisMode)
	}
	if !isSupportedProvider(response.Provider) {
		return fmt.Errorf("unsupported provider %q", response.Provider)
	}
	if len(response.Evidence) == 0 || len(response.Evidence) > 10 {
		return errors.New("evidence must contain 1 to 10 items")
	}
	for _, evidence := range response.Evidence {
		if strings.TrimSpace(evidence.Type) == "" || strings.TrimSpace(evidence.Description) == "" {
			return errors.New("evidence entries must be complete")
		}
	}
	if len(response.Citations) == 0 {
		return errors.New("citations are required")
	}
	for _, citation := range response.Citations {
		if strings.TrimSpace(citation.RunbookID) == "" ||
			strings.TrimSpace(citation.ChunkID) == "" ||
			strings.TrimSpace(citation.Title) == "" ||
			strings.TrimSpace(citation.SourcePath) == "" {
			return errors.New("citation entries must be complete")
		}
	}
	return nil
}

func isSupportedRecoveryStatus(status string) bool {
	switch status {
	case "not_needed", "not_ready", "review_required", "completed":
		return true
	default:
		return false
	}
}

func isSupportedAnalysisMode(mode string) bool {
	switch mode {
	case "deterministic_runbook", "deterministic_fallback", "llm_grounded":
		return true
	default:
		return false
	}
}

func isSupportedProvider(provider string) bool {
	switch provider {
	case "deterministic", "openai", "ollama":
		return true
	default:
		return false
	}
}
