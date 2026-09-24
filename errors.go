package jev

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Sentinel errors for classification with errors.Is. Details live on [APIError]
// and [ConnectionError], reached with errors.As.
var (
	// ErrNoAPIKey is returned by New when no key is configured.
	ErrNoAPIKey = errors.New("jev: no API key")
	// ErrInvalidConfig is returned by New for an unusable option.
	ErrInvalidConfig = errors.New("jev: invalid configuration")
	// ErrInvalidRequest marks input rejected before any request is sent.
	ErrInvalidRequest = errors.New("jev: invalid request")
	// ErrInvalidResponse marks a success status whose body could not be decoded.
	ErrInvalidResponse = errors.New("jev: invalid response")
	// ErrConnection marks a request that never produced an HTTP response.
	ErrConnection = errors.New("jev: connection failed")
	// ErrTimeout marks a request that exceeded its deadline.
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
	// Body is the raw response body, truncated to 8 KiB.
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
	parts := []string{"jev: " + strconv.Itoa(e.Status)}
	if e.Endpoint != "" {
		parts[0] = "jev: " + e.Endpoint + ": " + strconv.Itoa(e.Status)
	}
	if message != "" {
		parts = append(parts, message)
	}
	joined := strings.Join(parts, " ")
	if e.RequestID != "" {
		joined += " (request_id=" + e.RequestID + ")"
	}
	return joined
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

// ConnectionError is a request that never produced an HTTP response.
//
// Error is redacted: any configured credential is masked before the transport
// message is formatted. Unwrap returns the underlying transport error unchanged,
// so errors.Is against context.Canceled and friends keeps working; print the
// ConnectionError rather than the unwrapped error.
type ConnectionError struct {
	// Endpoint is the method and URL, without credentials or query.
	Endpoint string
	// Timeout reports whether the failure was a deadline rather than a refusal.
	Timeout bool
	// Err is the underlying transport error.
	Err error

	redacted string
}

func (e *ConnectionError) Error() string {
	message := e.redacted
	if message == "" && e.Err != nil {
		message = e.Err.Error()
	}
	if e.Endpoint == "" {
		return "jev: " + message
	}
	return "jev: " + e.Endpoint + ": " + message
}

func (e *ConnectionError) Unwrap() error { return e.Err }

func (e *ConnectionError) Is(target error) bool {
	switch target {
	case ErrConnection:
		return true
	case ErrTimeout:
		return e.Timeout
	}
	return false
}

// maxErrorBody caps how much of an error body is retained.
const maxErrorBody = 8 << 10

func newAPIError(status int, body []byte, headers http.Header, endpoint string) *APIError {
	if len(body) > maxErrorBody {
		body = body[:maxErrorBody]
	}
	return &APIError{
		Status:     status,
		Message:    extractMessage(body),
		Body:       body,
		RequestID:  headers.Get(headerRequestID),
		Endpoint:   endpoint,
		RetryAfter: parseRetryAfter(headers),
	}
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
			return message
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

func truncate(text string) string {
	const limit = 200
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "…"
}

// parseRetryAfter reads retry-after-ms, then retry-after as seconds or as an
// HTTP date. It returns zero when no usable value is present.
func parseRetryAfter(headers http.Header) time.Duration {
	if raw := strings.TrimSpace(headers.Get(headerRetryAfterMS)); raw != "" {
		if value, err := strconv.ParseFloat(raw, 64); err == nil && value >= 0 {
			return time.Duration(value * float64(time.Millisecond))
		}
	}
	raw := strings.TrimSpace(headers.Get(headerRetryAfter))
	if raw == "" {
		return 0
	}
	if value, err := strconv.ParseFloat(raw, 64); err == nil {
		if value < 0 {
			return 0
		}
		return time.Duration(value * float64(time.Second))
	}
	if date, err := http.ParseTime(raw); err == nil {
		if delay := time.Until(date); delay > 0 {
			return delay
		}
	}
	return 0
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
