package jev

import (
	"errors"
	"fmt"
	"math"
	"math/rand"
	"net/http"
	"time"
)

// RetryPolicy controls how a failed attempt is retried. The zero value retries
// nothing; start from [DefaultRetry] and adjust.
type RetryPolicy struct {
	// MaxRetries is the number of retries after the initial attempt.
	MaxRetries int
	// InitialBackoff is the first delay, doubled each attempt up to MaxBackoff.
	// Zero disables backoff.
	InitialBackoff time.Duration
	// MaxBackoff caps every delay between attempts, including one a server asks
	// for through retry-after. Zero disables backoff.
	MaxBackoff time.Duration
	// Jitter is the fraction of each delay randomly subtracted, from 0 to 1. It
	// applies to a server-supplied wait as well, so callers that received the
	// same retry-after do not wake at the same instant.
	Jitter float64
	// Budget is the total wall-clock allowance for one call including delays.
	// Zero disables the limit. An attempt is not started when the delay before
	// it would exhaust the budget.
	Budget time.Duration
	// RetryStatus reports whether a status code should be retried. Nil retries
	// no status.
	RetryStatus func(status int) bool
	// RespectRetryAfter honors the retry-after and retry-after-ms headers,
	// clamped by MaxBackoff and jittered.
	RespectRetryAfter bool
	// RetryConnection retries a request that produced no HTTP response.
	RetryConnection bool
}

// DefaultRetry retries 408, 429 and 5xx twice, plus connection failures, with
// jittered exponential backoff from 500ms to 5s and a 30s total budget.
func DefaultRetry() RetryPolicy {
	return RetryPolicy{
		MaxRetries:        2,
		InitialBackoff:    500 * time.Millisecond,
		MaxBackoff:        5 * time.Second,
		Jitter:            0.25,
		Budget:            30 * time.Second,
		RetryStatus:       RetryableStatus,
		RespectRetryAfter: true,
		RetryConnection:   true,
	}
}

// NoRetry sends one attempt and returns its outcome.
func NoRetry() RetryPolicy { return RetryPolicy{} }

// RetryableStatus reports whether a status is worth retrying: 408, 429 and any
// 5xx, including the non-standard 529 the API uses for overload.
func RetryableStatus(status int) bool {
	switch {
	case status == http.StatusRequestTimeout, status == http.StatusTooManyRequests:
		return true
	case status >= http.StatusInternalServerError:
		return true
	}
	return false
}

func (p RetryPolicy) validate() error {
	if p.MaxRetries < 0 {
		return fmt.Errorf("%w: MaxRetries must not be negative", ErrInvalidConfig)
	}
	if p.InitialBackoff < 0 || p.MaxBackoff < 0 || p.Budget < 0 {
		return fmt.Errorf("%w: retry durations must not be negative", ErrInvalidConfig)
	}
	if p.Jitter < 0 || p.Jitter > 1 {
		return fmt.Errorf("%w: Jitter must be between 0 and 1", ErrInvalidConfig)
	}
	// Without this check the pair below retries as fast as the network allows.
	if p.InitialBackoff > 0 && p.MaxBackoff == 0 {
		return fmt.Errorf("%w: MaxBackoff must be set when InitialBackoff is", ErrInvalidConfig)
	}
	if p.MaxBackoff > 0 && p.InitialBackoff > p.MaxBackoff {
		return fmt.Errorf("%w: InitialBackoff must not exceed MaxBackoff", ErrInvalidConfig)
	}
	return nil
}

// backoff returns the delay before the retry that follows attempt, counted
// from zero.
func (p RetryPolicy) backoff(attempt int) time.Duration {
	if p.InitialBackoff <= 0 || p.MaxBackoff <= 0 {
		return 0
	}
	delay := float64(p.InitialBackoff) * math.Pow(2, float64(attempt))
	return p.jitter(time.Duration(math.Min(delay, float64(p.MaxBackoff))))
}

// jitter subtracts a random fraction of the delay, so a fleet that failed
// together does not retry together.
func (p RetryPolicy) jitter(delay time.Duration) time.Duration {
	if p.Jitter <= 0 || delay <= 0 {
		return delay
	}
	return time.Duration(float64(delay) * (1 - rand.Float64()*p.Jitter)) //nolint:gosec // Backoff jitter, not cryptography.
}

// delayFor returns the wait before the next attempt and whether that attempt
// fits in the remaining budget.
func (p RetryPolicy) delayFor(attempt int, err error, elapsed time.Duration) (time.Duration, bool) {
	delay := p.backoff(attempt)
	if p.RespectRetryAfter {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.RetryAfter > 0 {
			// A server-supplied wait is still bounded by MaxBackoff, so a
			// hostile or misconfigured upstream cannot pin the caller's
			// goroutine, and it is jittered so a fleet does not wake in lockstep.
			requested := apiErr.RetryAfter
			if p.MaxBackoff > 0 {
				requested = min(requested, p.MaxBackoff)
			}
			delay = p.jitter(requested)
		}
	}
	if p.Budget > 0 && elapsed+delay >= p.Budget {
		return 0, false
	}
	return delay, true
}
