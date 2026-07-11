package twitchapi

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestReadTwitchResponseRejectsOversizeBody(t *testing.T) {
	_, err := readTwitchResponse(strings.NewReader(strings.Repeat("x", maxTwitchResponseBytes+1)))
	if err == nil {
		t.Fatal("expected oversized response error")
	}
}

func TestUserInfoContextBatchesLargeRequests(t *testing.T) {
	original := helixHTTPClient
	t.Cleanup(func() { helixHTTPClient = original })
	var requests atomic.Int32
	helixHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests.Add(1)
		if got := len(req.URL.Query()["login"]); got > 100 {
			t.Fatalf("batch exceeded Twitch limit: %d", got)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"data":[]}`)),
		}, nil
	})}

	channels := make([]string, 201)
	for i := range channels {
		channels[i] = "channel"
	}
	if _, err := UserInfoContext(context.Background(), "client", "token", channels); err != nil {
		t.Fatalf("unexpected batched request error: %v", err)
	}
	if got := requests.Load(); got != 3 {
		t.Fatalf("expected 3 requests, got %d", got)
	}
}
