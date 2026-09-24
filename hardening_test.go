package jev

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- credential containment -------------------------------------------------

func TestClientNeverPrintsItsKey(t *testing.T) {
	client := newTestClient(t, answerHandler)
	renders := []string{
		fmt.Sprintf("%v", client),
		fmt.Sprintf("%+v", client),
		fmt.Sprintf("%#v", client),
		fmt.Sprintf("%v", *client),
		fmt.Sprintf("%+v", *client),
		client.String(),
	}
	for _, render := range renders {
		if strings.Contains(render, testKey) {
			t.Errorf("the API key leaked through a format verb: %s", render)
		}
	}

	// The same must hold for log/slog, whose TextHandler renders a struct with %+v.
	var buffer bytes.Buffer
	slog.New(slog.NewTextHandler(&buffer, nil)).Info("configured", "client", client)
	slog.New(slog.NewJSONHandler(&buffer, nil)).Info("configured", "client", client)
	if strings.Contains(buffer.String(), testKey) {
		t.Errorf("the API key leaked into a log record: %s", buffer.String())
	}
	if !strings.Contains(buffer.String(), "***") {
		t.Errorf("the key was not masked: %s", buffer.String())
	}
}

func TestErrorBodyIsRedactedAndBounded(t *testing.T) {
	padding := strings.Repeat("x", 9000)
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(w, `{"error":"quota exhausted","echo":"%s","pad":"%s"}`,
			r.Header.Get(headerAuthorization), padding)
	})
	_, err := client.SystemOne(context.Background(), "x", Questions{"q": Noul{}})
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(string(apiErr.Body), testKey) {
		t.Errorf("the key survived in APIError.Body: %s", apiErr.Body)
	}
	// The message is extracted from the whole body, so a large trace field does
	// not bury the one line an operator needs.
	if apiErr.Message != "quota exhausted" {
		t.Errorf("Message = %q", apiErr.Message)
	}
	if len(apiErr.Body) > maxErrorBody {
		t.Errorf("Body is %d bytes, over the %d cap", len(apiErr.Body), maxErrorBody)
	}
	// The body must be a copy: a reslice would pin the whole response buffer.
	if cap(apiErr.Body) > maxErrorBody {
		t.Errorf("Body retains a %d byte array behind an %d byte slice", cap(apiErr.Body), len(apiErr.Body))
	}
}

func TestAuthorizationIsNotResentOnRedirect(t *testing.T) {
	var secondCalls int
	second := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		secondCalls++
		t.Errorf("the redirect target was reached with Authorization=%q", r.Header.Get(headerAuthorization))
	}))
	defer second.Close()

	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, second.URL+systemOnePath, http.StatusTemporaryRedirect)
	})
	_, err := client.SystemOne(context.Background(), "x", Questions{"q": Noul{}})
	if err == nil {
		t.Fatal("the redirect was followed and reported as success")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusTemporaryRedirect {
		t.Errorf("err = %v, want a 307 APIError", err)
	}
	if secondCalls != 0 {
		t.Errorf("the redirect target was called %d times", secondCalls)
	}
}

func TestBaseURLMustNotWeakenTransport(t *testing.T) {
	t.Setenv(APIKeyEnv, "")
	cases := []string{
		"http://api.typesafe.ai",              // cleartext carries the bearer token
		"https://user:secret@api.typesafe.ai", // credentials in the URL
		"https://api.typesafe.ai?trace=1",     // a query relocates the concatenated path
		"https://api.typesafe.ai#frag",        // so does a fragment
	}
	for _, baseURL := range cases {
		_, err := New(WithAPIKey(testKey), WithBaseURL(baseURL))
		if !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("%s: err = %v, want ErrInvalidConfig", baseURL, err)
		}
	}
	// http stays available for a loopback host, which is how tests and local
	// gateways run.
	if _, err := New(WithAPIKey(testKey), WithBaseURL("http://127.0.0.1:8080")); err != nil {
		t.Errorf("loopback http rejected: %v", err)
	}
}

// --- timeouts and deadlines -------------------------------------------------

func TestTimeoutComposesWithASuppliedClient(t *testing.T) {
	t.Setenv(APIKeyEnv, "")
	marker := &http.Transport{MaxIdleConnsPerHost: 512}
	supplied := &http.Client{Transport: marker}

	for _, order := range [][]Option{
		{WithHTTPClient(supplied), WithTimeout(2 * time.Second)},
		{WithTimeout(2 * time.Second), WithHTTPClient(supplied)},
	} {
		client, err := New(append([]Option{WithAPIKey(testKey)}, order...)...)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if client.httpClient != supplied {
			t.Error("the supplied client was replaced")
		}
		if client.httpClient.Transport != marker {
			t.Error("the supplied transport was discarded")
		}
		if client.timeout == nil || *client.timeout != 2*time.Second {
			t.Errorf("timeout = %v, want 2s", client.timeout)
		}
	}
}

func TestNonPositiveTimeoutsAreRejected(t *testing.T) {
	t.Setenv(APIKeyEnv, "")
	// net/http arms no deadline for a non-positive timeout, so accepting one
	// would silently remove every deadline the caller believes it set.
	for _, timeout := range []time.Duration{0, -time.Second} {
		if _, err := New(WithAPIKey(testKey), WithTimeout(timeout)); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("WithTimeout(%v): err = %v, want ErrInvalidConfig", timeout, err)
		}
	}
	client := newTestClient(t, answerHandler)
	for _, timeout := range []time.Duration{0, -time.Second} {
		_, err := client.SystemOne(context.Background(), "x", Questions{"q": Noul{}}, UsingTimeout(timeout))
		if !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("UsingTimeout(%v): err = %v, want ErrInvalidRequest", timeout, err)
		}
	}
}

func TestDefaultTimeoutIsApplied(t *testing.T) {
	client := newTestClient(t, answerHandler)
	if client.timeout == nil || *client.timeout != DefaultTimeout {
		t.Errorf("timeout = %v, want the %v default", client.timeout, DefaultTimeout)
	}
}

// A deadline that expires between attempts must be reported as a timeout, not
// as the last server error. This kills the mutant that returned a bare error.
func TestDeadlineDuringRetryReportsATimeout(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		w.Header().Set(headerRetryAfter, "5")
		w.WriteHeader(http.StatusTooManyRequests)
	}, WithRetry(RetryPolicy{
		MaxRetries: 5, RetryStatus: RetryableStatus,
		InitialBackoff: 500 * time.Millisecond, MaxBackoff: 2 * time.Second,
		RespectRetryAfter: true,
	}))

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := client.SystemOne(ctx, "x", Questions{"q": Noul{}})

	if !errors.Is(err, ErrTimeout) {
		t.Errorf("err = %v, want ErrTimeout", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want the deadline cause to be reachable", err)
	}
	// The failure in flight when the deadline passed stays available.
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusTooManyRequests {
		t.Errorf("the last failure is not reachable through %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Errorf("waited %v; the deadline did not cut the retry sleep", elapsed)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Errorf("calls = %d; the loop kept going past the deadline", calls)
	}
}

func TestCancellationIsNotReportedAsATimeout(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}, WithRetry(DefaultRetry()))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := client.SystemOne(ctx, "x", Questions{"q": Noul{}})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if errors.Is(err, ErrTimeout) {
		t.Errorf("err = %v; a cancellation is not a timeout", err)
	}
}

// --- retry loop -------------------------------------------------------------

// Every attempt must carry the same non-empty body. This kills the mutant that
// reused one bytes.Reader across attempts, which sent an empty body on retries.
func TestEveryAttemptSendsTheSameBody(t *testing.T) {
	var mu sync.Mutex
	var bodies [][]byte
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		payload, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, payload)
		count := len(bodies)
		mu.Unlock()
		if count < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		answerHandler(w, r)
	}, WithRetry(RetryPolicy{
		MaxRetries: 3, RetryStatus: RetryableStatus,
		InitialBackoff: time.Millisecond, MaxBackoff: 2 * time.Millisecond,
	}))

	if _, err := client.SystemOne(context.Background(), "state text", Questions{"q": Noul{Instructions: "?"}}); err != nil {
		t.Fatalf("SystemOne: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 3 {
		t.Fatalf("attempts = %d, want 3", len(bodies))
	}
	for i, body := range bodies {
		if len(body) == 0 {
			t.Errorf("attempt %d sent an empty body", i)
		}
		if !bytes.Equal(body, bodies[0]) {
			t.Errorf("attempt %d body differs from the first:\n%s\n%s", i, bodies[0], body)
		}
	}
}

// A connection failure must actually be retried. This kills the mutant that
// reported connection errors as non-retryable.
func TestConnectionFailuresAreRetriedTheConfiguredNumberOfTimes(t *testing.T) {
	t.Setenv(APIKeyEnv, "")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = listener.Close() }()

	var mu sync.Mutex
	connections := 0
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			connections++
			mu.Unlock()
			_ = conn.Close() // Accept, then reset, so every attempt is a transport failure.
		}
	}()

	client, err := New(WithAPIKey(testKey), WithBaseURL("http://"+listener.Addr().String()),
		WithRetry(RetryPolicy{MaxRetries: 2, RetryConnection: true,
			InitialBackoff: time.Millisecond, MaxBackoff: 2 * time.Millisecond}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := client.SystemOne(context.Background(), "x", Questions{"q": Noul{}}); !errors.Is(err, ErrConnection) {
		t.Fatalf("err = %v, want ErrConnection", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if connections != 3 {
		t.Errorf("connections = %d, want 3 (one attempt plus two retries)", connections)
	}
}

func TestConnectionFailuresAreNotRetriedWhenDisabled(t *testing.T) {
	t.Setenv(APIKeyEnv, "")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	address := listener.Addr().String()
	_ = listener.Close() // Nothing is listening.

	client, err := New(WithAPIKey(testKey), WithBaseURL("http://"+address),
		WithRetry(RetryPolicy{MaxRetries: 3, RetryConnection: false}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	started := time.Now()
	if _, err := client.SystemOne(context.Background(), "x", Questions{"q": Noul{}}); !errors.Is(err, ErrConnection) {
		t.Fatalf("err = %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Errorf("took %v; connection retries were not disabled", elapsed)
	}
}

func TestCallerCannotSetTheRetryCountHeader(t *testing.T) {
	var seen []string
	var mu sync.Mutex
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get(headerRetryCount))
		count := len(seen)
		mu.Unlock()
		if count == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		answerHandler(w, r)
	}, WithHeader(headerRetryCount, "99"), WithRetry(RetryPolicy{
		MaxRetries: 2, RetryStatus: RetryableStatus,
		InitialBackoff: time.Millisecond, MaxBackoff: 2 * time.Millisecond,
	}))

	if _, err := client.SystemOne(context.Background(), "x", Questions{"q": Noul{}},
		UsingHeader(headerRetryCount, "77")); err != nil {
		t.Fatalf("SystemOne: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 || seen[0] != "" || seen[1] != "1" {
		t.Errorf("retry counts = %q, want [\"\", \"1\"]", seen)
	}
}

// --- response validation ----------------------------------------------------

func TestDegradedResponsesAreRejected(t *testing.T) {
	usage := `"usage":{"input_tokens":1,"output_tokens":1}`
	cases := []struct {
		name string
		body string
		path string
	}{
		{"null body", `null`, ""},
		{"empty body", ``, ""},
		{"empty object", `{}`, "model"},
		{"null answers", `{"model":"m",` + usage + `,"answers":null}`, "answers"},
		{"no answers", `{"model":"m",` + usage + `,"answers":{}}`, "answers"},
		{"missing noul", `{"model":"m",` + usage + `,"answers":{"spam":{"type":"noul"}}}`, "answers.spam.noul"},
	}
	for _, testCase := range cases {
		client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set(headerRequestID, "req_bad")
			_, _ = io.WriteString(w, testCase.body)
		})
		_, err := client.SystemOne(context.Background(), "x", Questions{"spam": Noul{}})
		if !errors.Is(err, ErrInvalidResponse) {
			t.Errorf("%s: err = %v, want ErrInvalidResponse", testCase.name, err)
			continue
		}
		var validation *ResponseValidationError
		if !errors.As(err, &validation) {
			t.Errorf("%s: err = %v, want a *ResponseValidationError", testCase.name, err)
			continue
		}
		if validation.FieldPath != testCase.path {
			t.Errorf("%s: FieldPath = %q, want %q", testCase.name, validation.FieldPath, testCase.path)
		}
		if validation.RequestID != "req_bad" {
			t.Errorf("%s: RequestID = %q", testCase.name, validation.RequestID)
		}
	}
}

func TestModelsRejectsADegradedBody(t *testing.T) {
	for _, body := range []string{`null`, `{}`, `{"models":null}`} {
		client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, body)
		})
		models, err := client.Models(context.Background())
		if !errors.Is(err, ErrInvalidResponse) {
			t.Errorf("%s: models = %v, err = %v; want ErrInvalidResponse", body, models, err)
		}
	}
	// An account with no models is a legitimate empty list, not an error.
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"models":[]}`)
	})
	models, err := client.Models(context.Background())
	if err != nil || len(models) != 0 {
		t.Errorf("models = %v, err = %v; want an empty list", models, err)
	}
}

func TestModelsRejectsOptionsItCannotHonor(t *testing.T) {
	client := newTestClient(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("a request was sent despite an unusable option")
	})
	for _, option := range []CallOption{UsingExtraBody(map[string]any{"x": 1}), UsingModel("nope")} {
		if _, err := client.Models(context.Background(), option); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("err = %v, want ErrInvalidRequest", err)
		}
	}
}

func TestResponseCarriesTransportMetadata(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(headerRequestID, "req_123")
		w.Header().Set("X-Ratelimit-Remaining", "42")
		answerHandler(w, r)
	})
	response, err := client.SystemOne(context.Background(), "x", Questions{"q": Noul{}})
	if err != nil {
		t.Fatalf("SystemOne: %v", err)
	}
	if response.Status != http.StatusOK {
		t.Errorf("Status = %d", response.Status)
	}
	if got := response.Header.Get("X-Ratelimit-Remaining"); got != "42" {
		t.Errorf("Header lookup = %q", got)
	}
	if len(response.RawBody) == 0 {
		t.Error("RawBody is empty")
	}
	if response.RequestID != "req_123" {
		t.Errorf("RequestID = %q", response.RequestID)
	}
}

// --- input validation -------------------------------------------------------

func TestTypedNilStateIsRejected(t *testing.T) {
	client := newTestClient(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("a nil state reached the server")
	})
	var nilMap map[string]any
	var nilSlice []any
	for _, state := range []any{nil, nilMap, nilSlice} {
		if _, err := client.SystemOne(context.Background(), state, Questions{"q": Noul{}}); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("state %#v: err = %v, want ErrInvalidRequest", state, err)
		}
	}
}

func TestRawQuestionReachesTheWire(t *testing.T) {
	var body map[string]any
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		payload, _ := io.ReadAll(r.Body)
		decodeJSON(t, payload, &body)
		w.Header().Set(headerRequestID, "req_1")
		_, _ = io.WriteString(w, `{"model":"m","usage":{"input_tokens":1,"output_tokens":1},
			"answers":{"q":{"type":"rank","rank":3}}}`)
	})
	response, err := client.SystemOne(context.Background(), "x", Questions{
		"q": RawQuestion{Type: "rank", Fields: map[string]any{"instructions": "?"}},
	})
	if err != nil {
		t.Fatalf("SystemOne: %v", err)
	}
	question := body["questions"].(map[string]any)["q"].(map[string]any)
	if question["type"] != "rank" || question["instructions"] != "?" {
		t.Errorf("question = %v", question)
	}
	answer := response.Answers["q"]
	if answer.Type != "rank" || answer.Known() {
		t.Errorf("answer = %+v", answer)
	}
}

// --- connection pool --------------------------------------------------------

func TestTheSDKOwnsItsConnectionPool(t *testing.T) {
	client := newTestClient(t, answerHandler)
	transport, ok := client.httpClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport = %T, want a dedicated *http.Transport", client.httpClient.Transport)
	}
	if transport == http.DefaultTransport {
		t.Error("the client shares http.DefaultTransport with the rest of the process")
	}
	// The stdlib default of two idle connections per host forces a fresh TLS
	// handshake on most requests once more than two calls are in flight.
	if transport.MaxIdleConnsPerHost != defaultMaxIdleConnsPerHost {
		t.Errorf("MaxIdleConnsPerHost = %d, want %d", transport.MaxIdleConnsPerHost, defaultMaxIdleConnsPerHost)
	}
	if transport.MaxIdleConns != defaultMaxIdleConns {
		t.Errorf("MaxIdleConns = %d, want %d", transport.MaxIdleConns, defaultMaxIdleConns)
	}
}
