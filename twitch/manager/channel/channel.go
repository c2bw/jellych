package channel

import (
	"context"
	"strings"

	twitchapi "github.com/c2bw/jellych/twitch/api"
	"github.com/c2bw/jellych/twitch/client"
)

type Info struct {
	Name    string `json:"name"`
	IconURL string `json:"icon_url,omitempty"`
}

type Status struct {
	Name    string `json:"name"`
	Online  bool   `json:"online"`
	Viewers int    `json:"viewers,omitempty"`
	Title   string `json:"title,omitempty"`
	Game    string `json:"game,omitempty"`
}

func FetchStatus(client *client.TwitchClient, channels []string) ([]Status, error) {
	return FetchStatusContext(context.Background(), client, channels)
}

func FetchStatusContext(ctx context.Context, client *client.TwitchClient, channels []string) ([]Status, error) {
	info, err := twitchapi.StreamInfoContext(ctx, client.ClientID(), client.AccessToken(), channels)
	if err != nil {
		return []Status{}, err
	}
	var status []Status
	for _, data := range info.Data {
		status = append(status, Status{
			Name:    data.UserLogin,
			Online:  data.Type == "live",
			Viewers: data.ViewerCount,
			Title:   data.Title,
			Game:    data.GameName,
		})
	}
	return status, nil
}

func FetchIconURL(client *client.TwitchClient, channelName string) (string, error) {
	return FetchIconURLContext(context.Background(), client, channelName)
}

func FetchIconURLContext(ctx context.Context, client *client.TwitchClient, channelName string) (string, error) {
	info, err := twitchapi.UserInfoContext(ctx, client.ClientID(), client.AccessToken(), []string{channelName})
	if err != nil {
		return "", err
	}
	for _, data := range info.Data {
		if strings.EqualFold(data.Login, channelName) {
			return data.ProfileImageURL, nil
		}
	}
	if len(info.Data) > 0 {
		return info.Data[0].ProfileImageURL, nil
	}
	return "", nil
}
