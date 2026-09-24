# taurus-jev-sdk-go

[![Go Reference](https://pkg.go.dev/badge/github.com/KKloudTarus/taurus-jev-sdk-go.svg)](https://pkg.go.dev/github.com/KKloudTarus/taurus-jev-sdk-go)
[![CI](https://github.com/KKloudTarus/taurus-jev-sdk-go/actions/workflows/ci.yml/badge.svg)](https://github.com/KKloudTarus/taurus-jev-sdk-go/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/KKloudTarus/taurus-jev-sdk-go)](https://goreportcard.com/report/github.com/KKloudTarus/taurus-jev-sdk-go)
[![Go 1.22+](https://img.shields.io/badge/go-1.22%2B-00ADD8)](https://go.dev/dl/)
[![License MIT](https://img.shields.io/badge/license-MIT-blue)](LICENSE)

Go client for the [TypeSafe AI](https://typesafe.ai) System One API and its Jev
model.

Jev answers typed questions about a piece of state and returns calibrated
probabilities. It generates no text, so every answer is a value your code can
branch on directly.

Unofficial client, maintained independently of TypeSafe AI. Zero dependencies
outside the standard library.

```go
response, err := client.SystemOne(ctx, ticket, jev.Questions{
    "billing": jev.Noul{Instructions: "Is this about billing?"},
})
probability, _ := response.NoulOf("billing")
```

## Why this client

- **Degraded responses are rejected, not coerced.** A body missing a required
  field returns an error naming it, so a truncated response never reaches your
  branching logic as a confident zero.
- **The API key stays out of every string.** Errors, error bodies, log records
  and the client's own rendering are all masked, in six spellings.
- **The bearer token does not follow redirects.** Go's default policy would
  re-send it over cleartext to any subdomain of the same host.
- **A server cannot pin your goroutine.** A `retry-after` wait is clamped,
  jittered and capped.
- **The pool is sized for a service.** 128 idle connections per host against the
  stdlib default of two, worth 2.2x throughput at 200 concurrent calls.
- **Forward compatible.** A primitive the API adds later is reachable through
  `RawQuestion` and `Answer.Raw` without waiting for a release.

## Install

```
go get github.com/KKloudTarus/taurus-jev-sdk-go
```

Requires Go 1.22 or newer. Zero dependencies outside the standard library. The import path ends in `taurus-jev-sdk-go`; the
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

| Type | Ask | Answer |
|---|---|---|
| `jev.Noul` | Is this statement true? | `Noul`, from 0 to 1 |
| `jev.Choice` | Which label applies? | `Choice`, `Probabilities`, `Confidence` |
| `jev.Score` | Rate against ordered levels | `Score`, `Legend`, `Probabilities`, `Confidence` |

`Instructions` and every criterion accept a string, a map or a slice, so a
question can carry structure rather than a sentence.

Cardinality limits are the API's, not this client's, so a limit raised server
side needs no SDK upgrade.

Read one answer through `NoulOf`, `ChoiceOf` or `ScoreOf`, each returning a
second result that reports whether the name was answered by that primitive. Read
them in bulk through `Nouls()`, `Choices()`, `Scores()` and `Unknown()`.

`jev.RawQuestion` sends a question shape this version does not model, for a
primitive the API adds after this release.

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

Detail types: `*APIError` for an unsuccessful status, `*ConnectionError` for a
request that never got a response, and `*ResponseValidationError` for a success
whose body did not match the schema.

`RequestID` carries the `x-typesafe-request-id` header on all three. Quote it in
a support report. `SystemOneResponse` also carries `Status`, `Header` and
`RawBody`.

## Responses are validated, not coerced

A field the API documents as required is enforced. A response that omits `noul`
would decode to `0.0` in a plain float, which reads as a maximally confident no,
so the client returns a `*ResponseValidationError` naming the field instead:

```
jev: POST https://api.typesafe.ai/v1/systemone: invalid response data at "answers.spam.noul"
```

A body that is `null`, empty, or missing `model`, `answers` or `usage` is
rejected the same way. A truncated or degraded response never reaches your
branching logic as a zero value.

## Retries

`DefaultRetry` retries 408, 429 and 5xx twice, plus connection failures, with
jittered exponential backoff from 500ms to 5s under a 30s total budget.

It honors `retry-after` and `retry-after-ms`, with two bounds. The requested wait
is clamped by `MaxBackoff`, so a hostile or misconfigured upstream cannot pin
your goroutine for a day, and it is jittered, so a fleet handed the same
`retry-after` does not retry in lockstep. A wait that would exhaust `Budget` ends
the call instead of sleeping through it.

`jev.NoRetry()` sends one attempt. A cancelled or expired context stops retrying
immediately and reports `ErrTimeout` alongside `context.DeadlineExceeded`, with
the failure that was in flight still reachable through `errors.As`.

## Credentials

The API key is masked in every string this package produces: error messages,
error bodies, log records, and the rendering of the `Client` itself under `%v`,
`%+v`, `%#v` and `log/slog`. Masking covers the raw key, the `Bearer` form, and
its Go-quoted, JSON-escaped and percent-encoded spellings. URLs in errors are
stripped of userinfo, query and fragment.

`ConnectionError.Unwrap` returns a sanitized node: its message is redacted and
it has no `Unwrap` of its own, so an error reporter that walks the chain cannot
reach the raw transport error, while `errors.Is` and `errors.As` still see
through to it.

The SDK's own HTTP client does not follow redirects. Go's default policy
re-sends `Authorization` to any subdomain of the same host and ignores scheme
and port, which would leak the bearer token over cleartext or to a co-hosted
service. A client supplied through `WithHTTPClient` keeps its own policy and is
responsible for this itself.

`WithBaseURL` requires `https`, or `http` on a loopback host, and rejects a URL
carrying credentials, a query or a fragment.

## Connection pool

The SDK builds its own `http.Transport` with 128 idle connections per host. The
stdlib default is two, which forces a fresh TCP and TLS handshake on most
requests once more than two calls are in flight. Measured against an upstream
with a 20ms RTT at 200 concurrent calls, the default gave 1593 rps and a p50 of
106ms; this pool gave 3509 rps and a p50 of 36.6ms.

A client supplied through `WithHTTPClient` is used as given, transport included.

## Forward compatibility

An answer type this version does not model does not fail the response. It
arrives with `Known() == false` and its bytes in `Answer.Raw`, so a primitive
added to the API later cannot break a service already in production. Reach those
answers through `Unknown()`.

A malformed answer is rejected rather than passed through, so a broken payload
is never mistaken for a future primitive.

To send a question shape this version does not model, use `jev.RawQuestion`. For
a response shape it does not model, `SystemOneAs[T]` decodes the body into your
own type. `UsingExtraBody` adds top-level request fields.

A decoded response round-trips through `encoding/json`, so it can be cached or
forwarded and decoded again.

## Development

```
go test ./...          # unit tests, no network
go test -race ./...    # same under the race detector
go vet ./...
gofmt -l .             # must print nothing
```

Every test runs against `httptest` or a local listener, so the suite needs no
API key and no network.

The retry loop is covered by mutation testing rather than by line coverage
alone. Reusing one `bytes.Reader` across attempts, reporting a connection
failure as non-retryable, dropping the `MaxBackoff` clamp on `retry-after`, and
returning a bare error after a deadline are each caught by a named test.

## Project

- [CHANGELOG.md](CHANGELOG.md) for what each release contains
- [CONTRIBUTING.md](CONTRIBUTING.md) for the checks a change has to pass
- [SECURITY.md](SECURITY.md) for reporting a vulnerability and for the
  properties this client guarantees

## License

MIT. See [LICENSE](LICENSE).
