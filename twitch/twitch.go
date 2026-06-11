package twitch

import (
	"context"

	"github.com/c2bw/jellych/server/api"
	"github.com/c2bw/jellych/stream"
	"github.com/c2bw/jellych/twitch/client"
	"github.com/c2bw/jellych/twitch/manager"
)

func Start(c *client.TwitchClient, configPath, liveBaseURL string) (func(), error) {
	stream.SetLiveBaseURL(liveBaseURL)
	m, err := manager.Start(configPath)
	if err != nil {
		return nil, err
	}
	m.SetTwitchClient(c)
	api.SetChannelStore(m)
	api.SetVODStore(m)
	ctx, cancel := context.WithCancel(context.Background())
	go m.UpdateStatus(ctx, c)
	go m.ImportLatestVODs(ctx, c)
	return cancel, nil
}
