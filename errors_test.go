package jev

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestAPIErrorSentinels(t *testing.T) {
	cases := []struct {
		status  int
		matches error
		misses  error
	}{
		{http.StatusBadRequest, ErrBadRequest, ErrNotFound},
		{http.StatusUnauthorized, ErrAuthentication, ErrPermissionDenied},
		{http.StatusForbidden, ErrPermissionDenied, ErrAuthentication},
		{http.StatusNotFound, ErrNotFound, ErrBadRequest},
		{http.StatusUnprocessableEntity, ErrUnprocessableEntity, ErrBadRequest},
		{http.StatusTooManyRequests, ErrRateLimit, ErrInternalServer},
		{http.StatusInternalServerError, ErrInternalServer, ErrRateLimit},
		{statusOverloaded, ErrOverloaded, ErrRateLimit},
	}
	for _, testCase := range cases {
		err := error(&APIError{Status: testCase.status})
		if !errors.Is(err, testCase.matches) {
			t.Errorf("status %d does not match %v", testCase.status, testCase.matches)
		}
		if errors.Is(err, testCase.misses) {
			t.Errorf("status %d wrongly matches %v", testCase.status, testCase.misses)
		}
	}
	// 529 is an overload, and an overload is still a server error.
	if !errors.Is(&APIError{Status: statusOverloaded}, ErrInternalServer) {
		t.Error("529 should also match ErrInternalServer")
	}
}

func TestAPIErrorMessage(t *testing.T) {
	err := &APIError{Status: 429, Message: "slow down", Endpoint: "POST https://api.typesafe.ai/v1/systemone", RequestID: "req_1"}
	want := "jev: POST https://api.typesafe.ai/v1/systemone: 429 slow down (request_id=req_1)"
	if err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
	// An empty body still names the status.
	bare := &APIError{Status: 503}
	if bare.Error() != "jev: 503 Service Unavailable" {
		t.Errorf("Error() = %q", bare.Error())
	}
}

func TestConnectionErrorClassification(t *testing.T) {
	inner := errors.New("dial tcp: connection refused")
	sanitized := &sanitizedError{message: "dial tcp: connection refused", cause: inner}
	err := error(&ConnectionError{Endpoint: "GET https://api.typesafe.ai/v1/models", sanitized: sanitized})
	if !errors.Is(err, ErrConnection) {
		t.Error("not classified as a connection failure")
	}
	if errors.Is(err, ErrTimeout) {
		t.Error("a refusal is not a timeout")
	}
	if !errors.Is(err, inner) {
		t.Error("the transport error is no longer reachable by errors.Is")
	}
	timeout := error(&ConnectionError{Timeout: true, sanitized: sanitized})
	if !errors.Is(timeout, ErrTimeout) || !errors.Is(timeout, ErrConnection) {
		t.Error("a timeout is both a timeout and a connection failure")
	}
}

// The chain must stop at the sanitized node: a reporter that walks Unwrap
// cannot be allowed to reach a message that may still hold a credential.
func TestUnwrapStopsAtTheSanitizedNode(t *testing.T) {
	inner := errors.New("dial tcp https://sk-live-abcdef123456@host: refused")
	err := error(&ConnectionError{
		Endpoint:  "POST https://api.typesafe.ai/v1/systemone",
		sanitized: &sanitizedError{message: "dial tcp https://***@host: refused", cause: inner},
	})
	unwrapped := errors.Unwrap(err)
	if unwrapped == nil {
		t.Fatal("Unwrap returned nil")
	}
	if strings.Contains(unwrapped.Error(), "sk-live-abcdef123456") {
		t.Errorf("the unwrapped error leaked the credential: %v", unwrapped)
	}
	if errors.Unwrap(unwrapped) != nil {
		t.Error("the chain continues past the sanitized node")
	}
	if !errors.Is(err, inner) {
		t.Error("classification through the sanitized node broke")
	}
}

func TestDeadlineErrorIsATimeout(t *testing.T) {
	last := &APIError{Status: 429}
	err := error(&deadlineError{cause: context.DeadlineExceeded, last: last})
	for _, target := range []error{ErrTimeout, context.DeadlineExceeded} {
		if !errors.Is(err, target) {
			t.Errorf("not classified as %v", target)
		}
	}
	// The failure in flight when the deadline passed stays reachable.
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 429 {
		t.Error("the last failure is no longer reachable")
	}
	// A cancellation is not a timeout.
	cancelled := error(&deadlineError{cause: context.Canceled, last: last})
	if errors.Is(cancelled, ErrTimeout) {
		t.Error("a cancelled context reported itself as a timeout")
	}
	if !errors.Is(cancelled, context.Canceled) {
		t.Error("the cancellation cause is not reachable")
	}
}

func TestResponseValidationError(t *testing.T) {
	err := error(&ResponseValidationError{
		FieldPath: "answers.tone.confidence", Status: 200,
		RequestID: "req_9", Endpoint: "POST https://api.typesafe.ai/v1/systemone",
	})
	if !errors.Is(err, ErrInvalidResponse) {
		t.Error("not classified as ErrInvalidResponse")
	}
	var validation *ResponseValidationError
	if !errors.As(err, &validation) || validation.FieldPath != "answers.tone.confidence" {
		t.Errorf("field path lost: %+v", validation)
	}
	if !strings.Contains(err.Error(), "answers.tone.confidence") || !strings.Contains(err.Error(), "req_9") {
		t.Errorf("Error() = %q", err.Error())
	}
}

func TestExtractMessage(t *testing.T) {
	cases := []struct {
		body string
		want string
	}{
		{`{"error": "bad key"}`, "bad key"},
		{`{"error": {"message": "nested"}}`, "nested"},
		{`{"message": "plain"}`, "plain"},
		{`{"detail": "detail string"}`, "detail string"},
		{`{"detail": [{"loc": ["body", "questions", "urgency", "criteria"], "msg": "Field required"}]}`,
			"questions.urgency.criteria: Field required"},
		{`{"detail": [{"loc": ["body"], "msg": "one"}, {"loc": ["body", "state"], "msg": "two"}]}`,
			"one; state: two"},
		{`not json at all`, "not json at all"},
		{``, ""},
	}
	for _, testCase := range cases {
		if got := extractMessage([]byte(testCase.body)); got != testCase.want {
			t.Errorf("extractMessage(%s) = %q, want %q", testCase.body, got, testCase.want)
		}
	}
}

func TestParseRetryAfter(t *testing.T) {
	headers := func(pairs ...string) http.Header {
		result := http.Header{}
		for i := 0; i < len(pairs); i += 2 {
			result.Set(pairs[i], pairs[i+1])
		}
		return result
	}
	cases := []struct {
		name    string
		headers http.Header
		want    time.Duration
	}{
		{"milliseconds win", headers(headerRetryAfterMS, "250", headerRetryAfter, "60"), 250 * time.Millisecond},
		{"seconds", headers(headerRetryAfter, "2"), 2 * time.Second},
		{"fractional seconds", headers(headerRetryAfter, "1.5"), 1500 * time.Millisecond},
		{"negative is ignored", headers(headerRetryAfter, "-5"), 0},
		{"garbage is ignored", headers(headerRetryAfter, "soon"), 0},
		{"absent", headers(), 0},
	}
	for _, testCase := range cases {
		if got := parseRetryAfter(testCase.headers); got != testCase.want {
			t.Errorf("%s: got %v, want %v", testCase.name, got, testCase.want)
		}
	}
	// An HTTP date in the future becomes a positive delay.
	future := headers(headerRetryAfter, time.Now().Add(30*time.Second).UTC().Format(http.TimeFormat))
	if delay := parseRetryAfter(future); delay <= 0 || delay > 31*time.Second {
		t.Errorf("http date delay = %v", delay)
	}
	// A date in the past is not a negative wait.
	past := headers(headerRetryAfter, time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat))
	if delay := parseRetryAfter(past); delay != 0 {
		t.Errorf("past date delay = %v, want 0", delay)
	}
}

func TestEndpointOfStripsCredentialsAndQuery(t *testing.T) {
	got := endpointOf("POST", "https://user:secret@api.typesafe.ai/v1/systemone?token=abc#frag")
	want := "POST https://api.typesafe.ai/v1/systemone"
	if got != want {
		t.Errorf("endpointOf = %q, want %q", got, want)
	}
}
