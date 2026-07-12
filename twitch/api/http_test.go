package twitchapi

import (
	"context"
	"fmt"
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

func TestGetHelixJSONAuthorizesValidatesAndDecodes(t *testing.T) {
	original := helixHTTPClient
	t.Cleanup(func() { helixHTTPClient = original })
	helixHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet {
			t.Fatalf("request method = %q; want GET", req.Method)
		}
		if got := req.Header.Get("Client-ID"); got != "client" {
			t.Fatalf("Client-ID = %q; want client", got)
		}
		if got := req.Header.Get("Authorization"); got != "Bearer token" {
			t.Fatalf("Authorization = %q; want bearer token", got)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Ratelimit-Remaining": {"99"},
				"Ratelimit-Limit":     {"100"},
				"Ratelimit-Reset":     {"123"},
			},
			Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"1","login":"channel"}]}`)),
		}, nil
	})}

	var response UsersResponse
	if err := getHelixJSON(context.Background(), "client", "token", "https://api.twitch.tv/helix/users", &response); err != nil {
		t.Fatalf("get Helix JSON: %v", err)
	}
	if len(response.Data) != 1 || response.Data[0].Login != "channel" {
		t.Fatalf("decoded response = %#v", response)
	}
}

func TestGetHelixJSONRejectsErrorStatusAndInvalidJSON(t *testing.T) {
	for _, test := range []struct {
		name       string
		statusCode int
		body       string
		want       string
	}{
		{name: "error status", statusCode: http.StatusUnauthorized, body: `{"message":"invalid token"}`, want: "API error: status=401"},
		{name: "invalid JSON", statusCode: http.StatusOK, body: `{`, want: "failed to parse response"},
	} {
		t.Run(test.name, func(t *testing.T) {
			original := helixHTTPClient
			t.Cleanup(func() { helixHTTPClient = original })
			helixHTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: test.statusCode,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(test.body)),
				}, nil
			})}

			var response UsersResponse
			err := getHelixJSON(context.Background(), "client", "token", "https://api.twitch.tv/helix/users", &response)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v; want substring %q", err, test.want)
			}
		})
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

func TestVideosByIDsContextBatchesLargeRequests(t *testing.T) {
	original := helixHTTPClient
	t.Cleanup(func() { helixHTTPClient = original })
	var requests atomic.Int32
	helixHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests.Add(1)
		if got := len(req.URL.Query()["id"]); got > 100 {
			t.Fatalf("batch exceeded Twitch limit: %d", got)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"data":[]}`)),
		}, nil
	})}

	ids := make([]string, 201)
	for i := range ids {
		ids[i] = fmt.Sprintf("%d", i+1)
	}
	if _, err := VideosByIDsContext(context.Background(), "client", "token", ids); err != nil {
		t.Fatalf("unexpected batched request error: %v", err)
	}
	if got := requests.Load(); got != 3 {
		t.Fatalf("expected 3 requests, got %d", got)
	}
}
