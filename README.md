# taurus-jev-sdk-go

Go client for the [TypeSafe AI](https://typesafe.ai) System One API and its Jev model.

Jev answers typed questions about a piece of state and returns calibrated
probabilities. It generates no text, so every answer is a value your code can
branch on directly.

Unofficial client, maintained independently of TypeSafe AI. Zero dependencies
outside the standard library.

## Install

```
go get github.com/KKloudTarus/taurus-jev-sdk-go
```

Requires Go 1.22 or newer. The import path ends in `taurus-jev-sdk-go`; the
package identifier is `jev`.

```go
import jev "github.com/KKloudTarus/taurus-jev-sdk-go"
```

## Quickstart

Set `TYPESAFE_API_KEY`, then ask three questions in one request:

```go
client, err := jev.New()
if err != nil {
	return err
}

response, err := client.SystemOne(ctx,
	map[string]any{"subject": "Duplicate charge", "body": "I was charged twice."},
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
	return err
}

if probability, ok := response.NoulOf("billing"); ok && probability > 0.9 {
	routeToBilling(ticket)
}
if tone, ok := response.ChoiceOf("tone"); ok && tone.Confidence < 0.6 {
	sendToHumanReview(ticket)
}
```

## The three question types

| Type | Ask | Answer | Limit |
|---|---|---|---|
| `jev.Noul` | Is this statement true? | `Noul`, from 0 to 1 | yes/no |
| `jev.Choice` | Which label applies? | `Choice`, `Probabilities`, `Confidence` | 255 labels |
| `jev.Score` | Rate against ordered levels | `Score`, `Legend`, `Probabilities`, `Confidence` | 2 to 10 levels |

`Instructions` and every criterion accept a string, a map or a slice, so a
question can carry structure rather than a sentence.

Read an answer through `NoulOf`, `ChoiceOf` or `ScoreOf`. Each returns a second
result reporting whether the name was answered by that primitive.

## Configuration

`New` reads `TYPESAFE_API_KEY`, `TYPESAFE_BASE_URL` and
`TYPESAFE_DEFAULT_MODEL`. Options win over the environment.

```go
client, err := jev.New(
	jev.WithAPIKey(key),
	jev.WithModel("jev-latest"),
	jev.WithTimeout(2*time.Second),
	jev.WithRetry(jev.DefaultRetry()),
	jev.WithLogger(slog.Default()),
)
```

Per call, `UsingModel`, `UsingTimeout`, `UsingRetry`, `UsingHeader` and
`UsingExtraBody` override the client for that request only.

A `Client` is safe for concurrent use. Create one and share it so connections
are pooled.

## Errors

Classify with `errors.Is`, then read the details with `errors.As`:

```go
switch {
case errors.Is(err, jev.ErrRateLimit), errors.Is(err, jev.ErrOverloaded):
	degradeToRules()
case errors.Is(err, jev.ErrAuthentication):
	log.Fatal("check TYPESAFE_API_KEY")
case errors.Is(err, jev.ErrTimeout):
	giveUp()
default:
	var apiErr *jev.APIError
	if errors.As(err, &apiErr) {
		log.Printf("status %d request %s: %s", apiErr.Status, apiErr.RequestID, apiErr.Message)
	}
}
```

Sentinels: `ErrNoAPIKey`, `ErrInvalidConfig`, `ErrInvalidRequest`,
`ErrInvalidResponse`, `ErrConnection`, `ErrTimeout`, `ErrBadRequest`,
`ErrAuthentication`, `ErrPermissionDenied`, `ErrNotFound`,
`ErrUnprocessableEntity`, `ErrRateLimit`, `ErrOverloaded`, `ErrInternalServer`.

`APIError.RequestID` carries the `x-typesafe-request-id` header. Quote it in a
support report.

## Retries

`DefaultRetry` retries 408, 429 and 5xx twice, plus connection failures, with
jittered exponential backoff from 500ms to 5s under a 30s total budget. It
honors `retry-after` and `retry-after-ms`, so a server asking for a longer wait
than the budget allows ends the call instead of sleeping through it.

`jev.NoRetry()` sends one attempt. A cancelled context stops retrying
immediately.

## Credentials in logs and errors

The API key is masked in every error message and log record the client
produces, including a transport error that echoes the `Authorization` header.
URLs in error messages are stripped of userinfo, query and fragment.

`ConnectionError.Unwrap` returns the transport error unchanged so
`errors.Is(err, context.DeadlineExceeded)` keeps working. Print the
`ConnectionError`, not the unwrapped error.

## Forward compatibility

An answer type this version does not model does not fail the response. It
arrives with `Known() == false` and its bytes in `Answer.Raw`, so a primitive
added to the API later cannot break a service already in production.

For a response shape this version does not model, `SystemOneAs[T]` decodes the
body into your own type. `UsingExtraBody` adds top-level request fields.

## Development

```
go test ./...          # unit tests, no network
go test -race ./...    # same under the race detector
go vet ./...
gofmt -l .             # must print nothing
```

Every test runs against `httptest`, so the suite needs no API key and no
network.

## License

MIT. See [LICENSE](LICENSE).
