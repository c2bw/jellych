package twitchapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type VideosResponse struct {
	Data       []Video    `json:"data"`
	Pagination Pagination `json:"pagination"`
}

type Pagination struct {
	Cursor string `json:"cursor,omitempty"`
}

type Video struct {
	ID           string `json:"id"`
	StreamID     string `json:"stream_id"`
	UserID       string `json:"user_id"`
	UserLogin    string `json:"user_login"`
	UserName     string `json:"user_name"`
	Title        string `json:"title"`
	Description  string `json:"description"`
	CreatedAt    string `json:"created_at"`
	PublishedAt  string `json:"published_at"`
	URL          string `json:"url"`
	ThumbnailURL string `json:"thumbnail_url"`
	Viewable     string `json:"viewable"`
	ViewCount    int    `json:"view_count"`
	Language     string `json:"language"`
	Type         string `json:"type"`
	Duration     string `json:"duration"`
}

// VideosByUser fetches the latest archive VODs for a broadcaster user ID.
func VideosByUser(clientID, accessToken, userID string, first int) (*VideosResponse, error) {
	return VideosByUserContext(context.Background(), clientID, accessToken, userID, first)
}

func VideosByUserContext(ctx context.Context, clientID, accessToken, userID string, first int) (*VideosResponse, error) {
	if userID == "" {
		return &VideosResponse{Data: []Video{}}, nil
	}
	if first < 1 {
		first = 1
	}
	if first > 100 {
		first = 100
	}

	q := url.Values{}
	q.Set("user_id", userID)
	q.Set("type", "archive")
	q.Set("sort", "time")
	q.Set("first", strconv.Itoa(first))

	endpoint := "https://api.twitch.tv/helix/videos?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Client-ID", clientID)
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := helixHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	remaining := resp.Header.Get("Ratelimit-Remaining")
	limit := resp.Header.Get("Ratelimit-Limit")
	reset := resp.Header.Get("Ratelimit-Reset")
	if remaining != "" {
		slog.Debug("Twitch rate limit", "remaining", remaining, "limit", limit, "reset", reset)
	} else {
		slog.Debug("Twitch rate limit headers not present")
	}

	body, err := readTwitchResponse(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error: status=%d body=%s", resp.StatusCode, string(body))
	}

	var videosResp VideosResponse
	if err := json.Unmarshal(body, &videosResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}
	return &videosResp, nil
}

// VideosByID fetches a single VOD by Twitch video ID.
func VideosByID(clientID, accessToken, id string) (*VideosResponse, error) {
	return VideosByIDContext(context.Background(), clientID, accessToken, id)
}

func VideosByIDContext(ctx context.Context, clientID, accessToken, id string) (*VideosResponse, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return &VideosResponse{Data: []Video{}}, nil
	}

	q := url.Values{}
	q.Set("id", id)

	endpoint := "https://api.twitch.tv/helix/videos?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Client-ID", clientID)
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := helixHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	remaining := resp.Header.Get("Ratelimit-Remaining")
	limit := resp.Header.Get("Ratelimit-Limit")
	reset := resp.Header.Get("Ratelimit-Reset")
	if remaining != "" {
		slog.Debug("Twitch rate limit", "remaining", remaining, "limit", limit, "reset", reset)
	} else {
		slog.Debug("Twitch rate limit headers not present")
	}

	body, err := readTwitchResponse(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error: status=%d body=%s", resp.StatusCode, string(body))
	}

	var videosResp VideosResponse
	if err := json.Unmarshal(body, &videosResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}
	return &videosResp, nil
}
