package api

import "net/http"

// Handler returns an http.Handler that exposes API endpoints for controlling streaming.
func Handler() http.Handler {
	startIdleMonitor()
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/ping", handlePing)
	mux.HandleFunc("GET /api/ping/", handlePing)
	mux.HandleFunc("GET /api/channels", handleChannels)
	mux.HandleFunc("GET /api/vods", handleVODs)
	mux.HandleFunc("POST /api/vods", handleAddVOD)
	mux.HandleFunc("GET /api/vods/{id}/download", handleGetVODDownload)
	mux.HandleFunc("POST /api/vods/{id}/download", handleDownloadVOD)
	mux.HandleFunc("DELETE /api/vods/{id}/download", handleDeleteVODDownload)
	mux.HandleFunc("DELETE /api/vods/{id}", handleRemoveVOD)
	mux.HandleFunc("GET /api/vods.m3u", handleGetVODM3U)
	mux.HandleFunc("GET /api/streams", handleActiveStreams)
	mux.HandleFunc("POST /api/stream/{channel}", handleStartStream)
	mux.HandleFunc("POST /api/channels/add", handleAddChannel)
	mux.HandleFunc("POST /api/channels/remove", handleRemoveChannelByBody)
	mux.HandleFunc("DELETE /api/channels/remove/{name}", handleRemoveChannelByPath)
	mux.HandleFunc("POST /api/stop/{channel}", handleStopChannel)
	mux.HandleFunc("DELETE /api/stop/{channel}", handleStopChannel)
	mux.HandleFunc("GET /api/stream-ready/{channel}", handleStreamReady)
	mux.HandleFunc("GET /api/status", handleGetChannelStatus)
	mux.HandleFunc("GET /api/twitch.m3u", handleGetM3U)
	mux.HandleFunc("POST /api/status", handleSetChannelStatus)
	mux.HandleFunc("POST /api/playing/{channel}", handleRecordPlaying)
	mux.HandleFunc("GET /api/playing", handleGetPlayingCounts)
	mux.HandleFunc("POST /api/jellyfin/webhook", handleJellyfinWebhook)
	mux.HandleFunc("GET /vod/{id}/index.m3u8", handleGetVODPlaylist)

	return mux
}
