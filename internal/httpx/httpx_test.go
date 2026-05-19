package httpx

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/jarcoal/httpmock"
)

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

	if n := httpmock.GetCallCountInfo()["GET "+testURL]; n != 3 {
		t.Errorf("called %d times, want 3 (2 failures + success)", n)
	}
}

func TestGetBytesRetriesOn429(t *testing.T) {
	c := newFast()
	defer httpmock.DeactivateAndReset()

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

	httpmock.RegisterResponder("GET", testURL, httpmock.NewStringResponder(500, "boom"))

	if _, err := c.GetBytes(context.Background(), testURL); err == nil {
		t.Fatal("expected error after exhausting retries")
	}

	if n := httpmock.GetCallCountInfo()["GET "+testURL]; n != c.MaxRetries+1 {
		t.Errorf("called %d times, want %d (initial + retries)", n, c.MaxRetries+1)
	}
}

func TestGetBytesContextCancelledDuringBackoff(t *testing.T) {
	c := New(2 * time.Second)
	c.BaseDelay = 50 * time.Millisecond
	httpmock.ActivateNonDefault(c.HTTP)
	defer httpmock.DeactivateAndReset()

	httpmock.RegisterResponder("GET", testURL, httpmock.NewStringResponder(503, "unavailable"))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	if _, err := c.GetBytes(ctx, testURL); err == nil {
		t.Fatal("expected context error during backoff")
	}
}
