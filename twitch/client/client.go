package client

import (
	"log/slog"
	"sync"
	"time"

	twitchapi "github.com/c2bw/jellych/twitch/api"
)

type TwitchClient struct {
	clientID     string
	clientSecret string
	accessToken  string
	expiresIn    int
	mu           sync.RWMutex
}

func NewClient(clientID, clientSecret string) (*TwitchClient, error) {
	tr, err := twitchapi.GetAccessToken(clientID, clientSecret, "")
	if err != nil {
		return nil, err
	}

	twc := &TwitchClient{
		clientID:     clientID,
		clientSecret: clientSecret,
		accessToken:  tr.AccessToken,
		expiresIn:    tr.ExpiresIn,
	}
	//Auto-refresh token using a timer based on expires_in
	go twc.autoRefreshToken()
	return twc, nil
}

func (c *TwitchClient) AccessToken() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.accessToken
}

func (c *TwitchClient) ClientID() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.clientID
}

func (client *TwitchClient) autoRefreshToken() {
	client.mu.RLock()
	refreshBase := client.expiresIn
	client.mu.RUnlock()
	refreshInterval := refreshIntervalFor(refreshBase)
	ticker := time.NewTicker(refreshInterval)
	defer ticker.Stop()
	for {
		<-ticker.C
		tr, err := twitchapi.GetAccessToken(client.clientID, client.clientSecret, "")
		if err != nil {
			slog.Error("failed to refresh Twitch token", "error", err)
			// Retry after a short delay if refresh fails
			ticker.Reset(30 * time.Second)
			continue
		}
		client.mu.Lock()
		client.accessToken = tr.AccessToken
		client.expiresIn = tr.ExpiresIn
		expiresIn := client.expiresIn
		client.mu.Unlock()
		ticker.Reset(refreshIntervalFor(expiresIn)) // Reset timer based on new expiration
		slog.Info("successfully refreshed Twitch token", "expires_in", expiresIn)
	}
}

func refreshIntervalFor(expiresIn int) time.Duration {
	// Refresh 1 minute before expiration; clamp to avoid non-positive intervals.
	refreshIntervalSec := expiresIn - 60
	if refreshIntervalSec < 1 {
		refreshIntervalSec = 1
	}
	return time.Duration(refreshIntervalSec) * time.Second
}
