package delivery

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

type FailureKind string

const (
	FailureRetryable FailureKind = "retryable"
	FailurePermanent FailureKind = "permanent"
)

type Failure struct {
	Kind       FailureKind
	StatusCode int
	Cause      error
}

func (f *Failure) Error() string {
	if f == nil {
		return "delivery failure"
	}

	kind := string(f.Kind)
	if kind == "" {
		kind = "unknown"
	}

	if f.StatusCode != 0 {
		if f.Cause != nil {
			return fmt.Sprintf("%s delivery failure status=%d: %v", kind, f.StatusCode, f.Cause)
		}
		return fmt.Sprintf("%s delivery failure status=%d", kind, f.StatusCode)
	}
	if f.Cause != nil {
		return fmt.Sprintf("%s delivery failure: %v", kind, f.Cause)
	}
	return fmt.Sprintf("%s delivery failure", kind)
}

func (f *Failure) Unwrap() error {
	if f == nil {
		return nil
	}
	return f.Cause
}

func NewRetryableFailure(cause error) error {
	return &Failure{
		Kind:  FailureRetryable,
		Cause: cause,
	}
}

func NewPermanentFailure(cause error) error {
	return &Failure{
		Kind:  FailurePermanent,
		Cause: cause,
	}
}

func NewHTTPFailure(statusCode int, status string) error {
	kind := ClassifyHTTPStatus(statusCode)
	if kind == "" {
		return nil
	}

	statusText := strings.TrimSpace(status)
	if statusText == "" {
		statusText = http.StatusText(statusCode)
	}
	if statusText == "" {
		statusText = fmt.Sprintf("HTTP status %d", statusCode)
	}

	return &Failure{
		Kind:       kind,
		StatusCode: statusCode,
		Cause:      errors.New(statusText),
	}
}

func IsRetryable(err error) bool {
	var failure *Failure
	if !errors.As(err, &failure) {
		return false
	}
	return failure.Kind == FailureRetryable
}

func IsPermanent(err error) bool {
	var failure *Failure
	if !errors.As(err, &failure) {
		return false
	}
	return failure.Kind == FailurePermanent
}

func ClassifyHTTPStatus(statusCode int) FailureKind {
	switch statusCode {
	case http.StatusRequestTimeout, http.StatusTooEarly, http.StatusTooManyRequests:
		return FailureRetryable
	}
	if statusCode >= 500 && statusCode <= 599 {
		return FailureRetryable
	}
	if statusCode >= 400 && statusCode <= 499 {
		return FailurePermanent
	}
	return ""
}
