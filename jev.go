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
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Version is this client's release, sent in the User-Agent header.
const Version = "0.1.0"

var runtimeHeader = fmt.Sprintf("go/%s (%s; %s)",
	strings.TrimPrefix(runtime.Version(), "go"), runtime.GOOS, runtime.GOARCH)

// DefaultTimeout bounds one HTTP attempt when no timeout is configured.
const DefaultTimeout = 30 * time.Second

// Connection pool sizing. The stdlib default of two idle connections per host
// forces a fresh TCP and TLS handshake on most requests once more than two
// calls are in flight, which is the normal state for a service on a request
// path.
const (
	defaultMaxIdleConns        = 256
	defaultMaxIdleConnsPerHost = 128
	defaultIdleConnTimeout     = 90 * time.Second
)

// maxResponseBody caps how much of a response is read into memory.
const maxResponseBody = 32 << 20

// Client is a TypeSafe AI API client. It is safe for concurrent use and should
// be created once and shared, so connections are pooled.
//
// Its String and LogValue methods mask the API key, so printing or logging a
// client cannot disclose the credential.
type Client struct {
	apiKey             string
	authHeader         string
	baseURL            string
	model              string
	httpClient         *http.Client
	suppliedHTTPClient bool
	timeout            *time.Duration
	retry              RetryPolicy
	headers            http.Header
	logger             *slog.Logger
	redactor           *redactor
	userAgent          string
	endpoints          map[string]string
}

// String renders the client without its credential.
func (c Client) String() string {
	return fmt.Sprintf("jev.Client{baseURL:%s model:%s userAgent:%s apiKey:***}", c.baseURL, c.model, c.userAgent)
}

// GoString renders the client without its credential, for the %#v verb.
func (c Client) GoString() string { return c.String() }

// LogValue renders the client without its credential, for log/slog.
func (c Client) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("baseURL", c.baseURL),
		slog.String("model", c.model),
		slog.String("userAgent", c.userAgent),
		slog.String("apiKey", "***"),
	)
}

// Option configures a [Client] in [New]. Explicit options win over environment
// variables.
type Option func(*Client)

// WithAPIKey sets the API key, overriding TYPESAFE_API_KEY.
func WithAPIKey(key string) Option {
	return func(c *Client) { c.apiKey = strings.TrimSpace(key) }
}

// WithBaseURL sets the API root, overriding TYPESAFE_BASE_URL. It must be an
// absolute https URL, or http for a loopback host, and must carry no
// credentials, query or fragment.
func WithBaseURL(baseURL string) Option {
	return func(c *Client) { c.baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/") }
}

// WithModel sets the default model, overriding TYPESAFE_DEFAULT_MODEL.
func WithModel(model string) Option {
	return func(c *Client) { c.model = strings.TrimSpace(model) }
}

// WithHTTPClient supplies the HTTP client. The SDK does not close it and does
// not change its transport, timeout or redirect policy, so a client supplied
// here is responsible for its own connection pool and for not following a
// redirect that would carry the Authorization header to another host.
func WithHTTPClient(httpClient *http.Client) Option {
	return func(c *Client) {
		c.httpClient = httpClient
		c.suppliedHTTPClient = true
	}
}

// WithTimeout bounds each HTTP attempt. It composes with [WithHTTPClient]
// rather than replacing it: the timeout is applied through the request context,
// so a supplied client keeps its transport, and its own Timeout still applies
// alongside this one. Use RetryPolicy.Budget to bound a call across its retries.
//
// The value must be positive.
func WithTimeout(timeout time.Duration) Option {
	return func(c *Client) { c.timeout = &timeout }
}

// WithRetry replaces the retry policy. Use [NoRetry] to send one attempt.
func WithRetry(policy RetryPolicy) Option {
	return func(c *Client) { c.retry = policy }
}

// WithHeader adds a header to every request. Authentication, Accept, the SDK
// identification headers and the SDK's own retry-count header are set
// afterwards and cannot be overridden.
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
		apiKey:    strings.TrimSpace(os.Getenv(APIKeyEnv)),
		baseURL:   envOr(BaseURLEnv, DefaultBaseURL),
		model:     envOr(ModelEnv, DefaultModel),
		retry:     DefaultRetry(),
		headers:   http.Header{},
		userAgent: sdkName + "/" + Version,
	}
	client.baseURL = strings.TrimRight(client.baseURL, "/")
	for _, option := range options {
		option(client)
	}
	if client.httpClient == nil && !client.suppliedHTTPClient {
		client.httpClient = newHTTPClient()
	}
	if err := client.check(); err != nil {
		return nil, err
	}
	client.redactor = newRedactor(client.apiKey)
	client.authHeader = "Bearer " + client.apiKey
	client.endpoints = map[string]string{
		systemOnePath: endpointOf(http.MethodPost, client.baseURL+systemOnePath),
		modelsPath:    endpointOf(http.MethodGet, client.baseURL+modelsPath),
	}
	return client, nil
}

// newHTTPClient builds the SDK's own client: a pool sized for a service on a
// request path, and a redirect policy that never re-sends the Authorization
// header. The stdlib default follows redirects and re-sends that header to any
// subdomain of the same host, ignoring scheme and port.
func newHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = defaultMaxIdleConns
	transport.MaxIdleConnsPerHost = defaultMaxIdleConnsPerHost
	transport.IdleConnTimeout = defaultIdleConnTimeout
	return &http.Client{
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
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
	if err := checkBaseURL(c.baseURL); err != nil {
		return err
	}
	if c.model == "" {
		return fmt.Errorf("%w: model must not be empty", ErrInvalidConfig)
	}
	if c.httpClient == nil {
		return fmt.Errorf("%w: HTTP client must not be nil", ErrInvalidConfig)
	}
	if c.timeout != nil && *c.timeout <= 0 {
		return fmt.Errorf("%w: timeout must be positive", ErrInvalidConfig)
	}
	if c.timeout == nil && !c.suppliedHTTPClient {
		fallback := DefaultTimeout
		c.timeout = &fallback
	}
	return c.retry.validate()
}

// checkBaseURL rejects a root that would send the credential in cleartext or
// relocate the request. The path is joined by concatenation, so a query or
// fragment here would move every call to the host root.
func checkBaseURL(baseURL string) error {
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("%w: base URL %q is not an absolute URL", ErrInvalidConfig, baseURL)
	}
	switch {
	case parsed.User != nil:
		return fmt.Errorf("%w: base URL must not contain credentials", ErrInvalidConfig)
	case parsed.RawQuery != "" || parsed.Fragment != "":
		return fmt.Errorf("%w: base URL must not contain a query or fragment", ErrInvalidConfig)
	case parsed.Scheme == "https":
		return nil
	case parsed.Scheme == "http" && isLoopback(parsed.Hostname()):
		return nil
	}
	return fmt.Errorf("%w: base URL %q must use https, or http on a loopback host", ErrInvalidConfig, baseURL)
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
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
	timeout   *time.Duration
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

// UsingTimeout bounds each HTTP attempt of this call. The value must be
// positive. Use RetryPolicy.Budget, or the context, to bound the whole call.
func UsingTimeout(timeout time.Duration) CallOption {
	return callOption(func(c *call) { c.timeout = &timeout })
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

func newCall(options []CallOption) (*call, error) {
	result := &call{}
	for _, option := range options {
		option.apply(result)
	}
	if result.timeout != nil && *result.timeout <= 0 {
		return nil, fmt.Errorf("%w: timeout must be positive", ErrInvalidRequest)
	}
	if result.retry != nil {
		if err := result.retry.validate(); err != nil {
			return nil, err
		}
	}
	return result, nil
}

// SystemOne answers named questions about state.
//
// State is the content every question refers to: a string, or any value that
// encodes to a JSON object or array. Each answer is keyed by its question name.
//
// Errors are classified with errors.Is against [ErrRateLimit], [ErrTimeout] and
// the other sentinels; details come from errors.As into [*APIError],
// [*ConnectionError] or [*ResponseValidationError].
func (c *Client) SystemOne(ctx context.Context, state any, questions Questions, options ...CallOption) (*SystemOneResponse, error) {
	response := &SystemOneResponse{}
	if err := c.systemOne(ctx, state, questions, options, response); err != nil {
		return nil, err
	}
	return response, nil
}

// SystemOneAs answers named questions and decodes the body into T, for a
// response shape this version does not model or a narrower one you declare
// yourself. It is a function rather than a method because Go methods take no
// type parameters.
func SystemOneAs[T any](ctx context.Context, client *Client, state any, questions Questions, options ...CallOption) (T, error) {
	var result T
	if err := client.systemOne(ctx, state, questions, options, &result); err != nil {
		var zero T
		return zero, err
	}
	return result, nil
}

func (c *Client) systemOne(ctx context.Context, state any, questions Questions, options []CallOption, out any) error {
	if isNil(state) {
		return fmt.Errorf("%w: state must not be nil", ErrInvalidRequest)
	}
	if err := questions.validate(); err != nil {
		return err
	}
	current, err := newCall(options)
	if err != nil {
		return err
	}
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
		return fmt.Errorf("%w: encoding body: %s", ErrInvalidRequest, c.redactor.string(err.Error()))
	}
	meta, err := c.do(ctx, http.MethodPost, systemOnePath, encoded, current, out)
	if err != nil {
		return err
	}
	if response, ok := out.(*SystemOneResponse); ok {
		response.RequestID, response.Status = meta.requestID, meta.status
		response.Header, response.RawBody = meta.header, meta.body
	}
	return nil
}

// Models lists the models available to the account.
func (c *Client) Models(ctx context.Context, options ...CallOption) ([]Model, error) {
	current, err := newCall(options)
	if err != nil {
		return nil, err
	}
	if current.extraBody != nil || current.model != "" {
		return nil, fmt.Errorf("%w: the models endpoint takes no model or body fields", ErrInvalidRequest)
	}
	var result modelList
	if _, err := c.do(ctx, http.MethodGet, modelsPath, nil, current, &result); err != nil {
		return nil, err
	}
	return result.Models, nil
}

// isNil reports whether a value is nil, including a typed nil held in a
// non-nil interface, which a plain `== nil` misses.
func isNil(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice, reflect.UnsafePointer:
		return reflected.IsNil()
	}
	return false
}

// responseMeta carries the transport facts a caller may need for support or
// debugging.
type responseMeta struct {
	requestID string
	status    int
	header    http.Header
	body      json.RawMessage
}

// do runs the retry loop around attempt and decodes the successful body.
func (c *Client) do(ctx context.Context, method, path string, body []byte, current *call, out any) (responseMeta, error) {
	policy := c.retry
	if current.retry != nil {
		policy = *current.retry
	}

	started := time.Now()
	var lastErr error
	for attempt := 0; ; attempt++ {
		meta, retryable, err := c.attempt(ctx, method, path, body, current, policy, attempt, out)
		if err == nil {
			return meta, nil
		}
		lastErr = err
		// A definitive response wins even when the context expired in the same
		// tick: the server answered. An expired context is reported below,
		// where the loop would otherwise wait.
		if !retryable || attempt >= policy.MaxRetries {
			return responseMeta{}, lastErr
		}
		delay, within := policy.delayFor(attempt, err, time.Since(started))
		if !within {
			return responseMeta{}, lastErr
		}
		c.log(ctx, slog.LevelInfo, "retrying request",
			"method", method, "path", path, "attempt", attempt+1, "delay", delay, "cause", err)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return responseMeta{}, &deadlineError{cause: ctx.Err(), last: lastErr}
		case <-timer.C:
		}
	}
}

// attempt sends one request. It reports the transport metadata, whether the
// failure is worth retrying, and the error.
func (c *Client) attempt(ctx context.Context, method, path string, body []byte, current *call, policy RetryPolicy, attempt int, out any) (responseMeta, bool, error) {
	endpoint := c.endpoints[path]

	timeout := c.timeout
	if current.timeout != nil {
		timeout = current.timeout
	}
	if timeout != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *timeout)
		defer cancel()
	}

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return responseMeta{}, false, fmt.Errorf("%w: %s", ErrInvalidRequest, c.redactor.string(err.Error()))
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
	// The retry count is the SDK's own telemetry; a caller must not set it.
	request.Header.Del(headerRetryCount)
	request.Header.Set(headerAuthorization, c.authHeader)
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
		return responseMeta{}, policy.RetryConnection && ctx.Err() == nil, c.connectionError(endpoint, err)
	}
	defer func() { _ = response.Body.Close() }()

	// One byte past the cap distinguishes a truncated body from an exact fit.
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBody+1))
	if err != nil {
		return responseMeta{}, policy.RetryConnection && ctx.Err() == nil, c.connectionError(endpoint, err)
	}
	requestID := response.Header.Get(headerRequestID)
	c.log(ctx, slog.LevelDebug, "received response",
		"endpoint", endpoint, "status", response.StatusCode,
		"duration", time.Since(started), "request_id", requestID)

	if len(payload) > maxResponseBody {
		return responseMeta{}, false, &ResponseValidationError{
			Status: response.StatusCode, RequestID: requestID, Endpoint: endpoint,
			cause: fmt.Errorf("response body exceeds %d bytes", maxResponseBody),
		}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		apiErr := newAPIError(response.StatusCode, payload, response.Header, endpoint)
		apiErr.Message = c.redactor.string(apiErr.Message)
		apiErr.Body = c.redactor.bytes(apiErr.Body)
		retryable := policy.RetryStatus != nil && policy.RetryStatus(response.StatusCode)
		return responseMeta{}, retryable, apiErr
	}

	meta := responseMeta{requestID: requestID, status: response.StatusCode, header: response.Header, body: payload}
	if out == nil {
		return meta, false, nil
	}
	// encoding/json treats a null payload as a no-op, which would leave the
	// caller holding a zero value from a response that carried nothing.
	if trimmed := bytes.TrimSpace(payload); len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return responseMeta{}, false, &ResponseValidationError{
			Status: response.StatusCode, Body: c.redactor.bytes(truncateBytes(payload, maxErrorBody)),
			RequestID: requestID, Endpoint: endpoint,
			cause: errors.New("the body was empty or null"),
		}
	}
	if err := json.Unmarshal(payload, out); err != nil {
		validation := &ResponseValidationError{
			Status: response.StatusCode, Body: c.redactor.bytes(truncateBytes(payload, maxErrorBody)),
			RequestID: requestID, Endpoint: endpoint,
		}
		var field *fieldError
		if asFieldError(err, &field) {
			validation.FieldPath = field.path
			validation.cause = field.cause
		} else {
			validation.cause = errors.New(c.redactor.string(err.Error()))
		}
		return responseMeta{}, false, validation
	}
	return meta, false, nil
}

func (c *Client) connectionError(endpoint string, err error) *ConnectionError {
	timeout := isTimeout(err)
	connErr := &ConnectionError{
		Endpoint:  endpoint,
		Timeout:   timeout,
		sanitized: &sanitizedError{message: c.redactor.string(err.Error()), cause: err},
	}
	return connErr
}

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
	if c.logger == nil || !c.logger.Enabled(ctx, level) {
		return
	}
	c.logger.Log(ctx, level, message, args...)
}
