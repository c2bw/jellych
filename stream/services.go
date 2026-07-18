package stream

import (
	"net/http"
	"strings"
	"sync"
)

// Services owns the stateful streaming services used by one application.
type Services struct {
	Streams   *StreamRegistry
	Downloads *VODDownloader
	live      *LiveService
}

// NewServices creates an isolated live registry, media store, and downloader.
func NewServices(liveBaseURL string) *Services {
	var registryMu sync.Mutex
	managers := make(map[string]*manager)
	var storeMu sync.RWMutex
	items := make(map[string]map[string][]byte)
	store := NewLiveStore(&storeMu, &items)
	token := newLiveWriteToken()
	baseURL := strings.TrimRight(strings.TrimSpace(liveBaseURL), "/")

	registry := newStreamRegistry(&registryMu, &managers)
	registry.liveBaseURL = func() string { return baseURL }
	registry.getObject = store.GetObject
	registry.resetChannel = store.ResetChannel
	registry.clearChannel = store.ClearChannel
	registry.newCommand = func(inputURL, playlistURL, generation string) streamCommand {
		return newFFmpegCommandWithToken(inputURL, playlistURL, generation, token)
	}

	return &Services{
		Streams:   registry,
		Downloads: NewVODDownloader(),
		live: &LiveService{
			store:      store,
			registry:   registry,
			start:      registry.Start,
			writeToken: token,
		},
	}
}

// LiveHandler serves live media and limits playlist-driven starts to channels
// accepted by canAutoStart.
func (s *Services) LiveHandler(canAutoStart func(string) bool) http.Handler {
	service := *s.live
	service.canAutoStart = canAutoStart
	return service.LiveHandler()
}

// LiveWriteHandler accepts authenticated writes from this service's ffmpeg
// processes.
func (s *Services) LiveWriteHandler() http.Handler {
	return s.live.LiveWriteHandler()
}
