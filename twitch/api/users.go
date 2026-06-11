package twitchapi

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type UsersResponse struct {
	Data []User `json:"data"`
}

type User struct {
	ID              string `json:"id"`
	Login           string `json:"login"`
	DisplayName     string `json:"display_name"`
	ProfileImageURL string `json:"profile_image_url"`
}

// GET https://api.twitch.tv/helix/users?login=CHANNEL_NAME
func UserInfo(clientID, accessToken string, channels []string) (*UsersResponse, error) {
	if len(channels) == 0 {
		return &UsersResponse{Data: []User{}}, nil
	}

	q := url.Values{}
	for _, channel := range channels {
		channel = strings.TrimSpace(channel)
		if channel == "" {
			continue
		}
		q.Add("login", channel)
	}
	if len(q["login"]) == 0 {
		return &UsersResponse{Data: []User{}}, nil
	}

	endpoint := "https://api.twitch.tv/helix/users?" + q.Encode()
	req, err := http.NewRequest("GET", endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Client-ID", clientID)
	req.Header.Set("Authorization", "Bearer "+accessToken)

	httpClient := &http.Client{Timeout: 20 * time.Second}
	resp, err := httpClient.Do(req)
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

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error: status=%d body=%s", resp.StatusCode, string(body))
	}

	var usersResp UsersResponse
	if err := json.Unmarshal(body, &usersResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}
	return &usersResp, nil
}
