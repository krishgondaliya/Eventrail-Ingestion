package deliveryworker

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/krishgondaliya/eventrail-ingestion/internal/delivery"
	"github.com/redis/go-redis/v9"
)

type deliveryFailureRecorder struct {
	steps       []string
	nextRetry   int
	delay       time.Duration
	scheduleErr error
	dlqErr      error
	ackErr      error
}

func (r *deliveryFailureRecorder) schedule(ctx context.Context, msg redis.XMessage, nextRetry int, delay time.Duration) error {
	r.steps = append(r.steps, "retry")
	r.nextRetry = nextRetry
	r.delay = delay
	return r.scheduleErr
}

func (r *deliveryFailureRecorder) writeDLQ(ctx context.Context, msg redis.XMessage, cause error) error {
	r.steps = append(r.steps, "dlq")
	return r.dlqErr
}

func (r *deliveryFailureRecorder) ack(ctx context.Context, messageID string) error {
	r.steps = append(r.steps, "ack")
	return r.ackErr
}

func TestDeliveryFailurePermanentGoesDirectlyToDLQ(t *testing.T) {
	recorder := &deliveryFailureRecorder{}
	err := handleDeliveryFailure(
		context.Background(),
		deliveryFailureMessage(),
		delivery.NewPermanentFailure(errors.New("bad payload")),
		0,
		5,
		100*time.Millisecond,
		recorder.schedule,
		recorder.writeDLQ,
		recorder.ack,
	)

	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	assertDeliveryFailureSteps(t, recorder.steps, []string{"dlq", "ack"})
}

func TestDeliveryFailureRetryableBelowLimitSchedulesRetry(t *testing.T) {
	recorder := &deliveryFailureRecorder{}
	err := handleDeliveryFailure(
		context.Background(),
		deliveryFailureMessage(),
		delivery.NewRetryableFailure(errors.New("timeout")),
		1,
		3,
		100*time.Millisecond,
		recorder.schedule,
		recorder.writeDLQ,
		recorder.ack,
	)

	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	assertDeliveryFailureSteps(t, recorder.steps, []string{"retry", "ack"})
	if recorder.nextRetry != 2 {
		t.Fatalf("expected next retry 2, got %d", recorder.nextRetry)
	}
	if recorder.delay != 200*time.Millisecond {
		t.Fatalf("expected exponential retry delay 200ms, got %s", recorder.delay)
	}
}

func TestDeliveryFailureRetryableAtLimitGoesToDLQ(t *testing.T) {
	recorder := &deliveryFailureRecorder{}
	err := handleDeliveryFailure(
		context.Background(),
		deliveryFailureMessage(),
		delivery.NewRetryableFailure(errors.New("timeout")),
		3,
		3,
		100*time.Millisecond,
		recorder.schedule,
		recorder.writeDLQ,
		recorder.ack,
	)

	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	assertDeliveryFailureSteps(t, recorder.steps, []string{"dlq", "ack"})
}

func TestDeliveryFailureRetryWriteFailurePreventsAcknowledgement(t *testing.T) {
	recorder := &deliveryFailureRecorder{scheduleErr: errors.New("redis zadd failed")}
	err := handleDeliveryFailure(
		context.Background(),
		deliveryFailureMessage(),
		delivery.NewRetryableFailure(errors.New("timeout")),
		0,
		1,
		100*time.Millisecond,
		recorder.schedule,
		recorder.writeDLQ,
		recorder.ack,
	)

	if err == nil {
		t.Fatal("expected retry scheduling error")
	}
	assertDeliveryFailureSteps(t, recorder.steps, []string{"retry"})
}

func TestDeliveryFailureDLQWriteFailurePreventsAcknowledgement(t *testing.T) {
	recorder := &deliveryFailureRecorder{dlqErr: errors.New("redis xadd failed")}
	err := handleDeliveryFailure(
		context.Background(),
		deliveryFailureMessage(),
		delivery.NewPermanentFailure(errors.New("bad payload")),
		0,
		5,
		100*time.Millisecond,
		recorder.schedule,
		recorder.writeDLQ,
		recorder.ack,
	)

	if err == nil {
		t.Fatal("expected DLQ write error")
	}
	assertDeliveryFailureSteps(t, recorder.steps, []string{"dlq"})
}

func TestDeliveryFailureAcknowledgementErrorIsSurfacedAfterRetryWrite(t *testing.T) {
	recorder := &deliveryFailureRecorder{ackErr: errors.New("xack failed")}
	err := handleDeliveryFailure(
		context.Background(),
		deliveryFailureMessage(),
		errors.New("unknown failure"),
		0,
		1,
		100*time.Millisecond,
		recorder.schedule,
		recorder.writeDLQ,
		recorder.ack,
	)

	if err == nil {
		t.Fatal("expected acknowledgement error")
	}
	assertDeliveryFailureSteps(t, recorder.steps, []string{"retry", "ack"})
}

func TestDeliveryFailureAcknowledgementErrorIsSurfacedAfterDLQWrite(t *testing.T) {
	recorder := &deliveryFailureRecorder{ackErr: errors.New("xack failed")}
	err := handleDeliveryFailure(
		context.Background(),
		deliveryFailureMessage(),
		delivery.NewPermanentFailure(errors.New("bad payload")),
		0,
		5,
		100*time.Millisecond,
		recorder.schedule,
		recorder.writeDLQ,
		recorder.ack,
	)

	if err == nil {
		t.Fatal("expected acknowledgement error")
	}
	assertDeliveryFailureSteps(t, recorder.steps, []string{"dlq", "ack"})
}

func TestAcknowledgementErrorIsSurfacedAfterSuccessfulDelivery(t *testing.T) {
	recorder := &deliveryFailureRecorder{ackErr: errors.New("xack failed")}
	err := acknowledgeDeliveredMessage(context.Background(), deliveryFailureMessage(), recorder.ack)

	if err == nil {
		t.Fatal("expected acknowledgement error")
	}
	assertDeliveryFailureSteps(t, recorder.steps, []string{"ack"})
}

func deliveryFailureMessage() redis.XMessage {
	return redis.XMessage{
		ID: "1-0",
		Values: map[string]interface{}{
			"event_type": "webhook",
			"retry":      "0",
		},
	}
}

func assertDeliveryFailureSteps(t *testing.T, got []string, want []string) {
	t.Helper()

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected steps %#v, got %#v", want, got)
	}
}
