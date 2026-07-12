package twitchapi

import (
	"context"
	"net/url"
	"strings"
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
	return UserInfoContext(context.Background(), clientID, accessToken, channels)
}

func UserInfoContext(ctx context.Context, clientID, accessToken string, channels []string) (*UsersResponse, error) {
	if len(channels) == 0 {
		return &UsersResponse{Data: []User{}}, nil
	}
	if len(channels) > 100 {
		combined := &UsersResponse{Data: []User{}}
		for start := 0; start < len(channels); start += 100 {
			end := min(start+100, len(channels))
			batch, err := UserInfoContext(ctx, clientID, accessToken, channels[start:end])
			if err != nil {
				return nil, err
			}
			combined.Data = append(combined.Data, batch.Data...)
		}
		return combined, nil
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
	var usersResp UsersResponse
	if err := getHelixJSON(ctx, clientID, accessToken, endpoint, &usersResp); err != nil {
		return nil, err
	}
	return &usersResp, nil
}
