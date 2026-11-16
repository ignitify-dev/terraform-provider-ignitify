// SPDX-License-Identifier: MPL-2.0

package client

import (
	"net/http"
	"strconv"
	"time"
)

const (
	// maxAttempts is the number of tries of one request, the first included.
	maxAttempts = 4
	// backoffBase is the wait before the second try; each later try doubles it.
	backoffBase = time.Second
	// maxWait caps one wait, also one the server asks for in Retry-After.
	maxWait = 30 * time.Second
)

// attemptOutcome is what one try of a request gave.
type attemptOutcome struct {
	// attempt counts from 0 (the first try).
	attempt int
	// transportFailed is true when no answer came back (a network error or a timeout).
	transportFailed bool
	status          int
	retryAfter      string
	// reason is the `reason` of an error answer, empty when it has none.
	reason string
	// idempotent is true when the request carried an Idempotency-Key.
	idempotent bool
	now        time.Time
}

// retryDecision says whether to try again and how long to wait first.
type retryDecision struct {
	retry bool
	wait  time.Duration
	// refusedWait is set with retry false when a refusal with a reason asks
	// for a wait longer than maxWait (a rate window of an hour or a day).
	refusedWait time.Duration
}

var noRetry = retryDecision{}

// decideRetry decides whether a failed try is tried again. Every request the
// provider sends may be repeated: reads, deletes (a 404 is done), the
// set-like PUTs, and writes that carry an Idempotency-Key. Retried:
//   - no answer at all;
//   - 429 with Retry-After, waiting that long, up to maxWait; a 429 without it
//     only when it has no reason (the API rate limit). A 429 with a reason and
//     no Retry-After is a tier quota (such as the key or secret cap) and fails
//     at once; so does a 429 with a reason whose Retry-After is longer than
//     maxWait (a rate window of an hour or a day), reporting that wait;
//   - 503 only with Retry-After (a 503 without it is a lasting refusal, such as
//     a platform with no secret vault);
//   - 409 with no reason on a repeat of an idempotent write: the first try of
//     the same key is still running on the server.
//
// Pure.
func decideRetry(o attemptOutcome) retryDecision {
	if o.attempt+1 >= maxAttempts {
		return noRetry
	}
	backoff := backoffFor(o.attempt)
	switch {
	case o.transportFailed:
		return retryDecision{retry: true, wait: backoff}
	case o.status == http.StatusTooManyRequests:
		if wait, ok := parseRetryAfter(o.retryAfter, o.now); ok {
			if o.reason != "" && wait > maxWait {
				return retryDecision{refusedWait: wait}
			}
			return retryDecision{retry: true, wait: capWait(wait)}
		}
		if o.reason == "" {
			return retryDecision{retry: true, wait: backoff}
		}
		return noRetry
	case o.status == http.StatusServiceUnavailable:
		if wait, ok := parseRetryAfter(o.retryAfter, o.now); ok {
			return retryDecision{retry: true, wait: capWait(wait)}
		}
		return noRetry
	case o.status == http.StatusConflict && o.reason == "" && o.idempotent && o.attempt > 0:
		return retryDecision{retry: true, wait: backoff}
	default:
		return noRetry
	}
}

// backoffFor is the wait after try `attempt` (from 0): base, 2×base, 4×base, capped. Pure.
func backoffFor(attempt int) time.Duration {
	wait := backoffBase << attempt
	if wait <= 0 || wait > maxWait {
		return maxWait
	}
	return wait
}

// parseRetryAfter reads a Retry-After value: delta seconds or an HTTP date,
// not capped. A past date waits 0. Pure.
func parseRetryAfter(value string, now time.Time) (time.Duration, bool) {
	if value == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(value); err == nil {
		if secs < 0 {
			return 0, false
		}
		return time.Duration(secs) * time.Second, true
	}
	when, err := http.ParseTime(value)
	if err != nil {
		return 0, false
	}
	return max(when.Sub(now), 0), true
}

func capWait(d time.Duration) time.Duration {
	return min(d, maxWait)
}
