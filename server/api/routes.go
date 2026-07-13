package api

import (
	"net/http"
	"time"
)

var defaultAPI = newAPI(defaultState, Dependencies{})

// API owns the HTTP route surface for the API package.
type API struct {
	state              *APIState
	streams            StreamOperations
	playback           *PlaybackTracker
	vodMediaRegistry   *vodMediaRegistry
	vodMediaHTTPClient *http.Client
	now                func() time.Time
}

// New returns an API instance with the package's configured dependencies.
func New() *API {
	return newAPI(defaultState, Dependencies{})
}

// NewWithState returns an isolated API instance backed by state.
func NewWithState(state *APIState) *API {
	return newAPI(state, Dependencies{})
}

// NewWithDependencies returns an API instance with isolated runtime state and
// caller-supplied services.
func NewWithDependencies(state *APIState, dependencies Dependencies) *API {
	return newAPI(state, dependencies)
}

func newAPI(state *APIState, dependencies Dependencies) *API {
	if state == nil {
		state = &APIState{}
	}
	now := dependencies.Now
	if now == nil {
		now = time.Now
	}
	mediaClient := dependencies.VODMediaHTTPClient
	if mediaClient == nil {
		mediaClient = defaultVODMediaHTTPClient
	}
	return &API{
		state:              state,
		streams:            fillStreamOperationDefaults(dependencies.Streams),
		playback:           newPlaybackTracker(now),
		vodMediaRegistry:   &vodMediaRegistry{},
		vodMediaHTTPClient: mediaClient,
		now:                now,
	}
}

// Handler returns an http.Handler that exposes API endpoints for controlling streaming.
func Handler() http.Handler {
	return defaultAPI.Handler()
}

// Handler returns an http.Handler that exposes API endpoints for controlling streaming.
func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	control := func(handler http.HandlerFunc) http.HandlerFunc {
		return a.requireControlAuth(handler)
	}

	mux.HandleFunc("GET /api/ping", a.handlePing)
	mux.HandleFunc("GET /api/ping/", a.handlePing)
	mux.HandleFunc("GET /api/channels", a.handleChannels)
	mux.HandleFunc("GET /api/vod-presets", a.handleVODPresets)
	mux.HandleFunc("GET /api/vods", a.handleVODs)
	mux.HandleFunc("POST /api/vods", control(a.handleAddVOD))
	mux.HandleFunc("GET /api/vods/{id}/download", a.handleGetVODDownload)
	mux.HandleFunc("POST /api/vods/{id}/download", control(a.handleDownloadVOD))
	mux.HandleFunc("POST /api/vods/{id}/convert", control(a.handleConvertVOD))
	mux.HandleFunc("DELETE /api/vods/{id}/download", control(a.handleDeleteVODDownload))
	mux.HandleFunc("DELETE /api/vods/{id}", control(a.handleRemoveVOD))
	mux.HandleFunc("GET /api/vods.m3u", a.handleGetVODM3U)
	mux.HandleFunc("GET /api/streams", a.handleActiveStreams)
	mux.HandleFunc("POST /api/stream/{channel}", control(a.handleStartStream))
	mux.HandleFunc("POST /api/channels/add", control(a.handleAddChannel))
	mux.HandleFunc("POST /api/channels/remove", control(a.handleRemoveChannelByBody))
	mux.HandleFunc("DELETE /api/channels/remove/{name}", control(a.handleRemoveChannelByPath))
	mux.HandleFunc("POST /api/stop/{channel}", control(a.handleStopChannel))
	mux.HandleFunc("DELETE /api/stop/{channel}", control(a.handleStopChannel))
	mux.HandleFunc("GET /api/stream-ready/{channel}", a.handleStreamReady)
	mux.HandleFunc("GET /api/status", a.handleGetChannelStatus)
	mux.HandleFunc("GET /api/twitch.m3u", a.handleGetM3U)
	mux.HandleFunc("POST /api/status", control(a.handleSetChannelStatus))
	mux.HandleFunc("POST /api/playing/{channel}", control(a.handleRecordPlaying))
	mux.HandleFunc("GET /api/playing", a.handleGetPlayingCounts)
	mux.HandleFunc("POST /api/jellyfin/webhook", a.handleJellyfinWebhook)
	mux.HandleFunc("GET /vod/{id}/index.m3u8", a.handleGetVODPlaylist)
	mux.HandleFunc("HEAD /vod/{id}/index.m3u8", a.handleGetVODPlaylist)
	mux.HandleFunc("GET /vod/{id}/file.mkv", a.handleGetLocalVOD)
	mux.HandleFunc("HEAD /vod/{id}/file.mkv", a.handleGetLocalVOD)
	mux.HandleFunc("GET /vod/{id}/media/{token}", a.handleGetVODMedia)
	mux.HandleFunc("HEAD /vod/{id}/media/{token}", a.handleGetVODMedia)

	return mux
}
