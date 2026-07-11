package twitchapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// TokenResponse represents a Twitch OAuth token response.
type TokenResponse struct {
	AccessToken  string   `json:"access_token"`
	RefreshToken string   `json:"refresh_token,omitempty"`
	ExpiresIn    int      `json:"expires_in,omitempty"`
	Scope        []string `json:"scope,omitempty"`
	TokenType    string   `json:"token_type,omitempty"`
}

func GetAccessToken(clientID, clientSecret, redirectURI string) (*TokenResponse, error) {
	return GetAccessTokenContext(context.Background(), clientID, clientSecret, redirectURI)
}

func GetAccessTokenContext(ctx context.Context, clientID, clientSecret, redirectURI string) (*TokenResponse, error) {
	endpoint := "https://id.twitch.tv/oauth2/token"
	v := url.Values{}
	v.Set("client_id", clientID)
	v.Set("client_secret", clientSecret)
	v.Set("grant_type", "client_credentials")
	if redirectURI != "" {
		v.Set("redirect_uri", redirectURI)
	}

	httpClient := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(v.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := readTwitchResponse(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read token response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token request failed: status=%d body=%s", resp.StatusCode, string(body))
	}

	var tr TokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, fmt.Errorf("failed to parse token response: %w", err)
	}
	return &tr, nil
}
