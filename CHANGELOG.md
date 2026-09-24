# Changelog

Notable changes to this module. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html). While the major
version is zero, a minor bump may carry a breaking change.

## v0.1.0 (2026-09-24)

First release. A dependency-free client for the TypeSafe AI System One API and
its Jev model.

### Added

- `Client.SystemOne` and `Client.Models`, with `SystemOneAs[T]` for a response
  shape this version does not model.
- The three question primitives, `Noul`, `Choice` and `Score`, plus
  `RawQuestion` for a primitive the API adds after this release.
- Answer accessors: `NoulOf`, `ChoiceOf` and `ScoreOf` for one question,
  `Nouls`, `Choices`, `Scores` and `Unknown` in bulk.
- Client options `WithAPIKey`, `WithBaseURL`, `WithModel`, `WithHTTPClient`,
  `WithTimeout`, `WithRetry` and `WithLogger`, each overridable per call with
  `UsingModel`, `UsingTimeout`, `UsingRetry`, `UsingHeader` and
  `UsingExtraBody`.
- Error classification through `errors.Is` against fourteen sentinels, with
  detail on `*APIError`, `*ConnectionError` and `*ResponseValidationError`.
- Transport metadata on every response: `RequestID`, `Status`, `Header` and
  `RawBody`.

### Guarantees this release makes

- **Required fields are enforced.** A response omitting `noul` would decode to
  `0.0` in a plain float, which reads as a maximally confident no. It returns a
  `*ResponseValidationError` naming the field instead. The same holds for a
  body that is `null`, empty, or missing `model`, `answers` or `usage`.
- **Credentials stay out of strings.** The API key is masked in error messages,
  error bodies, log records, and the rendering of the `Client` under `%v`,
  `%+v`, `%#v` and `log/slog`. Masking covers the raw, `Bearer`, Go-quoted,
  JSON-escaped and percent-encoded spellings. `ConnectionError.Unwrap` ends at a
  sanitized node, so an error reporter that walks the chain cannot reach the raw
  transport error.
- **The Authorization header does not follow redirects.** Go's default policy
  compares hostnames only and would re-send the bearer token over cleartext or
  to a co-hosted port. `WithBaseURL` requires https outside a loopback host.
- **A server cannot pin your goroutine.** A `retry-after` wait is clamped by
  `MaxBackoff`, jittered so a fleet does not retry in lockstep, and capped at 24
  hours before either applies.
- **Timeouts compose.** `WithTimeout` is applied through the request context, so
  a client supplied through `WithHTTPClient` keeps its transport. A non-positive
  timeout is rejected rather than silently removing every deadline.
- **A deadline is reported as one.** An expiry during a retry sleep returns
  `ErrTimeout` with the context cause and the last failure both reachable.
- **The connection pool is sized for a service.** The client owns an
  `http.Transport` with 128 idle connections per host, against the stdlib
  default of two.
