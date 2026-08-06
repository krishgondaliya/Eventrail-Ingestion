package deliveryworker

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestRepublishRetryMemberPublishesThenRemoves(t *testing.T) {
	var steps []string
	member := retryPumpMember()

	err := republishRetryMember(
		context.Background(),
		member,
		func(ctx context.Context, values map[string]interface{}) error {
			steps = append(steps, "publish")
			if values["event_type"] != "invoice.paid" {
				t.Fatalf("expected event_type invoice.paid, got %#v", values["event_type"])
			}
			return nil
		},
		func(ctx context.Context, gotMember string) error {
			steps = append(steps, "remove")
			if gotMember != member {
				t.Fatalf("expected member %q, got %q", member, gotMember)
			}
			return nil
		},
	)

	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	assertRetryPumpSteps(t, steps, []string{"publish", "remove"})
}

func TestRepublishRetryMemberPublishFailureDoesNotRemove(t *testing.T) {
	var steps []string
	publishErr := errors.New("xadd failed")

	err := republishRetryMember(
		context.Background(),
		retryPumpMember(),
		func(ctx context.Context, values map[string]interface{}) error {
			steps = append(steps, "publish")
			return publishErr
		},
		func(ctx context.Context, member string) error {
			steps = append(steps, "remove")
			return nil
		},
	)

	if err == nil {
		t.Fatal("expected publish error")
	}
	if !errors.Is(err, publishErr) {
		t.Fatalf("expected error to wrap publish error, got %v", err)
	}
	assertRetryPumpSteps(t, steps, []string{"publish"})
}

func TestRepublishRetryMemberRemoveFailureIsSurfaced(t *testing.T) {
	var steps []string
	removeErr := errors.New("zrem failed")

	err := republishRetryMember(
		context.Background(),
		retryPumpMember(),
		func(ctx context.Context, values map[string]interface{}) error {
			steps = append(steps, "publish")
			return nil
		},
		func(ctx context.Context, member string) error {
			steps = append(steps, "remove")
			return removeErr
		},
	)

	if err == nil {
		t.Fatal("expected remove error")
	}
	if !errors.Is(err, removeErr) {
		t.Fatalf("expected error to wrap remove error, got %v", err)
	}
	assertRetryPumpSteps(t, steps, []string{"publish", "remove"})
}

func TestRepublishRetryMemberInvalidJSONDoesNotPublishOrRemove(t *testing.T) {
	var steps []string

	err := republishRetryMember(
		context.Background(),
		`{`,
		func(ctx context.Context, values map[string]interface{}) error {
			steps = append(steps, "publish")
			return nil
		},
		func(ctx context.Context, member string) error {
			steps = append(steps, "remove")
			return nil
		},
	)

	if err == nil {
		t.Fatal("expected invalid JSON error")
	}
	assertRetryPumpSteps(t, steps, nil)
}

func retryPumpMember() string {
	return `{"event_type":"invoice.paid","source":"payments-service","payload":"{}","retry":"1"}`
}

func assertRetryPumpSteps(t *testing.T, got []string, want []string) {
	t.Helper()

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected steps %#v, got %#v", want, got)
	}
}
