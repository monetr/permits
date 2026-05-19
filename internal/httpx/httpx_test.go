package httpx

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/jarcoal/httpmock"
)

// newFast returns a client wired to httpmock with an intentionally tiny backoff so retry
// behaviour can be exercised without the tests actually sleeping for the real delays.
func newFast() *Client {
	c := New(2 * time.Second)
	c.BaseDelay = time.Millisecond // keep retries fast in tests
	httpmock.ActivateNonDefault(c.HTTP)

	return c
}

const testURL = "https://example.test/resource"

func TestGetBytesRetriesOn5xxThenSucceeds(t *testing.T) {
	c := newFast()
	defer httpmock.DeactivateAndReset()

	// Two 502s followed by a 200 proves the client retries server errors and ultimately
	// returns the successful body.
	httpmock.RegisterResponder("GET", testURL, httpmock.ResponderFromMultipleResponses([]*http.Response{
		httpmock.NewStringResponse(502, "bad gateway"),
		httpmock.NewStringResponse(502, "bad gateway"),
		httpmock.NewStringResponse(200, "ok"),
	}))

	body, err := c.GetBytes(context.Background(), testURL)
	if err != nil {
		t.Fatalf("GetBytes: %v", err)
	}
	if string(body) != "ok" {
		t.Errorf("body = %q, want ok", body)
	}

	// Exactly three calls: the two failures plus the eventual success.
	if n := httpmock.GetCallCountInfo()["GET "+testURL]; n != 3 {
		t.Errorf("called %d times, want 3 (2 failures + success)", n)
	}
}

func TestGetBytesRetriesOn429(t *testing.T) {
	c := newFast()
	defer httpmock.DeactivateAndReset()

	// 429 (rate limited) is a retryable status, so a single retry should recover the request.
	httpmock.RegisterResponder("GET", testURL, httpmock.ResponderFromMultipleResponses([]*http.Response{
		httpmock.NewStringResponse(429, "slow down"),
		httpmock.NewStringResponse(200, "done"),
	}))

	body, err := c.GetBytes(context.Background(), testURL)
	if err != nil || string(body) != "done" {
		t.Fatalf("GetBytes = (%q, %v), want (done, nil)", body, err)
	}
	if n := httpmock.GetCallCountInfo()["GET "+testURL]; n != 2 {
		t.Errorf("called %d times, want 2", n)
	}
}

func TestGetBytesDoesNotRetry4xx(t *testing.T) {
	c := newFast()
	defer httpmock.DeactivateAndReset()

	// A 404 is a deterministic client error; retrying it would only waste time, so the client
	// must give up immediately after the first attempt.
	httpmock.RegisterResponder("GET", testURL, httpmock.NewStringResponder(404, "nope"))

	if _, err := c.GetBytes(context.Background(), testURL); err == nil {
		t.Fatal("expected error for 404")
	}
	if n := httpmock.GetCallCountInfo()["GET "+testURL]; n != 1 {
		t.Errorf("called %d times, want 1 (404 must not retry)", n)
	}
}

func TestGetBytesExhaustsRetries(t *testing.T) {
	c := newFast()
	defer httpmock.DeactivateAndReset()

	// A persistently failing endpoint must eventually surface an error after the configured
	// number of retries rather than looping forever.
	httpmock.RegisterResponder("GET", testURL, httpmock.NewStringResponder(500, "boom"))

	if _, err := c.GetBytes(context.Background(), testURL); err == nil {
		t.Fatal("expected error after exhausting retries")
	}

	// The total attempt count is the initial request plus the configured retry budget.
	if n := httpmock.GetCallCountInfo()["GET "+testURL]; n != c.MaxRetries+1 {
		t.Errorf("called %d times, want %d (initial + retries)", n, c.MaxRetries+1)
	}
}

func TestGetBytesContextCancelledDuringBackoff(t *testing.T) {
	// Use a backoff long enough that the context deadline is guaranteed to fire while the
	// client is sleeping between retries rather than while it is in flight.
	c := New(2 * time.Second)
	c.BaseDelay = 50 * time.Millisecond
	httpmock.ActivateNonDefault(c.HTTP)
	defer httpmock.DeactivateAndReset()

	httpmock.RegisterResponder("GET", testURL, httpmock.NewStringResponder(503, "unavailable"))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	// Cancellation during the backoff sleep must abort the call with an error instead of
	// continuing to retry past the deadline.
	if _, err := c.GetBytes(ctx, testURL); err == nil {
		t.Fatal("expected context error during backoff")
	}
}
