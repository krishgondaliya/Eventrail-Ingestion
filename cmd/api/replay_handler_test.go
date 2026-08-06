package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestReplayPaginationStableForSharedTimestamp(t *testing.T) {
	createdAt := time.Date(2026, 8, 6, 12, 0, 0, 123000000, time.UTC)
	rows := []fakeReplayRow{
		{id: "00000000-0000-0000-0000-000000000001", eventType: "invoice.paid", source: "billing", payload: []byte(`{"n":1}`), createdAt: createdAt},
		{id: "00000000-0000-0000-0000-000000000002", eventType: "invoice.paid", source: "billing", payload: []byte(`{"n":2}`), createdAt: createdAt},
		{id: "00000000-0000-0000-0000-000000000003", eventType: "invoice.paid", source: "billing", payload: []byte(`{"n":3}`), createdAt: createdAt},
	}
	var queryText string
	var published []map[string]interface{}

	handler := newReplayHandler(
		func(ctx context.Context, query string, args ...any) (replayRows, error) {
			queryText = query
			return &fakeReplayRows{rows: rows}, nil
		},
		func(ctx context.Context, values map[string]interface{}) (string, error) {
			published = append(published, values)
			return "1-0", nil
		},
	)

	rr := performReplayRequest(handler, `{
		"from":"2026-08-06T00:00:00Z",
		"to":"2026-08-07T00:00:00Z",
		"limit":2
	}`)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rr.Code, http.StatusOK, rr.Body.String())
	}
	if !strings.Contains(queryText, "ORDER BY created_at ASC, id ASC LIMIT 3") {
		t.Fatalf("query does not use stable order and limit+1: %s", queryText)
	}
	if len(published) != 2 {
		t.Fatalf("published count = %d, want 2", len(published))
	}

	var resp ReplayResponse
	decodeReplayResponse(t, rr, &resp)
	if resp.Published != 2 || !resp.HasMore {
		t.Fatalf("response = %+v, want published=2 has_more=true", resp)
	}
	if resp.NextCursor == nil {
		t.Fatal("next_cursor is nil, want cursor for last published event")
	}
	if resp.NextCursor.EventID != "00000000-0000-0000-0000-000000000002" {
		t.Fatalf("next cursor event_id = %q", resp.NextCursor.EventID)
	}
	if resp.NextCursor.CreatedAt != createdAt.Format(time.RFC3339Nano) {
		t.Fatalf("next cursor created_at = %q", resp.NextCursor.CreatedAt)
	}
}

func TestReplayCursorFiltersStrictlyAfterCreatedAtAndID(t *testing.T) {
	cursorAt := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	var queryText string
	var queryArgs []any

	handler := newReplayHandler(
		func(ctx context.Context, query string, args ...any) (replayRows, error) {
			queryText = query
			queryArgs = append(queryArgs, args...)
			return &fakeReplayRows{}, nil
		},
		func(ctx context.Context, values map[string]interface{}) (string, error) {
			t.Fatal("publish should not be called for empty result set")
			return "", nil
		},
	)

	rr := performReplayRequest(handler, `{
		"from":"2026-08-06T00:00:00Z",
		"to":"2026-08-07T00:00:00Z",
		"cursor":{
			"created_at":"2026-08-06T12:00:00Z",
			"event_id":"00000000-0000-0000-0000-000000000123"
		}
	}`)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rr.Code, http.StatusOK, rr.Body.String())
	}
	if !strings.Contains(queryText, "AND (created_at > $3 OR (created_at = $3 AND id > $4::uuid))") {
		t.Fatalf("query does not filter strictly after cursor: %s", queryText)
	}
	if len(queryArgs) != 4 {
		t.Fatalf("query args len = %d, want 4", len(queryArgs))
	}
	if got := queryArgs[2].(time.Time); !got.Equal(cursorAt) {
		t.Fatalf("cursor created_at arg = %s, want %s", got, cursorAt)
	}
	if got := queryArgs[3]; got != "00000000-0000-0000-0000-000000000123" {
		t.Fatalf("cursor event_id arg = %v", got)
	}
}

func TestReplayRejectsPartialCursor(t *testing.T) {
	queryCalled := false
	publishCalled := false
	handler := newReplayHandler(
		func(ctx context.Context, query string, args ...any) (replayRows, error) {
			queryCalled = true
			return &fakeReplayRows{}, nil
		},
		func(ctx context.Context, values map[string]interface{}) (string, error) {
			publishCalled = true
			return "1-0", nil
		},
	)

	rr := performReplayRequest(handler, `{
		"from":"2026-08-06T00:00:00Z",
		"to":"2026-08-07T00:00:00Z",
		"cursor":{"created_at":"2026-08-06T12:00:00Z"}
	}`)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
	if queryCalled || publishCalled {
		t.Fatalf("queryCalled=%v publishCalled=%v, want both false", queryCalled, publishCalled)
	}
}

func TestReplayPublishFailureReturnsUnavailableWithoutAdvancing(t *testing.T) {
	createdAt := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	rows := []fakeReplayRow{
		{id: "00000000-0000-0000-0000-000000000001", eventType: "invoice.paid", source: "billing", payload: []byte(`{"n":1}`), createdAt: createdAt},
		{id: "00000000-0000-0000-0000-000000000002", eventType: "invoice.paid", source: "billing", payload: []byte(`{"n":2}`), createdAt: createdAt},
		{id: "00000000-0000-0000-0000-000000000003", eventType: "invoice.paid", source: "billing", payload: []byte(`{"n":3}`), createdAt: createdAt},
	}
	var attempted []string

	handler := newReplayHandler(
		func(ctx context.Context, query string, args ...any) (replayRows, error) {
			return &fakeReplayRows{rows: rows}, nil
		},
		func(ctx context.Context, values map[string]interface{}) (string, error) {
			eventID := values["event_id"].(string)
			attempted = append(attempted, eventID)
			if eventID == "00000000-0000-0000-0000-000000000002" {
				return "", errors.New("redis unavailable")
			}
			return "1-0", nil
		},
	)

	rr := performReplayRequest(handler, `{
		"from":"2026-08-06T00:00:00Z",
		"to":"2026-08-07T00:00:00Z",
		"limit":3
	}`)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusServiceUnavailable)
	}
	want := []string{
		"00000000-0000-0000-0000-000000000001",
		"00000000-0000-0000-0000-000000000002",
	}
	if len(attempted) != len(want) {
		t.Fatalf("attempted = %v, want %v", attempted, want)
	}
	for i := range want {
		if attempted[i] != want[i] {
			t.Fatalf("attempted = %v, want %v", attempted, want)
		}
	}
}

type fakeReplayRow struct {
	id        string
	eventType string
	source    string
	payload   []byte
	createdAt time.Time
}

type fakeReplayRows struct {
	rows []fakeReplayRow
	idx  int
	err  error
}

func (r *fakeReplayRows) Close() {}

func (r *fakeReplayRows) Err() error {
	return r.err
}

func (r *fakeReplayRows) Next() bool {
	if r.idx >= len(r.rows) {
		return false
	}
	r.idx++
	return true
}

func (r *fakeReplayRows) Scan(dest ...any) error {
	row := r.rows[r.idx-1]
	*(dest[0].(*string)) = row.id
	*(dest[1].(*string)) = row.eventType
	*(dest[2].(*string)) = row.source
	*(dest[3].(*[]byte)) = row.payload
	*(dest[4].(*time.Time)) = row.createdAt
	return nil
}

func performReplayRequest(handler http.Handler, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/replay", strings.NewReader(body))
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

func decodeReplayResponse(t *testing.T, rr *httptest.ResponseRecorder, dst any) {
	t.Helper()
	if err := json.NewDecoder(rr.Body).Decode(dst); err != nil {
		t.Fatalf("decode response: %v", err)
	}
}
