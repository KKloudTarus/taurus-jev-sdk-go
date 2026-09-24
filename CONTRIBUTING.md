# Contributing

Issues and pull requests are welcome.

## Running the checks

```
go test ./...            # unit tests, no API key and no network
go test -race ./...      # the same under the race detector
go vet ./...
gofmt -l .               # must print nothing
golangci-lint run ./...  # config in .golangci.yml
govulncheck ./...
```

Every test runs against `httptest` or a local listener, so the suite is
hermetic. CI runs the same checks on Go 1.22, 1.23 and the current release.

## What a change needs

Behavior changes ship with a test in the same commit. Line coverage is not the
bar: the retry loop is covered by mutation testing, because a suite that passes
against a broken client is not coverage. Reusing one `bytes.Reader` across
attempts, reporting a connection failure as non-retryable, dropping the
`MaxBackoff` clamp on `retry-after`, and returning a bare error after a deadline
each have a named test that fails when the behavior is removed.

A convenient way to check your own work is to break the thing you just fixed and
confirm a test goes red.

## The wire contract

The request and response shapes follow `https://api.typesafe.ai/openapi.json`.
When they disagree with this client, the spec wins. Do not add a client-side
limit the API does not have: an earlier release capped choice labels at 255
based on a blog post, which rejected legal requests and would have turned a
server-side change into an SDK upgrade.

Forward compatibility is deliberate. An answer type this version does not model
keeps its bytes in `Answer.Raw` rather than failing the response, and
`RawQuestion`, `UsingExtraBody` and `SystemOneAs[T]` exist so a new API field is
reachable without a release.

## Credentials

The API key must not reach any string this package produces. `redact.go` is the
single redaction point. A new error path or log site goes through it, and a new
field that can hold a response body is redacted like the others.

Never paste a real key, an `Authorization` header, or a raw response body into
an issue or a test fixture.

## Commits

Commit messages follow [Conventional Commits](https://www.conventionalcommits.org/en/v1.0.0/).

```
<type>[optional scope][!]: <description>

[optional body]

[optional footer(s)]
```

Types: `feat`, `fix`, `perf`, `refactor`, `test`, `docs`, `build`, `ci`,
`style`, `chore`, `revert`. The subject is 72 characters or fewer, imperative,
lowercase and without a trailing period. A breaking change takes `!` after the
type and a `BREAKING CHANGE:` footer. A change that fits two types is two
commits.

Each commit should build and pass its tests on its own.
