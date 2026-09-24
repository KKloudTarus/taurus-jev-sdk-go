package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Sentinel errors for classification with errors.Is. Details live on [APIError],
// [ConnectionError] and [ResponseValidationError], reached with errors.As.
var (
	// ErrNoAPIKey is returned by New when no key is configured.
	ErrNoAPIKey = errors.New("jev: no API key")
	// ErrInvalidConfig is returned by New for an unusable option.
	ErrInvalidConfig = errors.New("jev: invalid configuration")
	// ErrInvalidRequest marks input rejected before any request is sent.
	ErrInvalidRequest = errors.New("jev: invalid request")
	// ErrInvalidResponse marks a success status whose body did not match the
	// documented schema.
	ErrInvalidResponse = errors.New("jev: invalid response")
	// ErrConnection marks a request that never produced an HTTP response.
	ErrConnection = errors.New("jev: connection failed")
	// ErrTimeout marks a call that ran out of time.
	ErrTimeout = errors.New("jev: request timed out")

	ErrBadRequest          = errors.New("jev: bad request")           // 400
	ErrAuthentication      = errors.New("jev: authentication failed") // 401
	ErrPermissionDenied    = errors.New("jev: permission denied")     // 403
	ErrNotFound            = errors.New("jev: not found")             // 404
	ErrUnprocessableEntity = errors.New("jev: unprocessable entity")  // 422
	ErrRateLimit           = errors.New("jev: rate limit exceeded")   // 429
	ErrOverloaded          = errors.New("jev: service overloaded")    // 529
	ErrInternalServer      = errors.New("jev: server error")          // 5xx
)

// statusOverloaded is TypeSafe's non-standard "service overloaded" status.
const statusOverloaded = 529

// APIError is an unsuccessful HTTP response. Classify it with errors.Is against
// the status sentinels, and read the details after errors.As.
type APIError struct {
	// Status is the HTTP status code.
	Status int
	// Message is the server's explanation, extracted from the error body.
	Message string
	// Body is the response body, truncated to 8 KiB. Any configured credential
	// is masked, as in Message.
	Body []byte
	// RequestID is the x-typesafe-request-id header, empty when absent.
	RequestID string
	// Endpoint is the method and URL, without credentials or query.
	Endpoint string
	// RetryAfter is the server's requested wait, zero when not supplied.
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	message := e.Message
	if message == "" {
		message = http.StatusText(e.Status)
	}
	head := "jev: " + strconv.Itoa(e.Status)
	if e.Endpoint != "" {
		head = "jev: " + e.Endpoint + ": " + strconv.Itoa(e.Status)
	}
	if message != "" {
		head += " " + message
	}
	if e.RequestID != "" {
		head += " (request_id=" + e.RequestID + ")"
	}
	return head
}

// Is reports whether this status matches a sentinel. A 529 matches both
// [ErrOverloaded] and [ErrInternalServer].
func (e *APIError) Is(target error) bool {
	switch target {
	case ErrBadRequest:
		return e.Status == http.StatusBadRequest
	case ErrAuthentication:
		return e.Status == http.StatusUnauthorized
	case ErrPermissionDenied:
		return e.Status == http.StatusForbidden
	case ErrNotFound:
		return e.Status == http.StatusNotFound
	case ErrUnprocessableEntity:
		return e.Status == http.StatusUnprocessableEntity
	case ErrRateLimit:
		return e.Status == http.StatusTooManyRequests
	case ErrOverloaded:
		return e.Status == statusOverloaded
	case ErrInternalServer:
		return e.Status >= http.StatusInternalServerError
	}
	return false
}

// ResponseValidationError is a success status whose body did not match the
// documented schema. FieldPath names the first offending field, so a truncated
// or degraded response is reported instead of decoding to a zero value.
type ResponseValidationError struct {
	// FieldPath is the dotted path to the offending field, such as
	// "answers.tone.confidence". It is empty when the whole body is unusable.
	FieldPath string
	// Status is the HTTP status code, which was a success.
	Status int
	// Body is the response body, truncated to 8 KiB and redacted.
	Body []byte
	// RequestID is the x-typesafe-request-id header, empty when absent.
	RequestID string
	// Endpoint is the method and URL, without credentials or query.
	Endpoint string

	cause error
}

func (e *ResponseValidationError) Error() string {
	subject := "the response body"
	if e.FieldPath != "" {
		subject = strconv.Quote(e.FieldPath)
	}
	message := "jev: " + e.Endpoint + ": invalid response data at " + subject
	if e.Endpoint == "" {
		message = "jev: invalid response data at " + subject
	}
	if e.cause != nil {
		message += ": " + e.cause.Error()
	}
	if e.RequestID != "" {
		message += " (request_id=" + e.RequestID + ")"
	}
	return message
}

// Is reports whether the target is [ErrInvalidResponse].
func (e *ResponseValidationError) Is(target error) bool { return target == ErrInvalidResponse }

// fieldError names a response field that is missing or the wrong shape. It
// travels up from a custom UnmarshalJSON, where the endpoint and request ID are
// not yet known.
type fieldError struct {
	path  string
	cause error
}

func (e *fieldError) Error() string {
	if e.cause == nil {
		return "missing or invalid field " + strconv.Quote(e.path)
	}
	return "invalid field " + strconv.Quote(e.path) + ": " + e.cause.Error()
}

func (e *fieldError) Unwrap() error { return e.cause }

func missingField(path string) error { return &fieldError{path: path} }

func asFieldError(err error, target **fieldError) bool {
	return err != nil && errors.As(err, target)
}

// ConnectionError is a request that never produced an HTTP response.
//
// Every string this type exposes is redacted, including the one reached through
// Unwrap. The chain stops at a sanitized node, so an error reporter that walks
// Unwrap cannot reach the raw transport error, while errors.Is and errors.As
// still see through to it.
type ConnectionError struct {
	// Endpoint is the method and URL, without credentials or query.
	Endpoint string
	// Timeout reports whether the failure was a deadline rather than a refusal.
	Timeout bool

	sanitized *sanitizedError
}

func (e *ConnectionError) Error() string {
	message := ""
	if e.sanitized != nil {
		message = e.sanitized.Error()
	}
	if e.Endpoint == "" {
		return "jev: " + message
	}
	return "jev: " + e.Endpoint + ": " + message
}

// Unwrap returns the sanitized transport error. Its message is redacted and it
// has no Unwrap of its own, so the raw error is not reachable by walking the
// chain. errors.Is and errors.As still reach the original cause.
func (e *ConnectionError) Unwrap() error {
	if e.sanitized == nil {
		return nil
	}
	return e.sanitized
}

// Is reports whether the target is [ErrConnection], or [ErrTimeout] when the
// failure was a deadline.
func (e *ConnectionError) Is(target error) bool {
	switch target {
	case ErrConnection:
		return true
	case ErrTimeout:
		return e.Timeout
	}
	return false
}

// sanitizedError carries a redacted message while delegating classification to
// the error it replaces. It deliberately has no Unwrap: a reporter that walks
// the chain stops here rather than reaching a message that may hold a
// credential.
type sanitizedError struct {
	message string
	cause   error
}

func (e *sanitizedError) Error() string { return e.message }

func (e *sanitizedError) Is(target error) bool { return errors.Is(e.cause, target) }

func (e *sanitizedError) As(target any) bool { return errors.As(e.cause, target) }

// deadlineError reports a call that ran out of time, keeping the failure that
// was in flight when the deadline passed.
type deadlineError struct {
	cause error // context.DeadlineExceeded or context.Canceled
	last  error
}

func (e *deadlineError) Error() string {
	if e.last == nil {
		return "jev: " + e.cause.Error()
	}
	return "jev: " + e.cause.Error() + " after " + e.last.Error()
}

func (e *deadlineError) Unwrap() []error { return []error{e.cause, e.last} }

func (e *deadlineError) Is(target error) bool {
	return target == ErrTimeout && errors.Is(e.cause, context.DeadlineExceeded)
}

// maxErrorBody caps how much of an error body is retained on the error value.
const maxErrorBody = 8 << 10

// maxRetryAfter caps a server-supplied wait before any policy clamps it, so a
// hostile or broken value cannot overflow a duration.
const maxRetryAfter = 24 * time.Hour

func newAPIError(status int, body []byte, headers http.Header, endpoint string) *APIError {
	// The message is extracted from the whole body, then the body is truncated.
	// Truncating first would cut valid JSON into unparseable text and bury the
	// one field an operator needs.
	return &APIError{
		Status:     status,
		Message:    extractMessage(body),
		Body:       truncateBytes(body, maxErrorBody),
		RequestID:  headers.Get(headerRequestID),
		Endpoint:   endpoint,
		RetryAfter: parseRetryAfter(headers),
	}
}

// truncateBytes copies rather than reslices. A reslice keeps the whole response
// backing array alive, which is up to maxResponseBody, while the field's
// documented size is 8 KiB.
func truncateBytes(body []byte, limit int) []byte {
	if len(body) <= limit {
		return bytes.Clone(body)
	}
	return bytes.Clone(body[:limit])
}

// extractMessage pulls a human message out of the shapes the API uses:
// {"error": ...}, {"message": ...} and {"detail": ...}, including the
// validation list of {"loc": [...], "msg": ...} entries.
func extractMessage(payload []byte) string {
	trimmed := strings.TrimSpace(string(payload))
	var body map[string]json.RawMessage
	if json.Unmarshal(payload, &body) != nil {
		return truncate(trimmed)
	}
	for _, key := range []string{"error", "message", "detail"} {
		raw, ok := body[key]
		if !ok {
			continue
		}
		if message := messageFrom(raw); message != "" {
			return truncate(message)
		}
	}
	return truncate(trimmed)
}

func messageFrom(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var nested struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &nested) == nil && nested.Message != "" {
		return nested.Message
	}
	var details []struct {
		Loc []any  `json:"loc"`
		Msg string `json:"msg"`
	}
	if json.Unmarshal(raw, &details) != nil {
		return ""
	}
	parts := make([]string, 0, len(details))
	for _, detail := range details {
		if detail.Msg == "" {
			continue
		}
		path := make([]string, 0, len(detail.Loc))
		for _, segment := range detail.Loc {
			// "body" is the request location, noise in a client-side message.
			if segment != "body" {
				path = append(path, fmt.Sprint(segment))
			}
		}
		if len(path) == 0 {
			parts = append(parts, detail.Msg)
		} else {
			parts = append(parts, strings.Join(path, ".")+": "+detail.Msg)
		}
	}
	return strings.Join(parts, "; ")
}

// truncate cuts on a rune boundary, so the result is always valid UTF-8 and
// survives a JSON encoder or a log pipeline.
func truncate(text string) string {
	const limit = 200
	if len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "…"
}

// parseRetryAfter reads retry-after-ms, then retry-after as seconds or as an
// HTTP date. It returns zero when no usable value is present, and never returns
// more than maxRetryAfter, so a hostile header cannot overflow a duration.
func parseRetryAfter(headers http.Header) time.Duration {
	if raw := strings.TrimSpace(headers.Get(headerRetryAfterMS)); raw != "" {
		if delay, ok := finiteDelay(raw, float64(time.Millisecond)); ok {
			return delay
		}
	}
	raw := strings.TrimSpace(headers.Get(headerRetryAfter))
	if raw == "" {
		return 0
	}
	if _, err := strconv.ParseFloat(raw, 64); err == nil {
		delay, _ := finiteDelay(raw, float64(time.Second))
		return delay
	}
	if date, err := http.ParseTime(raw); err == nil {
		if delay := time.Until(date); delay > 0 {
			return min(delay, maxRetryAfter)
		}
	}
	return 0
}

func finiteDelay(raw string, unit float64) (time.Duration, bool) {
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return 0, false
	}
	nanoseconds := value * unit
	if nanoseconds >= float64(maxRetryAfter) {
		return maxRetryAfter, true
	}
	return time.Duration(nanoseconds), true
}

// endpointOf renders a method and URL for error messages, dropping credentials,
// query and fragment so nothing sensitive reaches a log line.
func endpointOf(method, rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return method
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return method + " " + parsed.String()
}
