package delivery

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestClassifyHTTPStatus(t *testing.T) {
	tests := []struct {
		statusCode int
		want       FailureKind
	}{
		{statusCode: 400, want: FailurePermanent},
		{statusCode: 401, want: FailurePermanent},
		{statusCode: 404, want: FailurePermanent},
		{statusCode: 408, want: FailureRetryable},
		{statusCode: 425, want: FailureRetryable},
		{statusCode: 429, want: FailureRetryable},
		{statusCode: 500, want: FailureRetryable},
		{statusCode: 503, want: FailureRetryable},
		{statusCode: 599, want: FailureRetryable},
		{statusCode: 200, want: ""},
		{statusCode: 399, want: ""},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%d", tt.statusCode), func(t *testing.T) {
			got := ClassifyHTTPStatus(tt.statusCode)
			if got != tt.want {
				t.Fatalf("expected %q, got %q", tt.want, got)
			}
		})
	}
}

func TestTypedFailures(t *testing.T) {
	retryCause := errors.New("network timeout")
	permanentCause := errors.New("bad webhook payload")

	retryErr := NewRetryableFailure(retryCause)
	if !IsRetryable(retryErr) {
		t.Fatal("expected retryable constructor to be detected")
	}
	if IsPermanent(retryErr) {
		t.Fatal("did not expect retryable error to be permanent")
	}
	if !errors.Is(retryErr, retryCause) {
		t.Fatal("expected errors.Is to reach retryable cause")
	}

	permanentErr := NewPermanentFailure(permanentCause)
	if !IsPermanent(permanentErr) {
		t.Fatal("expected permanent constructor to be detected")
	}
	if IsRetryable(permanentErr) {
		t.Fatal("did not expect permanent error to be retryable")
	}
	if !errors.Is(permanentErr, permanentCause) {
		t.Fatal("expected errors.Is to reach permanent cause")
	}

	if !IsRetryable(fmt.Errorf("wrapped: %w", retryErr)) {
		t.Fatal("expected wrapped retryable error to be detected")
	}
	if !IsPermanent(fmt.Errorf("wrapped: %w", permanentErr)) {
		t.Fatal("expected wrapped permanent error to be detected")
	}

	if IsRetryable(nil) || IsPermanent(nil) {
		t.Fatal("nil should not be retryable or permanent")
	}
}

func TestHTTPFailure(t *testing.T) {
	retryErr := NewHTTPFailure(503, "503 Service Unavailable")
	if !IsRetryable(retryErr) {
		t.Fatal("expected 503 to produce retryable failure")
	}

	permanentErr := NewHTTPFailure(400, "400 Bad Request")
	if !IsPermanent(permanentErr) {
		t.Fatal("expected 400 to produce permanent failure")
	}

	if err := NewHTTPFailure(204, "204 No Content"); err != nil {
		t.Fatalf("expected non-error status to produce nil failure, got %v", err)
	}
}

func TestFailureErrorDoesNotIncludeExternalPayloadSecret(t *testing.T) {
	secretPayload := `{"secret":"payload-token-123"}`
	err := &Failure{
		Kind:       FailurePermanent,
		StatusCode: 400,
		Cause:      errors.New("bad request"),
	}

	if strings.Contains(err.Error(), secretPayload) || strings.Contains(err.Error(), "payload-token-123") {
		t.Fatalf("failure error leaked payload secret: %q", err.Error())
	}
}
