package api

import "net/http"

var defaultAPI = &API{state: defaultState}

// API owns the HTTP route surface for the API package.
type API struct {
	state *APIState
}

// New returns an API instance with the package's configured dependencies.
func New() *API {
	return &API{state: defaultState}
}

// Handler returns an http.Handler that exposes API endpoints for controlling streaming.
func Handler() http.Handler {
	return defaultAPI.Handler()
}

// Handler returns an http.Handler that exposes API endpoints for controlling streaming.
func (a *API) Handler() http.Handler {
	startIdleMonitor()
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/ping", a.handlePing)
	mux.HandleFunc("GET /api/ping/", a.handlePing)
	mux.HandleFunc("GET /api/channels", a.handleChannels)
	mux.HandleFunc("GET /api/vods", a.handleVODs)
	mux.HandleFunc("POST /api/vods", a.handleAddVOD)
	mux.HandleFunc("GET /api/vods/{id}/download", a.handleGetVODDownload)
	mux.HandleFunc("POST /api/vods/{id}/download", a.handleDownloadVOD)
	mux.HandleFunc("DELETE /api/vods/{id}/download", a.handleDeleteVODDownload)
	mux.HandleFunc("DELETE /api/vods/{id}", a.handleRemoveVOD)
	mux.HandleFunc("GET /api/vods.m3u", a.handleGetVODM3U)
	mux.HandleFunc("GET /api/streams", a.handleActiveStreams)
	mux.HandleFunc("POST /api/stream/{channel}", a.handleStartStream)
	mux.HandleFunc("POST /api/channels/add", a.handleAddChannel)
	mux.HandleFunc("POST /api/channels/remove", a.handleRemoveChannelByBody)
	mux.HandleFunc("DELETE /api/channels/remove/{name}", a.handleRemoveChannelByPath)
	mux.HandleFunc("POST /api/stop/{channel}", a.handleStopChannel)
	mux.HandleFunc("DELETE /api/stop/{channel}", a.handleStopChannel)
	mux.HandleFunc("GET /api/stream-ready/{channel}", a.handleStreamReady)
	mux.HandleFunc("GET /api/status", a.handleGetChannelStatus)
	mux.HandleFunc("GET /api/twitch.m3u", a.handleGetM3U)
	mux.HandleFunc("POST /api/status", a.handleSetChannelStatus)
	mux.HandleFunc("POST /api/playing/{channel}", a.handleRecordPlaying)
	mux.HandleFunc("GET /api/playing", a.handleGetPlayingCounts)
	mux.HandleFunc("POST /api/jellyfin/webhook", a.handleJellyfinWebhook)
	mux.HandleFunc("GET /vod/{id}/index.m3u8", a.handleGetVODPlaylist)

	return mux
}
