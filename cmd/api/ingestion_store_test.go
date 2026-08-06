package main

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateExistingRequestHash(t *testing.T) {
	matchingHash := "e7b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

	tests := []struct {
		name          string
		existing      *string
		incoming      string
		wantErr       bool
		wantConflict  bool
		forbiddenText string
	}{
		{
			name:     "matching hash",
			existing: &matchingHash,
			incoming: matchingHash,
		},
		{
			name:          "different hash",
			existing:      &matchingHash,
			incoming:      "payload-content-must-not-leak",
			wantErr:       true,
			wantConflict:  true,
			forbiddenText: "payload-content-must-not-leak",
		},
		{
			name:          "null existing hash",
			existing:      nil,
			incoming:      "payload-content-must-not-leak",
			wantErr:       true,
			wantConflict:  true,
			forbiddenText: "payload-content-must-not-leak",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateExistingRequestHash(tt.existing, tt.incoming)

			if tt.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("expected nil error, got %v", err)
			}
			if tt.wantConflict && !errors.Is(err, ErrIdempotencyConflict) {
				t.Fatalf("expected ErrIdempotencyConflict, got %v", err)
			}
			if tt.forbiddenText != "" && err != nil && strings.Contains(err.Error(), tt.forbiddenText) {
				t.Fatalf("error leaked payload content: %q", err.Error())
			}
		})
	}
}
