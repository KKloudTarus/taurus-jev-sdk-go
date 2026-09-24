package jev

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const testKey = "sk-live-abcdef123456"

// newTestClient starts a server and points a client at it, with the environment
// cleared so a real key on the machine cannot influence the test.
func newTestClient(t *testing.T, handler http.HandlerFunc, options ...Option) *Client {
	t.Helper()
	t.Setenv(APIKeyEnv, "")
	t.Setenv(BaseURLEnv, "")
	t.Setenv(ModelEnv, "")
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	defaults := []Option{WithAPIKey(testKey), WithBaseURL(server.URL), WithRetry(NoRetry())}
	client, err := New(append(defaults, options...)...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client
}

func decodeJSON(t *testing.T, payload []byte, out any) {
	t.Helper()
	if err := json.Unmarshal(payload, out); err != nil {
		t.Fatalf("decoding %s: %v", payload, err)
	}
}

func answerHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set(headerRequestID, "req_123")
	io.WriteString(w, answersPayload)
}

func TestNewAppliesEnvironmentThenOptions(t *testing.T) {
	t.Setenv(APIKeyEnv, "  env-key-12345  ")
	t.Setenv(BaseURLEnv, "https://gateway.internal/")
	t.Setenv(ModelEnv, "jev-pinned")

	client, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if client.apiKey != "env-key-12345" {
		t.Errorf("apiKey = %q; surrounding whitespace should be stripped", client.apiKey)
	}
	if client.baseURL != "https://gateway.internal" {
		t.Errorf("baseURL = %q; the trailing slash should be trimmed", client.baseURL)
	}
	if client.model != "jev-pinned" {
		t.Errorf("model = %q", client.model)
	}

	overridden, err := New(WithAPIKey("explicit-key-1"), WithModel("jev-latest"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if overridden.apiKey != "explicit-key-1" || overridden.model != "jev-latest" {
		t.Errorf("options did not win over the environment: %q, %q", overridden.apiKey, overridden.model)
	}
}

func TestNewRejectsUnusableConfig(t *testing.T) {
	t.Setenv(APIKeyEnv, "")
	cases := []struct {
		name    string
		options []Option
		want    error
	}{
		{"no key", nil, ErrNoAPIKey},
		{"key with a space", []Option{WithAPIKey("key with space")}, ErrInvalidConfig},
		{"key with a control character", []Option{WithAPIKey("key\x01value")}, ErrInvalidConfig},
		{"relative base URL", []Option{WithAPIKey("k123456789"), WithBaseURL("/v1")}, ErrInvalidConfig},
		{"empty model", []Option{WithAPIKey("k123456789"), WithModel("")}, ErrInvalidConfig},
		{"nil HTTP client", []Option{WithAPIKey("k123456789"), WithHTTPClient(nil)}, ErrInvalidConfig},
		{"bad retry", []Option{WithAPIKey("k123456789"), WithRetry(RetryPolicy{MaxRetries: -1})}, ErrInvalidConfig},
	}
	for _, testCase := range cases {
		_, err := New(testCase.options...)
		if !errors.Is(err, testCase.want) {
			t.Errorf("%s: err = %v, want %v", testCase.name, err, testCase.want)
		}
	}
}

func TestSystemOneSendsTheDocumentedRequest(t *testing.T) {
	var body map[string]any
	var request *http.Request
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		request = r
		payload, _ := io.ReadAll(r.Body)
		json.Unmarshal(payload, &body)
		answerHandler(w, r)
	})

	response, err := client.SystemOne(context.Background(),
		map[string]any{"message": "I was charged twice."},
		Questions{
			"billing": Noul{Instructions: "Is this about billing?"},
			"tone":    Choice{Criteria: map[string]any{"angry": nil, "calm": "polite"}},
			"urgency": Score{Criteria: []any{"Can wait", "Today"}},
		})
	if err != nil {
		t.Fatalf("SystemOne: %v", err)
	}

	if request.URL.Path != systemOnePath {
		t.Errorf("path = %q, want %q", request.URL.Path, systemOnePath)
	}
	if got := request.Header.Get(headerAuthorization); got != "Bearer "+testKey {
		t.Errorf("Authorization = %q", got)
	}
	if got := request.Header.Get(headerContentType); got != contentTypeJSON {
		t.Errorf("Content-Type = %q", got)
	}
	if got := request.Header.Get(headerAccept); got != contentTypeJSON {
		t.Errorf("Accept = %q", got)
	}
	if got := request.Header.Get(headerUserAgent); !strings.HasPrefix(got, sdkName+"/") {
		t.Errorf("User-Agent = %q", got)
	}
	if got := request.Header.Get(headerRuntime); !strings.HasPrefix(got, "go/") {
		t.Errorf("runtime header = %q", got)
	}
	if request.Header.Get(headerRetryCount) != "" {
		t.Error("the first attempt carried a retry count")
	}
	if body["model"] != DefaultModel {
		t.Errorf("model = %v, want %v", body["model"], DefaultModel)
	}
	if state, ok := body["state"].(map[string]any); !ok || state["message"] != "I was charged twice." {
		t.Errorf("state = %v", body["state"])
	}
	questions := body["questions"].(map[string]any)
	if len(questions) != 3 {
		t.Errorf("questions = %v", questions)
	}

	if response.RequestID != "req_123" {
		t.Errorf("RequestID = %q", response.RequestID)
	}
	if response.Model != "jev-latest" {
		t.Errorf("Model = %q", response.Model)
	}
	if probability, ok := response.NoulOf("billing"); !ok || probability != 0.98 {
		t.Errorf("NoulOf = %v, %v", probability, ok)
	}
}

func TestCallOptionsOverrideTheClient(t *testing.T) {
	var body map[string]any
	var header string
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		payload, _ := io.ReadAll(r.Body)
		json.Unmarshal(payload, &body)
		header = r.Header.Get("X-Tenant")
		answerHandler(w, r)
	}, WithHeader("X-Tenant", "client-level"))

	_, err := client.SystemOne(context.Background(), "x", Questions{"q": Noul{}},
		UsingModel("jev-pinned"),
		UsingHeader("X-Tenant", "call-level"),
		UsingExtraBody(map[string]any{"beta_flag": true}))
	if err != nil {
		t.Fatalf("SystemOne: %v", err)
	}
	if body["model"] != "jev-pinned" {
		t.Errorf("model = %v", body["model"])
	}
	if body["beta_flag"] != true {
		t.Errorf("extra body field missing: %v", body)
	}
	if header != "call-level" {
		t.Errorf("X-Tenant = %q, want the call-level value", header)
	}
}

func TestAuthenticationHeaderCannotBeOverridden(t *testing.T) {
	var auth string
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get(headerAuthorization)
		answerHandler(w, r)
	}, WithHeader(headerAuthorization, "Bearer attacker"))

	if _, err := client.SystemOne(context.Background(), "x", Questions{"q": Noul{}}); err != nil {
		t.Fatalf("SystemOne: %v", err)
	}
	if auth != "Bearer "+testKey {
		t.Errorf("Authorization = %q; a custom header overrode authentication", auth)
	}
}

func TestSystemOneAsDecodesACustomShape(t *testing.T) {
	client := newTestClient(t, answerHandler)

	type billingOnly struct {
		Model   string `json:"model"`
		Answers struct {
			Billing struct {
				Noul float64 `json:"noul"`
			} `json:"billing"`
		} `json:"answers"`
	}
	result, err := SystemOneAs[billingOnly](context.Background(), client, "x", Questions{"billing": Noul{}})
	if err != nil {
		t.Fatalf("SystemOneAs: %v", err)
	}
	if result.Model != "jev-latest" || result.Answers.Billing.Noul != 0.98 {
		t.Errorf("result = %+v", result)
	}
}

func TestInputIsValidatedBeforeAnyRequest(t *testing.T) {
	sent := false
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		sent = true
		answerHandler(w, r)
	})
	if _, err := client.SystemOne(context.Background(), "x", nil); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("empty questions: err = %v", err)
	}
	if _, err := client.SystemOne(context.Background(), nil, Questions{"q": Noul{}}); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("nil state: err = %v", err)
	}
	if sent {
		t.Error("an invalid request reached the server")
	}
}

func TestRetriesRateLimitAndHonorsRetryAfter(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		attempt := calls
		mu.Unlock()
		if attempt == 1 {
			w.Header().Set(headerRetryAfterMS, "10")
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"error": "slow down"}`)
			return
		}
		if got := r.Header.Get(headerRetryCount); got != "1" {
			t.Errorf("retry count = %q, want 1", got)
		}
		answerHandler(w, r)
	}, WithRetry(DefaultRetry()))

	started := time.Now()
	if _, err := client.SystemOne(context.Background(), "x", Questions{"q": Noul{}}); err != nil {
		t.Fatalf("SystemOne: %v", err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
	// The server asked for 10ms; the computed backoff would have been ~500ms.
	if elapsed := time.Since(started); elapsed > 300*time.Millisecond {
		t.Errorf("waited %v; retry-after-ms was ignored", elapsed)
	}
}

func TestNoRetryOnClientError(t *testing.T) {
	calls := 0
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnprocessableEntity)
		io.WriteString(w, `{"detail": [{"loc": ["body", "questions", "q", "criteria"], "msg": "Field required"}]}`)
	}, WithRetry(DefaultRetry()))

	_, err := client.SystemOne(context.Background(), "x", Questions{"q": Noul{}})
	if !errors.Is(err, ErrUnprocessableEntity) {
		t.Fatalf("err = %v, want ErrUnprocessableEntity", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d; a 422 must not be retried", calls)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want an *APIError", err)
	}
	if apiErr.Message != "questions.q.criteria: Field required" {
		t.Errorf("Message = %q", apiErr.Message)
	}
	if apiErr.Endpoint == "" || !strings.HasPrefix(apiErr.Endpoint, "POST ") {
		t.Errorf("Endpoint = %q", apiErr.Endpoint)
	}
}

func TestRetriesAreBounded(t *testing.T) {
	calls := 0
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusServiceUnavailable)
	}, WithRetry(RetryPolicy{MaxRetries: 2, RetryStatus: RetryableStatus}))

	if _, err := client.SystemOne(context.Background(), "x", Questions{"q": Noul{}}); !errors.Is(err, ErrInternalServer) {
		t.Fatalf("err = %v", err)
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3 (one attempt plus two retries)", calls)
	}
}

func TestRetryStopsAtTheBudget(t *testing.T) {
	calls := 0
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set(headerRetryAfter, "30")
		w.WriteHeader(http.StatusTooManyRequests)
	}, WithRetry(RetryPolicy{
		MaxRetries: 5, RetryStatus: RetryableStatus, RespectRetryAfter: true, Budget: time.Second,
	}))

	started := time.Now()
	if _, err := client.SystemOne(context.Background(), "x", Questions{"q": Noul{}}); !errors.Is(err, ErrRateLimit) {
		t.Fatalf("err = %v", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d; a 30s wait does not fit a 1s budget", calls)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Errorf("waited %v despite the budget", elapsed)
	}
}

func TestConnectionFailureIsTypedAndRetried(t *testing.T) {
	t.Setenv(APIKeyEnv, "")
	server := httptest.NewServer(http.HandlerFunc(answerHandler))
	address := server.URL
	server.Close() // Nothing is listening now.

	client, err := New(WithAPIKey(testKey), WithBaseURL(address),
		WithRetry(RetryPolicy{MaxRetries: 1, RetryConnection: true}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = client.SystemOne(context.Background(), "x", Questions{"q": Noul{}})
	if !errors.Is(err, ErrConnection) {
		t.Fatalf("err = %v, want ErrConnection", err)
	}
	var connErr *ConnectionError
	if !errors.As(err, &connErr) {
		t.Fatalf("err = %v, want a *ConnectionError", err)
	}
	if connErr.Endpoint == "" {
		t.Error("the endpoint is missing from the error")
	}
}

func TestPerCallTimeoutIsTyped(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	})
	_, err := client.SystemOne(context.Background(), "x", Questions{"q": Noul{}}, UsingTimeout(50*time.Millisecond))
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Error("the deadline cause is no longer reachable")
	}
}

func TestContextCancellationStopsRetries(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(headerRetryAfter, "60")
		w.WriteHeader(http.StatusTooManyRequests)
	}, WithRetry(RetryPolicy{MaxRetries: 5, RetryStatus: RetryableStatus}))

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := client.SystemOne(ctx, "x", Questions{"q": Noul{}}); err == nil {
		t.Fatal("expected an error")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Errorf("waited %v; the context was ignored", elapsed)
	}
}

func TestCredentialIsRedactedFromErrors(t *testing.T) {
	// Some gateways echo the request headers back in an error body.
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error": "rejected header Authorization: `+r.Header.Get(headerAuthorization)+`"}`)
	})
	_, err := client.SystemOne(context.Background(), "x", Questions{"q": Noul{}})
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), testKey) {
		t.Errorf("the API key leaked into the error: %v", err)
	}
	if !strings.Contains(err.Error(), "***") {
		t.Errorf("the credential was not masked: %v", err)
	}
}

func TestMalformedSuccessBody(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(headerRequestID, "req_bad")
		io.WriteString(w, `{"answers": "not an object"}`)
	})
	_, err := client.SystemOne(context.Background(), "x", Questions{"q": Noul{}})
	if !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("err = %v, want ErrInvalidResponse", err)
	}
	if !strings.Contains(err.Error(), "req_bad") {
		t.Errorf("the request ID is missing from %v", err)
	}
}

func TestModels(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != modelsPath {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Method != http.MethodGet {
			t.Errorf("method = %q", r.Method)
		}
		if r.Header.Get(headerContentType) != "" {
			t.Error("a GET request carried a Content-Type")
		}
		io.WriteString(w, `{"models": [{"name": "jev-latest", "description": "General-purpose.", "release_date": "2026-09-15"}]}`)
	})
	models, err := client.Models(context.Background())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(models) != 1 || models[0].Name != "jev-latest" || models[0].ReleaseDate != "2026-09-15" {
		t.Errorf("models = %+v", models)
	}
}

func TestClientIsSafeForConcurrentUse(t *testing.T) {
	client := newTestClient(t, answerHandler)
	var group sync.WaitGroup
	for i := 0; i < 16; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			if _, err := client.SystemOne(context.Background(), "x", Questions{"q": Noul{}}); err != nil {
				t.Errorf("SystemOne: %v", err)
			}
		}()
	}
	group.Wait()
}
