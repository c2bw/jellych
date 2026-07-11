package client

import (
	"context"
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
	cancel       context.CancelFunc
	done         chan struct{}
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
	ctx, cancel := context.WithCancel(context.Background())
	twc.cancel = cancel
	twc.done = make(chan struct{})
	go twc.autoRefreshToken(ctx)
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

func (client *TwitchClient) Close() {
	client.mu.RLock()
	cancel := client.cancel
	done := client.done
	client.mu.RUnlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}

func (client *TwitchClient) autoRefreshToken(ctx context.Context) {
	defer close(client.done)
	client.mu.RLock()
	refreshBase := client.expiresIn
	client.mu.RUnlock()
	refreshInterval := refreshIntervalFor(refreshBase)
	ticker := time.NewTicker(refreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		tr, err := twitchapi.GetAccessTokenContext(ctx, client.clientID, client.clientSecret, "")
		if err != nil {
			if ctx.Err() != nil {
				return
			}
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
