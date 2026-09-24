// Package jev is a dependency-free client for the TypeSafe AI System One API
// and its Jev model.
//
// Jev answers typed questions about a piece of state and returns calibrated
// probabilities. It generates no text, so every answer is a value your code can
// branch on directly.
//
// Send state and named questions:
//
//	client, err := jev.New(jev.WithAPIKey(os.Getenv(jev.APIKeyEnv)))
//	if err != nil {
//		return err
//	}
//	response, err := client.SystemOne(ctx, "I was charged twice.", jev.Questions{
//		"billing": jev.Noul{Instructions: "Is this about billing?"},
//	})
//	if err != nil {
//		return err
//	}
//	probability, _ := response.NoulOf("billing")
//
// The three question types are [Noul] (yes/no), [Choice] (one label out of
// many) and [Score] (a rating against ordered levels). Each has a matching
// answer type reached through [SystemOneResponse.NoulOf],
// [SystemOneResponse.ChoiceOf] and [SystemOneResponse.ScoreOf].
//
// This is an unofficial client, maintained independently of TypeSafe AI.
// The wire contract follows https://docs.typesafe.ai/api.
package jev

// Wire constants and environment variables.
const (
	// DefaultBaseURL is the API root used when none is configured.
	DefaultBaseURL = "https://api.typesafe.ai"
	// DefaultModel is the model alias used when none is configured.
	DefaultModel = "jev-latest"

	// APIKeyEnv holds the API key.
	APIKeyEnv = "TYPESAFE_API_KEY"
	// BaseURLEnv overrides the API root.
	BaseURLEnv = "TYPESAFE_BASE_URL"
	// ModelEnv overrides the default model.
	ModelEnv = "TYPESAFE_DEFAULT_MODEL"
)

const (
	systemOnePath = "/v1/systemone"
	modelsPath    = "/v1/models"

	headerAuthorization = "Authorization"
	headerAccept        = "Accept"
	headerContentType   = "Content-Type"
	headerUserAgent     = "User-Agent"
	headerSDK           = "X-TypeSafe-SDK"
	headerRuntime       = "X-TypeSafe-Runtime"
	headerRetryCount    = "X-TypeSafe-Retry-Count"
	headerRequestID     = "x-typesafe-request-id"
	headerRetryAfter    = "retry-after"
	headerRetryAfterMS  = "retry-after-ms"

	contentTypeJSON = "application/json"
	sdkName         = "taurus-jev-sdk-go"
)
