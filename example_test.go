package jev_test

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"os"
	"time"

	jev "github.com/KKloudTarus/taurus-jev-sdk-go"
)

// Route a support ticket on three questions asked in one request.
func Example() {
	client, err := jev.New()
	if err != nil {
		log.Fatal(err)
	}
	response, err := client.SystemOne(context.Background(),
		map[string]any{"subject": "Duplicate charge", "body": "I was charged twice. Please help."},
		jev.Questions{
			"billing": jev.Noul{Instructions: "Is this about billing?"},
			"tone": jev.Choice{
				Instructions: "What is the tone?",
				Criteria:     map[string]any{"angry": "upset or hostile", "calm": nil},
			},
			"urgency": jev.Score{
				Instructions: "How urgent is this?",
				Criteria:     []any{"Can wait", "This week", "Today"},
			},
		})
	if err != nil {
		log.Fatal(err)
	}

	if probability, ok := response.NoulOf("billing"); ok && probability > 0.9 {
		fmt.Println("route to billing")
	}
	if tone, ok := response.ChoiceOf("tone"); ok {
		fmt.Println("tone:", tone.Choice, "confidence:", tone.Confidence)
	}
	if urgency, ok := response.ScoreOf("urgency"); ok && urgency.Score > 1.5 {
		fmt.Println("escalate")
	}
}

// Confidence is the point of a System One model: a low-confidence answer is a
// signal to ask a person rather than to guess.
func ExampleSystemOneResponse_ChoiceOf() {
	var response *jev.SystemOneResponse // from client.SystemOne

	tone, ok := response.ChoiceOf("tone")
	switch {
	case !ok:
		fmt.Println("no answer for that question")
	case tone.Confidence < 0.6:
		fmt.Println("uncertain, send to a human reviewer")
	default:
		fmt.Println("acting on", tone.Choice)
	}
}

// Classify a failure before deciding whether to fall back or to fail the request.
func ExampleAPIError() {
	client, err := jev.New()
	if err != nil {
		log.Fatal(err)
	}
	_, err = client.SystemOne(context.Background(), "state", jev.Questions{"q": jev.Noul{}})

	switch {
	case err == nil:
	case errors.Is(err, jev.ErrRateLimit), errors.Is(err, jev.ErrOverloaded):
		// Retries are already exhausted here; shed load instead.
		fmt.Println("degrade to the rule-based path")
	case errors.Is(err, jev.ErrAuthentication):
		log.Fatal("check TYPESAFE_API_KEY")
	case errors.Is(err, jev.ErrTimeout):
		fmt.Println("give up on this ticket for now")
	default:
		var apiErr *jev.APIError
		if errors.As(err, &apiErr) {
			fmt.Printf("status %d, request %s: %s\n", apiErr.Status, apiErr.RequestID, apiErr.Message)
		}
	}
}

// Tune retries and logging for a request path with a tight latency budget.
func ExampleNew_options() {
	client, err := jev.New(
		jev.WithAPIKey(os.Getenv(jev.APIKeyEnv)),
		jev.WithModel("jev-latest"),
		jev.WithTimeout(2*time.Second),
		jev.WithRetry(jev.RetryPolicy{
			MaxRetries:        1,
			InitialBackoff:    100 * time.Millisecond,
			MaxBackoff:        time.Second,
			Jitter:            0.25,
			Budget:            3 * time.Second,
			RetryStatus:       jev.RetryableStatus,
			RespectRetryAfter: true,
			RetryConnection:   true,
		}),
		jev.WithLogger(slog.Default()),
	)
	if err != nil {
		log.Fatal(err)
	}
	_ = client
}

// Decode a response shape this version does not model, such as a field added to
// the API after this release.
func ExampleSystemOneAs() {
	client, err := jev.New()
	if err != nil {
		log.Fatal(err)
	}

	type response struct {
		Model   string `json:"model"`
		Answers struct {
			Spam struct {
				Noul float64 `json:"noul"`
			} `json:"spam"`
		} `json:"answers"`
	}
	result, err := jev.SystemOneAs[response](context.Background(), client,
		"Buy cheap watches now", jev.Questions{"spam": jev.Noul{Instructions: "Is this spam?"}})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(result.Answers.Spam.Noul > 0.9)
}
