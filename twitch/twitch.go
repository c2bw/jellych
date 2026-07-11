package twitch

import (
	"context"
	"sync"

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
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		m.UpdateStatus(ctx, c)
	}()
	go func() {
		defer wg.Done()
		m.ImportLatestVODs(ctx, c)
	}()
	return func() {
		cancel()
		wg.Wait()
	}, nil
}
