package jev

import (
	"errors"
	"testing"
	"time"
)

func TestRetryableStatus(t *testing.T) {
	retried := []int{408, 429, 500, 502, 503, 504, 529}
	for _, status := range retried {
		if !RetryableStatus(status) {
			t.Errorf("status %d should be retried", status)
		}
	}
	for _, status := range []int{200, 400, 401, 403, 404, 422} {
		if RetryableStatus(status) {
			t.Errorf("status %d should not be retried", status)
		}
	}
}

func TestBackoffGrowsAndIsCapped(t *testing.T) {
	policy := DefaultRetry()
	previous := time.Duration(0)
	for attempt := 0; attempt < 8; attempt++ {
		delay := policy.backoff(attempt)
		if delay > policy.MaxBackoff {
			t.Errorf("attempt %d: delay %v exceeds the cap %v", attempt, delay, policy.MaxBackoff)
		}
		// Jitter only ever subtracts, so the floor is (1-Jitter) of the ideal.
		ideal := policy.InitialBackoff << attempt
		if ideal > policy.MaxBackoff {
			ideal = policy.MaxBackoff
		}
		floor := time.Duration(float64(ideal) * (1 - policy.Jitter))
		if delay < floor {
			t.Errorf("attempt %d: delay %v below the floor %v", attempt, delay, floor)
		}
		if attempt > 0 && attempt < 4 && delay <= previous/2 {
			t.Errorf("attempt %d: delay %v did not grow from %v", attempt, delay, previous)
		}
		previous = delay
	}
}

func TestBackoffDisabled(t *testing.T) {
	policy := RetryPolicy{MaxRetries: 3}
	if delay := policy.backoff(2); delay != 0 {
		t.Errorf("delay = %v, want 0 when backoff is unset", delay)
	}
}

func TestDelayForHonorsRetryAfter(t *testing.T) {
	policy := DefaultRetry()
	err := error(&APIError{Status: 429, RetryAfter: 2 * time.Second})
	delay, within := policy.delayFor(0, err, 0)
	if !within || delay != 2*time.Second {
		t.Errorf("delay = %v, within = %v; want the server's 2s", delay, within)
	}

	ignoring := policy
	ignoring.RespectRetryAfter = false
	delay, _ = ignoring.delayFor(0, err, 0)
	if delay == 2*time.Second {
		t.Error("retry-after was honored despite RespectRetryAfter=false")
	}
}

func TestDelayForStopsAtBudget(t *testing.T) {
	policy := DefaultRetry()
	policy.Budget = 3 * time.Second
	// A server asking for longer than the budget allows ends the call.
	err := error(&APIError{Status: 429, RetryAfter: 10 * time.Second})
	if _, within := policy.delayFor(0, err, 0); within {
		t.Error("a delay past the budget was accepted")
	}
	// Time already spent counts against the budget.
	if _, within := policy.delayFor(0, nil, 2*time.Second+900*time.Millisecond); within {
		t.Error("elapsed time was not counted against the budget")
	}
	// Fresh call, ordinary backoff, well inside the budget.
	if _, within := policy.delayFor(0, nil, 0); !within {
		t.Error("the first retry should fit the budget")
	}
}

func TestPolicyValidation(t *testing.T) {
	cases := []RetryPolicy{
		{MaxRetries: -1},
		{Jitter: 1.5},
		{Jitter: -0.1},
		{InitialBackoff: -time.Second},
		{Budget: -time.Second},
	}
	for _, policy := range cases {
		err := policy.validate()
		if err == nil {
			t.Errorf("%+v was accepted", policy)
			continue
		}
		if !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("%+v: error %v is not ErrInvalidConfig", policy, err)
		}
	}
	if err := DefaultRetry().validate(); err != nil {
		t.Errorf("DefaultRetry is invalid: %v", err)
	}
	if err := NoRetry().validate(); err != nil {
		t.Errorf("NoRetry is invalid: %v", err)
	}
}
