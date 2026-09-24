package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Version is this client's release, sent in the User-Agent header.
const Version = "0.1.0"

var runtimeHeader = fmt.Sprintf("go/%s (%s; %s)",
	strings.TrimPrefix(runtime.Version(), "go"), runtime.GOOS, runtime.GOARCH)

// Client is a TypeSafe AI API client. It is safe for concurrent use and should
// be created once and shared, so connections are pooled.
type Client struct {
	apiKey     string
	baseURL    string
	model      string
	httpClient *http.Client
	retry      RetryPolicy
	headers    http.Header
	logger     *slog.Logger
	redactor   *redactor
	userAgent  string
}

// Option configures a [Client] in [New]. Explicit options win over environment
// variables.
type Option func(*Client)

// WithAPIKey sets the API key, overriding TYPESAFE_API_KEY.
func WithAPIKey(key string) Option {
	return func(c *Client) { c.apiKey = strings.TrimSpace(key) }
}

// WithBaseURL sets the API root, overriding TYPESAFE_BASE_URL.
func WithBaseURL(baseURL string) Option {
	return func(c *Client) { c.baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/") }
}

// WithModel sets the default model, overriding TYPESAFE_DEFAULT_MODEL.
func WithModel(model string) Option {
	return func(c *Client) { c.model = strings.TrimSpace(model) }
}

// WithHTTPClient supplies the HTTP client. The SDK does not close it.
func WithHTTPClient(httpClient *http.Client) Option {
	return func(c *Client) { c.httpClient = httpClient }
}

// WithTimeout sets the per-attempt timeout. It is ignored when [WithHTTPClient]
// supplies a client with its own timeout.
func WithTimeout(timeout time.Duration) Option {
	return func(c *Client) { c.httpClient = &http.Client{Timeout: timeout} }
}

// WithRetry replaces the retry policy. Use [NoRetry] to send one attempt.
func WithRetry(policy RetryPolicy) Option {
	return func(c *Client) { c.retry = policy }
}

// WithHeader adds a header to every request. Authentication, Accept and the SDK
// identification headers are set afterwards and cannot be overridden.
func WithHeader(name, value string) Option {
	return func(c *Client) { c.headers.Set(name, value) }
}

// WithLogger enables logging. Requests log at debug, retries at info. Headers
// are never logged and any configured credential is masked.
func WithLogger(logger *slog.Logger) Option {
	return func(c *Client) { c.logger = logger }
}

// New creates a client. It reads TYPESAFE_API_KEY, TYPESAFE_BASE_URL and
// TYPESAFE_DEFAULT_MODEL, each overridable by an option.
//
// It returns [ErrNoAPIKey] when no key is configured and [ErrInvalidConfig] for
// an unusable option.
func New(options ...Option) (*Client, error) {
	client := &Client{
		apiKey:     strings.TrimSpace(os.Getenv(APIKeyEnv)),
		baseURL:    envOr(BaseURLEnv, DefaultBaseURL),
		model:      envOr(ModelEnv, DefaultModel),
		httpClient: &http.Client{Timeout: 30 * time.Second},
		retry:      DefaultRetry(),
		headers:    http.Header{},
		userAgent:  sdkName + "/" + Version,
	}
	client.baseURL = strings.TrimRight(client.baseURL, "/")
	for _, option := range options {
		option(client)
	}
	if err := client.check(); err != nil {
		return nil, err
	}
	client.redactor = newRedactor(client.apiKey)
	return client, nil
}

func (c *Client) check() error {
	if c.apiKey == "" {
		return fmt.Errorf("%w: pass WithAPIKey or set %s", ErrNoAPIKey, APIKeyEnv)
	}
	for _, char := range c.apiKey {
		if char > 126 || char < 33 {
			return fmt.Errorf("%w: API key must be printable ASCII without whitespace", ErrInvalidConfig)
		}
	}
	parsed, err := url.Parse(c.baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("%w: base URL %q is not an absolute URL", ErrInvalidConfig, c.baseURL)
	}
	if c.model == "" {
		return fmt.Errorf("%w: model must not be empty", ErrInvalidConfig)
	}
	if c.httpClient == nil {
		return fmt.Errorf("%w: HTTP client must not be nil", ErrInvalidConfig)
	}
	return c.retry.validate()
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

// CallOption overrides client configuration for a single call. The interface is
// closed; use the Using* constructors.
type CallOption interface {
	apply(*call)
}

type callOption func(*call)

func (f callOption) apply(c *call) { f(c) }

type call struct {
	model     string
	retry     *RetryPolicy
	timeout   time.Duration
	headers   http.Header
	extraBody map[string]any
}

// UsingModel overrides the model for this call.
func UsingModel(model string) CallOption {
	return callOption(func(c *call) { c.model = model })
}

// UsingRetry overrides the retry policy for this call.
func UsingRetry(policy RetryPolicy) CallOption {
	return callOption(func(c *call) { c.retry = &policy })
}

// UsingTimeout bounds this call, including its retries.
func UsingTimeout(timeout time.Duration) CallOption {
	return callOption(func(c *call) { c.timeout = timeout })
}

// UsingHeader adds a header to this call.
func UsingHeader(name, value string) CallOption {
	return callOption(func(c *call) {
		if c.headers == nil {
			c.headers = http.Header{}
		}
		c.headers.Set(name, value)
	})
}

// UsingExtraBody merges top-level fields into the request body after state,
// model and questions are set. Last write wins, and values replace rather than
// merge. Use it to reach a field this version does not model.
func UsingExtraBody(fields map[string]any) CallOption {
	return callOption(func(c *call) { c.extraBody = fields })
}

func newCall(options []CallOption) *call {
	result := &call{}
	for _, option := range options {
		option.apply(result)
	}
	return result
}

// SystemOne answers named questions about state.
//
// State is the content every question refers to: a string, or any value that
// encodes to a JSON object or array. Each answer is keyed by its question name.
//
// Errors are classified with errors.Is against [ErrRateLimit], [ErrTimeout] and
// the other sentinels; details come from errors.As into [*APIError] or
// [*ConnectionError].
func (c *Client) SystemOne(ctx context.Context, state any, questions Questions, options ...CallOption) (*SystemOneResponse, error) {
	response := &SystemOneResponse{}
	requestID, err := c.systemOne(ctx, state, questions, options, response)
	if err != nil {
		return nil, err
	}
	response.RequestID = requestID
	return response, nil
}

// SystemOneAs answers named questions and decodes the body into T, for a
// response shape this version does not model or a narrower one you declare
// yourself. It is a function rather than a method because Go methods take no
// type parameters.
func SystemOneAs[T any](ctx context.Context, client *Client, state any, questions Questions, options ...CallOption) (T, error) {
	var result T
	if _, err := client.systemOne(ctx, state, questions, options, &result); err != nil {
		var zero T
		return zero, err
	}
	return result, nil
}

func (c *Client) systemOne(ctx context.Context, state any, questions Questions, options []CallOption, out any) (string, error) {
	if state == nil {
		return "", fmt.Errorf("%w: state must not be nil", ErrInvalidRequest)
	}
	if err := questions.validate(); err != nil {
		return "", err
	}
	current := newCall(options)
	model := c.model
	if current.model != "" {
		model = current.model
	}
	body := map[string]any{"state": state, "model": model, "questions": questions}
	for name, value := range current.extraBody {
		body[name] = value
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("%w: encoding body: %s", ErrInvalidRequest, c.redactor.string(err.Error()))
	}
	return c.do(ctx, http.MethodPost, systemOnePath, encoded, current, out)
}

// Models lists the models available to the account.
func (c *Client) Models(ctx context.Context, options ...CallOption) ([]Model, error) {
	var result struct {
		Models []Model `json:"models"`
	}
	if _, err := c.do(ctx, http.MethodGet, modelsPath, nil, newCall(options), &result); err != nil {
		return nil, err
	}
	return result.Models, nil
}

// do runs the retry loop around attempt and decodes the successful body.
func (c *Client) do(ctx context.Context, method, path string, body []byte, current *call, out any) (string, error) {
	if current.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, current.timeout)
		defer cancel()
	}
	policy := c.retry
	if current.retry != nil {
		policy = *current.retry
	}
	if err := policy.validate(); err != nil {
		return "", err
	}

	started := time.Now()
	var lastErr error
	for attempt := 0; ; attempt++ {
		requestID, retryable, err := c.attempt(ctx, method, path, body, current, policy, attempt, out)
		if err == nil {
			return requestID, nil
		}
		lastErr = err
		if !retryable || attempt >= policy.MaxRetries || ctx.Err() != nil {
			return "", lastErr
		}
		delay, within := policy.delayFor(attempt, err, time.Since(started))
		if !within {
			return "", lastErr
		}
		c.log(ctx, slog.LevelInfo, "retrying request",
			"method", method, "path", path, "attempt", attempt+1, "delay", delay, "cause", err)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", lastErr
		case <-timer.C:
		}
	}
}

// attempt sends one request. It reports the request ID, whether the failure is
// worth retrying, and the error.
func (c *Client) attempt(ctx context.Context, method, path string, body []byte, current *call, policy RetryPolicy, attempt int, out any) (string, bool, error) {
	endpoint := endpointOf(method, c.baseURL+path)

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return "", false, fmt.Errorf("%w: %s", ErrInvalidRequest, c.redactor.string(err.Error()))
	}
	for name, values := range c.headers {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
	for name, values := range current.headers {
		for _, value := range values {
			request.Header.Set(name, value)
		}
	}
	request.Header.Set(headerAuthorization, "Bearer "+c.apiKey)
	request.Header.Set(headerAccept, contentTypeJSON)
	request.Header.Set(headerUserAgent, c.userAgent)
	request.Header.Set(headerSDK, c.userAgent)
	request.Header.Set(headerRuntime, runtimeHeader)
	if body != nil {
		request.Header.Set(headerContentType, contentTypeJSON)
	}
	if attempt > 0 {
		request.Header.Set(headerRetryCount, strconv.Itoa(attempt))
	}

	c.log(ctx, slog.LevelDebug, "sending request", "method", method, "endpoint", endpoint, "bytes", len(body))
	started := time.Now()
	response, err := c.httpClient.Do(request)
	if err != nil {
		timeout := isTimeout(err)
		connErr := &ConnectionError{
			Endpoint: endpoint,
			Timeout:  timeout,
			Err:      err,
			redacted: c.redactor.string(err.Error()),
		}
		c.log(ctx, slog.LevelDebug, "request failed", "endpoint", endpoint, "timeout", timeout, "cause", connErr)
		// A cancelled context is the caller's decision, never retried.
		return "", policy.RetryConnection && ctx.Err() == nil, connErr
	}
	defer response.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBody))
	if err != nil {
		connErr := &ConnectionError{
			Endpoint: endpoint,
			Timeout:  isTimeout(err),
			Err:      err,
			redacted: c.redactor.string(err.Error()),
		}
		return "", policy.RetryConnection && ctx.Err() == nil, connErr
	}
	requestID := response.Header.Get(headerRequestID)
	c.log(ctx, slog.LevelDebug, "received response",
		"endpoint", endpoint, "status", response.StatusCode,
		"duration", time.Since(started), "request_id", requestID)

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		apiErr := newAPIError(response.StatusCode, payload, response.Header, endpoint)
		apiErr.Message = c.redactor.string(apiErr.Message)
		retryable := policy.RetryStatus != nil && policy.RetryStatus(response.StatusCode)
		return "", retryable, apiErr
	}
	if out != nil {
		if err := json.Unmarshal(payload, out); err != nil {
			return "", false, fmt.Errorf("%w: %s: %s (request_id=%s)",
				ErrInvalidResponse, endpoint, c.redactor.string(err.Error()), requestID)
		}
	}
	return requestID, false, nil
}

// maxResponseBody caps how much of a response is read into memory.
const maxResponseBody = 32 << 20

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return netErr.Timeout()
	}
	return false
}

func (c *Client) log(ctx context.Context, level slog.Level, message string, args ...any) {
	if c.logger == nil {
		return
	}
	c.logger.Log(ctx, level, message, args...)
}
