package twitchapi

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"time"
)

type TwitchResponse struct {
	Data []Stream `json:"data"`
}

type Stream struct {
	ID           string   `json:"id"`
	UserID       string   `json:"user_id"`
	UserLogin    string   `json:"user_login"`
	UserName     string   `json:"user_name"`
	GameID       string   `json:"game_id"`
	GameName     string   `json:"game_name"`
	Type         string   `json:"type"`
	Title        string   `json:"title"`
	Tags         []string `json:"tags"`
	ViewerCount  int      `json:"viewer_count"`
	StartedAt    string   `json:"started_at"`
	Language     string   `json:"language"`
	ThumbnailURL string   `json:"thumbnail_url"`
	TagIDs       []string `json:"tag_ids"`
	IsMature     bool     `json:"is_mature"`
}

// GET https://api.twitch.tv/helix/streams?user_login=CHANNEL_NAME
func StreamInfo(clientID, accessToken string, channel []string) (*TwitchResponse, error) {
	if len(channel) == 0 {
		return &TwitchResponse{Data: []Stream{}}, nil
	}
	// Build the request URL with query parameters
	q := url.Values{}
	for _, ch := range channel {
		q.Add("user_login", ch)
	}
	url := "https://api.twitch.tv/helix/streams?" + q.Encode()
	// Create a new HTTP request
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	// Set the required headers
	req.Header.Set("Client-ID", clientID)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	// Send the HTTP request
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	// Print remaining requests using Twitch rate-limit response headers
	remaining := resp.Header.Get("Ratelimit-Remaining")
	limit := resp.Header.Get("Ratelimit-Limit")
	reset := resp.Header.Get("Ratelimit-Reset")
	if remaining != "" {
		slog.Debug("Twitch rate limit", "remaining", remaining, "limit", limit, "reset", reset)
	} else {
		slog.Debug("Twitch rate limit headers not present")
	}
	// Read and parse the response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error: status=%d body=%s", resp.StatusCode, string(body))
	}
	var twitchResp TwitchResponse
	if err := json.Unmarshal(body, &twitchResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}
	return &twitchResp, nil
}
