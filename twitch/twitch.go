package twitch

import (
	"context"
	"errors"
	"sync"

	"github.com/c2bw/jellych/server/api"
	"github.com/c2bw/jellych/stream"
	"github.com/c2bw/jellych/twitch/client"
	"github.com/c2bw/jellych/twitch/manager"
)

// StartContext starts Twitch synchronization under the application context.
func StartContext(parent context.Context, c *client.TwitchClient, configPath string, state *api.APIState, downloads *stream.VODDownloader) (func(), error) {
	if parent == nil {
		parent = context.Background()
	}
	if state == nil {
		return nil, errors.New("api state is required")
	}
	if downloads == nil {
		return nil, errors.New("vod downloader is required")
	}
	m, err := manager.StartWithState(configPath, state)
	if err != nil {
		return nil, err
	}
	m.SetTwitchClient(c)
	ctx, cancel := context.WithCancel(parent)
	var wg sync.WaitGroup
	var stopOnce sync.Once
	wg.Add(2)
	go func() {
		defer wg.Done()
		m.UpdateStatus(ctx, c)
	}()
	go func() {
		defer wg.Done()
		m.SyncVODs(ctx, c, func(id string, removeMetadata func() error) (bool, error) {
			return pruneVODIfNoDownload(downloads, id, removeMetadata)
		})
	}()
	return func() {
		stopOnce.Do(func() {
			cancel()
			wg.Wait()
			_ = m.Close()
		})
	}, nil
}

func pruneVODIfNoDownload(downloads *stream.VODDownloader, id string, removeMetadata func() error) (bool, error) {
	err := downloads.RemoveMetadataIfNoDownload(id, removeMetadata)
	if errors.Is(err, stream.ErrVODDownloadProtected) || errors.Is(err, stream.ErrVODRemovalInProgress) {
		return false, nil
	}
	return err == nil, err
}
