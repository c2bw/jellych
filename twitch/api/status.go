package twitchapi

import (
	"context"
	"net/url"
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
	return StreamInfoContext(context.Background(), clientID, accessToken, channel)
}

func StreamInfoContext(ctx context.Context, clientID, accessToken string, channel []string) (*TwitchResponse, error) {
	if len(channel) == 0 {
		return &TwitchResponse{Data: []Stream{}}, nil
	}
	if len(channel) > 100 {
		combined := &TwitchResponse{Data: []Stream{}}
		for start := 0; start < len(channel); start += 100 {
			end := min(start+100, len(channel))
			batch, err := StreamInfoContext(ctx, clientID, accessToken, channel[start:end])
			if err != nil {
				return nil, err
			}
			combined.Data = append(combined.Data, batch.Data...)
		}
		return combined, nil
	}
	// Build the request URL with query parameters
	q := url.Values{}
	for _, ch := range channel {
		q.Add("user_login", ch)
	}
	endpoint := "https://api.twitch.tv/helix/streams?" + q.Encode()
	var twitchResp TwitchResponse
	if err := getHelixJSON(ctx, clientID, accessToken, endpoint, &twitchResp); err != nil {
		return nil, err
	}
	return &twitchResp, nil
}
