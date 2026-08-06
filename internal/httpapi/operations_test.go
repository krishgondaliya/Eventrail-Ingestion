package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLivenessReturnsOK(t *testing.T) {
	rr := performOperationsRequest(NewLivenessHandler(), http.MethodGet)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rr.Code)
	}

	var got livenessResponse
	decodeOperationResponse(t, rr, &got)
	if got.Status != "ok" {
		t.Fatalf("expected status ok, got %q", got.Status)
	}
}

func TestLivenessDoesNotCallDependencyChecks(t *testing.T) {
	postgresCalled := false
	redisCalled := false
	_ = DependencyCheck(func(context.Context) error {
		postgresCalled = true
		return errors.New("postgres unavailable")
	})
	_ = DependencyCheck(func(context.Context) error {
		redisCalled = true
		return errors.New("redis unavailable")
	})

	rr := performOperationsRequest(NewLivenessHandler(), http.MethodGet)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rr.Code)
	}
	if postgresCalled || redisCalled {
		t.Fatal("expected liveness not to call dependency checks")
	}
}

func TestReadinessReturnsOKWhenDependenciesSucceed(t *testing.T) {
	handler := NewReadinessHandler(
		func(context.Context) error { return nil },
		func(context.Context) error { return nil },
	)

	rr := performOperationsRequest(handler, http.MethodGet)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rr.Code)
	}

	var got readinessResponse
	decodeOperationResponse(t, rr, &got)
	if got.Status != "ok" || got.Postgres != "ok" || got.Redis != "ok" {
		t.Fatalf("unexpected readiness response: %#v", got)
	}
}

func TestReadinessReturnsUnavailableWhenDependencyFails(t *testing.T) {
	tests := []struct {
		name        string
		postgresErr error
		redisErr    error
		wantPG      string
		wantRedis   string
	}{
		{
			name:        "postgres fails",
			postgresErr: errors.New("postgres password secret"),
			wantPG:      "error",
			wantRedis:   "ok",
		},
		{
			name:      "redis fails",
			redisErr:  errors.New("redis password secret"),
			wantPG:    "ok",
			wantRedis: "error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := NewReadinessHandler(
				func(context.Context) error { return tt.postgresErr },
				func(context.Context) error { return tt.redisErr },
			)

			rr := performOperationsRequest(handler, http.MethodGet)

			if rr.Code != http.StatusServiceUnavailable {
				t.Fatalf("expected status %d, got %d", http.StatusServiceUnavailable, rr.Code)
			}
			responseBody := rr.Body.String()
			if strings.Contains(responseBody, "password secret") {
				t.Fatalf("readiness response exposed raw dependency error: %q", responseBody)
			}

			var got readinessResponse
			decodeOperationResponseBody(t, rr, responseBody, &got)
			if got.Status != "degraded" || got.Postgres != tt.wantPG || got.Redis != tt.wantRedis {
				t.Fatalf("unexpected readiness response: %#v", got)
			}
		})
	}
}

func TestVersionReturnsSuppliedValues(t *testing.T) {
	handler := NewVersionHandler("eventrail-api", "1.2.3", "abc123", "2026-08-06T12:00:00Z")

	rr := performOperationsRequest(handler, http.MethodGet)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rr.Code)
	}

	var got versionResponse
	decodeOperationResponse(t, rr, &got)
	if got.Service != "eventrail-api" || got.Version != "1.2.3" || got.Commit != "abc123" || got.BuiltAt != "2026-08-06T12:00:00Z" {
		t.Fatalf("unexpected version response: %#v", got)
	}
}

func TestOperationsHandlersRejectNonGet(t *testing.T) {
	tests := []struct {
		name    string
		handler http.Handler
	}{
		{
			name:    "liveness",
			handler: NewLivenessHandler(),
		},
		{
			name:    "readiness",
			handler: NewReadinessHandler(nil, nil),
		},
		{
			name:    "version",
			handler: NewVersionHandler("eventrail-api", "dev", "unknown", "unknown"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rr := performOperationsRequest(tt.handler, http.MethodPost)

			if rr.Code != http.StatusMethodNotAllowed {
				t.Fatalf("expected status %d, got %d", http.StatusMethodNotAllowed, rr.Code)
			}
			if allow := rr.Header().Get("Allow"); allow != http.MethodGet {
				t.Fatalf("expected Allow %q, got %q", http.MethodGet, allow)
			}
		})
	}
}

func performOperationsRequest(handler http.Handler, method string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

func decodeOperationResponse(t *testing.T, rr *httptest.ResponseRecorder, dst any) {
	t.Helper()

	decodeOperationResponseBody(t, rr, rr.Body.String(), dst)
}

func decodeOperationResponseBody(t *testing.T, rr *httptest.ResponseRecorder, body string, dst any) {
	t.Helper()

	if contentType := rr.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		t.Fatalf("expected JSON Content-Type, got %q", contentType)
	}
	if err := json.NewDecoder(strings.NewReader(body)).Decode(dst); err != nil {
		t.Fatalf("decode response %q: %v", body, err)
	}
}
