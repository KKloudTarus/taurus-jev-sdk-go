# Security policy

## Reporting a vulnerability

Report privately through
[GitHub Security Advisories](https://github.com/KKloudTarus/taurus-jev-sdk-go/security/advisories/new).
Please do not open a public issue for a vulnerability.

Include the affected version, what an attacker gains, and the smallest program
that shows it. Never include a real API key, an `Authorization` header, or a
response body that may carry one.

Expect an acknowledgement within a week.

This repository covers the Go client only. A vulnerability in the TypeSafe AI
API itself belongs with the vendor at https://typesafe.ai.

## Supported versions

While the major version is zero, only the latest minor release receives fixes.

## What this client guarantees

These are the properties a report can hold the client to. Each has a named test.

- The API key does not appear in any string the package produces: error
  messages, error bodies, log records, or the rendering of the `Client` under
  `%v`, `%+v`, `%#v` and `log/slog`. Masking covers the raw, `Bearer`,
  Go-quoted, JSON-escaped and percent-encoded spellings.
- `ConnectionError.Unwrap` ends at a sanitized node, so an error reporter that
  walks the chain cannot reach the raw transport error. `errors.Is` and
  `errors.As` still reach the cause.
- The SDK's own HTTP client does not follow redirects, so the `Authorization`
  header is never re-sent to another scheme, port or subdomain.
- `WithBaseURL` requires https outside a loopback host, and rejects a URL
  carrying credentials, a query or a fragment.
- A caller cannot override the `Authorization`, `Accept`, `User-Agent` or SDK
  identification headers, nor the SDK's retry-count header.
- A server-supplied `retry-after` is clamped by `MaxBackoff` and capped at 24
  hours, so a hostile upstream cannot pin a goroutine.
- A response body is read under a 32 MiB limit, and a truncated read is reported
  rather than parsed.

## What it does not cover

A client supplied through `WithHTTPClient` is used as given. Its transport,
timeout and redirect policy are yours, including the redirect behavior described
above.
