package prometheus

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/api"
)

// retryingClient wraps an api.Client and repeats a request after a bounded,
// exponentially growing pause. It retries only what can plausibly succeed the
// second time: a 5xx answer (Prometheus returns 503 for a query that timed out
// while busy) and a timeout. A 4xx answer means the request itself is wrong, and
// a TLS failure means the hop is misconfigured, so neither is retried.
type retryingClient struct {
	api.Client
	maxAttempts    int
	initialBackoff time.Duration
	maxBackoff     time.Duration
}

func (c retryingClient) Do(ctx context.Context, request *http.Request) (*http.Response, []byte, error) {
	for attempt := 1; ; attempt++ {
		attemptRequest, err := freshRequest(request)
		if err != nil {
			return nil, nil, err
		}
		response, body, err := c.Client.Do(ctx, attemptRequest)
		if attempt >= c.maxAttempts || !shouldRetry(ctx, response, err) {
			if err != nil && attempt > 1 {
				err = fmt.Errorf("after %d attempts: %w", attempt, err)
			}
			return response, body, err
		}
		select {
		case <-ctx.Done():
			return response, body, ctx.Err()
		case <-time.After(c.backoff(attempt)):
		}
	}
}

// backoff is initialBackoff doubled for every attempt already made, capped at
// maxBackoff.
func (c retryingClient) backoff(attemptsMade int) time.Duration {
	pause := c.initialBackoff
	for range attemptsMade - 1 {
		pause *= 2
		if pause >= c.maxBackoff {
			return c.maxBackoff
		}
	}
	return min(pause, c.maxBackoff)
}

// shouldRetry reports whether the outcome of one attempt is worth repeating.
// A cancelled or expired caller context ends the retries: the caller gave up.
func shouldRetry(ctx context.Context, response *http.Response, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	if err != nil {
		var networkError net.Error
		return errors.As(err, &networkError) && networkError.Timeout()
	}
	return response != nil && response.StatusCode >= http.StatusInternalServerError
}

// freshRequest copies the request with a new body, because the body of a POST
// (query_range uses one) is consumed by the first attempt.
func freshRequest(request *http.Request) (*http.Request, error) {
	copied := request.Clone(request.Context())
	if request.GetBody == nil {
		return copied, nil
	}
	body, err := request.GetBody()
	if err != nil {
		return nil, fmt.Errorf("rewind request body for retry: %w", err)
	}
	copied.Body = body
	return copied, nil
}
