package bilibili

import (
	"context"
	"errors"
	"testing"

	"github.com/sony/gobreaker"
)

func newRetryTestClient() *Client {
	return New(nil, "", "", false, 0, nil)
}

// TestWithRetryStopsOnBusinessCode pins the fix for the retry amplification:
// an HTTP 200 carrying a non-zero API code is a decision, not a fault, so it
// must cost exactly one attempt. It used to be retried by the outer loop on
// top of retryablehttp's own retries.
func TestWithRetryStopsOnBusinessCode(t *testing.T) {
	c := newRetryTestClient()
	calls := 0
	err := c.withRetry(context.Background(), func() error {
		calls++
		return apiCodeError(-404, "啥都木有")
	})
	if err == nil {
		t.Fatal("withRetry returned nil for a business-code failure")
	}
	if calls != 1 {
		t.Fatalf("business-code failure was attempted %d times, want 1", calls)
	}
	if !isNonRetryable(err) {
		t.Fatalf("error lost its non-retryable marker: %v", err)
	}
}

// TestWithRetryStillRetriesTransientErrors guards against over-correcting: a
// plain error is still worth another attempt.
func TestWithRetryStillRetriesTransientErrors(t *testing.T) {
	c := newRetryTestClient()
	c.minBackoff = 0
	c.maxBackoff = 0
	calls := 0
	err := c.withRetry(context.Background(), func() error {
		calls++
		return errors.New("bilibili: connection reset")
	})
	if err == nil {
		t.Fatal("withRetry returned nil for a persistent transport failure")
	}
	if calls != c.businessRetries+1 {
		t.Fatalf("transient failure was attempted %d times, want %d", calls, c.businessRetries+1)
	}
}

func TestWithRetrySucceedsAfterTransientError(t *testing.T) {
	c := newRetryTestClient()
	c.minBackoff = 0
	c.maxBackoff = 0
	calls := 0
	err := c.withRetry(context.Background(), func() error {
		calls++
		if calls == 1 {
			return errors.New("bilibili: temporary failure")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("withRetry = %v, want nil once the call succeeds", err)
	}
	if calls != 2 {
		t.Fatalf("call count = %d, want 2", calls)
	}
}

// TestBusinessCodeDoesNotOpenBreaker covers the regression that fast-failing
// business codes could otherwise introduce: a handful of deleted videos must
// not trip the breaker and block every healthy bilibili request.
func TestBusinessCodeDoesNotOpenBreaker(t *testing.T) {
	c := newRetryTestClient()
	for i := range 20 {
		err := c.execute(context.Background(), func() error {
			return apiCodeError(-404, "啥都木有")
		})
		if err == nil {
			t.Fatalf("call %d: execute returned nil for a business-code failure", i)
		}
		if errors.Is(err, gobreaker.ErrOpenState) || errors.Is(err, gobreaker.ErrTooManyRequests) {
			t.Fatalf("breaker opened after %d business-code failures", i+1)
		}
	}
	// The breaker must still be usable for a healthy call.
	if err := c.execute(context.Background(), func() error { return nil }); err != nil {
		t.Fatalf("healthy call rejected after business-code failures: %v", err)
	}
}
