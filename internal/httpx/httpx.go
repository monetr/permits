// Package httpx is an internal helper: a shared HTTP client with a per-request timeout and
// bounded exponential-backoff retries on transient failures (5xx, 429, and network errors). It is
// an implementation detail of the providers.
package httpx

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"
)

// Client wraps [http.Client] with retry behaviour.
type Client struct {
	HTTP       *http.Client
	MaxRetries int
	BaseDelay  time.Duration
}

// New returns a [Client] with the given per-request timeout.
func New(timeout time.Duration) *Client {
	return &Client{
		HTTP:       &http.Client{Timeout: timeout},
		MaxRetries: 3,
		BaseDelay:  300 * time.Millisecond,
	}
}

// GetBytes fetches url and returns the full body. The body is always drained and closed. Non-2xx
// responses are treated as errors; 5xx and 429 are retried.
func (c *Client) GetBytes(ctx context.Context, url string) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt <= c.MaxRetries; attempt++ {
		// Every attempt after the first waits an exponentially growing delay, but still honours
		// context cancellation so a cancelled caller does not block on the backoff timer.
		if attempt > 0 {
			delay := time.Duration(float64(c.BaseDelay) * math.Pow(2, float64(attempt-1)))
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}

		// A transport-level failure is assumed transient (DNS blip, reset connection); record it
		// and let the loop retry.
		resp, err := c.HTTP.Do(req)
		if err != nil {
			lastErr = err
			continue // network error: retry
		}

		// Drain and close the body before inspecting the status so the connection can be reused
		// regardless of which branch we take next.
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode/100 == 2 {
			if readErr != nil {
				lastErr = fmt.Errorf("reading %s: %w", url, readErr)
				continue
			}

			return body, nil
		}

		// Non-2xx: 429 and 5xx are worth retrying, while other 4xx responses are permanent and
		// returned immediately.
		lastErr = fmt.Errorf("GET %s: unexpected status %s", url, resp.Status)
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode/100 == 5 {
			continue // transient: retry
		}

		return nil, lastErr // permanent (4xx): do not retry
	}

	return nil, fmt.Errorf("exhausted retries: %w", lastErr)
}
