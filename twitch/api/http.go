package twitchapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

const maxTwitchResponseBytes = 4 << 20

var helixHTTPClient = &http.Client{Timeout: 20 * time.Second}

func getHelixJSON(ctx context.Context, clientID, accessToken, endpoint string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Client-ID", clientID)
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := helixHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	logRateLimit(resp.Header)
	body, err := readTwitchResponse(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("API error: status=%d body=%s", resp.StatusCode, string(body))
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}
	return nil
}

func logRateLimit(header http.Header) {
	remaining := header.Get("Ratelimit-Remaining")
	if remaining != "" {
		slog.Debug("Twitch rate limit", "remaining", remaining, "limit", header.Get("Ratelimit-Limit"), "reset", header.Get("Ratelimit-Reset"))
	} else {
		slog.Debug("Twitch rate limit headers not present")
	}
}

func readTwitchResponse(body io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, maxTwitchResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxTwitchResponseBytes {
		return nil, fmt.Errorf("twitch response exceeds %d bytes", maxTwitchResponseBytes)
	}
	return data, nil
}
