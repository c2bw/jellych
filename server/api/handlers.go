package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/c2bw/jellych/stream"
)

const maxJSONRequestBytes = 1 << 20
const maxVODStatusWorkers = 4

func (a *API) requireControlAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		expected := a.state.controlAPISecret()
		if expected == "" {
			next(w, r)
			return
		}
		provided := strings.TrimSpace(r.Header.Get("X-Jellych-API-Secret"))
		if authorization := strings.TrimSpace(r.Header.Get("Authorization")); strings.HasPrefix(authorization, "Bearer ") {
			provided = strings.TrimSpace(strings.TrimPrefix(authorization, "Bearer "))
		}
		if subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="jellych-control"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func decodeJSONBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONRequestBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return false
		}
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return false
		}
		http.Error(w, "request body must contain one JSON value", http.StatusBadRequest)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		slog.Error("failed to encode JSON response", "error", err)
	}
}

func writeText(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(msg))
}

func writeErrorf(w http.ResponseWriter, status int, format string, err error) {
	http.Error(w, fmt.Sprintf(format, err), status)
}

type handlerError struct {
	err     error
	status  int
	message string
}

func writeMappedError(w http.ResponseWriter, err error, mappings []handlerError, fallbackStatus int, fallbackFormat string) {
	for _, mapping := range mappings {
		if errors.Is(err, mapping.err) {
			http.Error(w, mapping.message, mapping.status)
			return
		}
	}
	writeErrorf(w, fallbackStatus, fallbackFormat, err)
}

var vodDownloadStartErrors = []handlerError{
	{err: stream.ErrVODDownloadsDisabled, status: http.StatusServiceUnavailable, message: "vod downloads folder is not configured; start jellych with --vods <folder>"},
	{err: stream.ErrVODDownloadAlreadyStarted, status: http.StatusConflict, message: "vod download already started"},
	{err: stream.ErrVODDownloadAlreadyExists, status: http.StatusConflict, message: "vod already downloaded"},
	{err: stream.ErrVODRemovalInProgress, status: http.StatusConflict, message: "vod removal in progress"},
	{err: stream.ErrVODManifestRestricted, status: http.StatusForbidden, message: "vod is subscriber-only or otherwise restricted by Twitch"},
	{err: context.DeadlineExceeded, status: http.StatusGatewayTimeout, message: "timed out resolving vod stream URL"},
}

var vodDownloadProgressErrors = []handlerError{
	{err: stream.ErrVODDownloadsDisabled, status: http.StatusServiceUnavailable, message: "vod downloads folder is not configured; start jellych with --vods <folder>"},
}

var vodDownloadDeleteErrors = []handlerError{
	{err: stream.ErrVODDownloadsDisabled, status: http.StatusServiceUnavailable, message: "vod downloads folder is not configured; start jellych with --vods <folder>"},
	{err: stream.ErrVODDownloadAlreadyStarted, status: http.StatusConflict, message: "vod download is still running"},
	{err: stream.ErrVODDownloadNotFound, status: http.StatusNotFound, message: "downloaded vod not found"},
}

var vodConversionErrors = []handlerError{
	{err: stream.ErrVODDownloadsDisabled, status: http.StatusServiceUnavailable, message: "vod downloads folder is not configured; start jellych with --vods <folder>"},
	{err: stream.ErrVODDownloadAlreadyStarted, status: http.StatusConflict, message: "vod download or conversion already started"},
	{err: stream.ErrVODDownloadNotFound, status: http.StatusNotFound, message: "downloaded vod not found"},
	{err: stream.ErrVODConversionRequiresOriginal, status: http.StatusConflict, message: "only original downloads can be converted"},
	{err: stream.ErrVODConversionTargetOriginal, status: http.StatusBadRequest, message: "conversion target must be h264, hevc, or vp9"},
	{err: stream.ErrVODRemovalInProgress, status: http.StatusConflict, message: "vod removal in progress"},
}

var addVODErrors = []handlerError{
	{err: ErrVODAlreadyExists, status: http.StatusConflict, message: "vod already exists"},
}

var removeVODErrors = []handlerError{
	{err: ErrVODNotFound, status: http.StatusNotFound, message: "vod not found"},
	{err: stream.ErrVODRemovalInProgress, status: http.StatusConflict, message: "vod removal already in progress"},
}

var startStreamErrors = []handlerError{
	{err: stream.ErrAlreadyStarted, status: http.StatusConflict, message: "stream already started"},
}

var addChannelErrors = []handlerError{
	{err: ErrChannelAlreadyExists, status: http.StatusConflict, message: "channel already exists"},
}

var removeChannelErrors = []handlerError{
	{err: ErrChannelNotFound, status: http.StatusNotFound, message: "channel not found"},
}

var vodPlaylistErrors = []handlerError{
	{err: stream.ErrVODManifestRestricted, status: http.StatusForbidden, message: "vod is subscriber-only or otherwise restricted by Twitch"},
}

func (a *API) handlePing(w http.ResponseWriter, r *http.Request) {
	slog.Debug("ping request received", "method", r.Method, "path", r.URL.Path)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	writeText(w, http.StatusOK, "pong")
}

func requireValidChannelName(w http.ResponseWriter, name string) (string, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return "", false
	}
	if err := stream.ValidateChannelName(name); err != nil {
		http.Error(w, "invalid channel name", http.StatusBadRequest)
		return "", false
	}
	return name, true
}

func (a *API) handleChannels(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, a.state.GetChannels())
}

func (a *API) handleVODs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, a.buildVODResponses(a.state.GetVODs()))
}

type vodResponse struct {
	VOD
	Downloaded           bool   `json:"downloaded"`
	DownloadActive       bool   `json:"downloadActive"`
	DownloadSize         int64  `json:"downloadSize"`
	DownloadRate         int64  `json:"downloadRate,omitempty"`
	DownloadSpeed        string `json:"downloadSpeed,omitempty"`
	DownloadPreset       string `json:"downloadPreset,omitempty"`
	DownloadOperation    string `json:"downloadOperation,omitempty"`
	OriginalSize         int64  `json:"originalSize,omitempty"`
	DownloadETASeconds   int64  `json:"downloadETASeconds,omitempty"`
	DownloadVideoCodec   string `json:"downloadVideoCodec,omitempty"`
	DownloadVideoWidth   int    `json:"downloadVideoWidth,omitempty"`
	DownloadVideoHeight  int    `json:"downloadVideoHeight,omitempty"`
	DownloadTotalBitrate int64  `json:"downloadTotalBitrate,omitempty"`
	EstimatedDeletionAt  string `json:"estimatedDeletionAt,omitempty"`
}

func (a *API) buildVODResponses(vods []VOD) []vodResponse {
	out := make([]vodResponse, 0, len(vods))
	if len(vods) == 0 {
		return out
	}
	out = append(out, make([]vodResponse, len(vods))...)
	jobs := make(chan int)
	workerCount := min(maxVODStatusWorkers, len(vods))
	var workers sync.WaitGroup
	workers.Add(workerCount)
	for range workerCount {
		go func() {
			defer workers.Done()
			for i := range jobs {
				out[i] = a.buildVODResponse(vods[i])
			}
		}()
	}
	for i := range vods {
		jobs <- i
	}
	close(jobs)
	workers.Wait()
	return out
}

func (a *API) buildVODResponse(vod VOD) vodResponse {
	progress, err := a.streams.GetVODDownloadProgress(vod.ID)
	if err != nil && !errors.Is(err, stream.ErrVODDownloadsDisabled) {
		slog.Warn("failed to check vod download status", "id", vod.ID, "error", err)
	}
	downloaded := progress.Downloaded
	downloadActive := progress.Active
	var estimatedDeletionAt string
	if downloaded {
		_, deletionAt, err := a.streams.VODDownloadStatus(vod.ID)
		if err != nil && !errors.Is(err, stream.ErrVODDownloadsDisabled) {
			slog.Warn("failed to check vod download retention status", "id", vod.ID, "error", err)
		}
		if !deletionAt.IsZero() {
			estimatedDeletionAt = deletionAt.Format(time.RFC3339)
		}
	}
	return vodResponse{
		VOD:                  vod,
		Downloaded:           downloaded,
		DownloadActive:       downloadActive,
		DownloadSize:         progress.TotalSize,
		DownloadRate:         progress.BytesPerSecond,
		DownloadSpeed:        progress.Speed,
		DownloadPreset:       progress.Preset,
		DownloadOperation:    progress.Operation,
		OriginalSize:         progress.OriginalSize,
		DownloadETASeconds:   progress.ETASeconds,
		DownloadVideoCodec:   progress.VideoCodec,
		DownloadVideoWidth:   progress.VideoWidth,
		DownloadVideoHeight:  progress.VideoHeight,
		DownloadTotalBitrate: progress.TotalBitrate,
		EstimatedDeletionAt:  estimatedDeletionAt,
	}
}

func (a *API) handleAddVOD(w http.ResponseWriter, r *http.Request) {
	var vod VOD
	if !decodeJSONBody(w, r, &vod) {
		return
	}
	vod = PrepareVOD(vod)
	if err := a.state.AddVOD(vod); err != nil {
		writeMappedError(w, err, addVODErrors, http.StatusBadRequest, "failed to add vod: %v")
		return
	}

	writeJSON(w, vod)
}

func (a *API) handleRemoveVOD(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if _, ok := a.state.FindVOD(id); !ok {
		http.Error(w, "vod not found", http.StatusNotFound)
		return
	}
	if err := a.streams.RemoveVODWithArtifacts(id, func() error {
		return a.state.RemoveVOD(id)
	}); err != nil {
		writeMappedError(w, err, removeVODErrors, http.StatusInternalServerError, "failed to remove vod: %v")
		return
	}

	writeText(w, http.StatusOK, "removed")
}

func (a *API) handleDownloadVOD(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	vod, ok := a.state.FindVOD(id)
	if !ok {
		http.Error(w, "vod not found", http.StatusNotFound)
		return
	}

	type downloadRequest struct {
		Preset string `json:"preset"`
	}
	payload := &downloadRequest{}
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONRequestBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		if !errors.Is(err, io.EOF) {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		payload = &downloadRequest{}
	} else if payload == nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "request body must contain one JSON value", http.StatusBadRequest)
		return
	}
	preset, err := stream.ParseVODDownloadPreset(payload.Preset)
	if err != nil {
		http.Error(w, "invalid download preset", http.StatusBadRequest)
		return
	}

	totalDuration, err := time.ParseDuration(strings.TrimSpace(vod.Duration))
	if err != nil {
		totalDuration = 0
	}
	if err := a.streams.StartVODDownload(r.Context(), vod.ID, vod.URL, vod.Title, vod.Channel, preset, totalDuration); err != nil {
		writeMappedError(w, err, vodDownloadStartErrors, http.StatusInternalServerError, "failed to start vod download: %v")
		return
	}

	writeText(w, http.StatusAccepted, "download started")
}

func (a *API) handleGetVODDownload(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if _, ok := a.state.FindVOD(id); !ok {
		http.Error(w, "vod not found", http.StatusNotFound)
		return
	}

	progress, err := a.streams.GetVODDownloadProgress(id)
	if err != nil {
		writeMappedError(w, err, vodDownloadProgressErrors, http.StatusInternalServerError, "failed to get vod download progress: %v")
		return
	}
	writeJSON(w, progress)
}

func (a *API) handleConvertVOD(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if _, ok := a.state.FindVOD(id); !ok {
		http.Error(w, "vod not found", http.StatusNotFound)
		return
	}
	var payload struct {
		Preset string `json:"preset"`
	}
	if !decodeJSONBody(w, r, &payload) {
		return
	}
	preset, err := stream.ParseVODDownloadPreset(payload.Preset)
	if err != nil {
		http.Error(w, "invalid conversion preset", http.StatusBadRequest)
		return
	}
	if err := a.streams.ConvertVODDownload(r.Context(), id, preset); err != nil {
		writeMappedError(w, err, vodConversionErrors, http.StatusInternalServerError, "failed to start vod conversion: %v")
		return
	}
	writeText(w, http.StatusAccepted, "conversion started")
}

func (a *API) handleDeleteVODDownload(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if err := a.streams.DeleteVODDownload(id); err != nil {
		writeMappedError(w, err, vodDownloadDeleteErrors, http.StatusInternalServerError, "failed to delete vod download: %v")
		return
	}

	writeText(w, http.StatusOK, "download deleted")
}

func (a *API) handleStartStream(w http.ResponseWriter, r *http.Request) {
	channel, ok := requireValidChannelName(w, r.PathValue("channel"))
	if !ok {
		return
	}
	if !a.state.IsConfiguredChannel(channel) {
		http.Error(w, "channel not configured", http.StatusNotFound)
		return
	}

	if err := a.streams.Start(channel); err != nil {
		writeMappedError(w, err, startStreamErrors, http.StatusInternalServerError, "failed to start: %v")
		return
	}

	writeText(w, http.StatusOK, "started")
}

func (a *API) handleAddChannel(w http.ResponseWriter, r *http.Request) {
	var c Channel
	if !decodeJSONBody(w, r, &c) {
		return
	}
	name, ok := requireValidChannelName(w, c.Name)
	if !ok {
		return
	}
	if err := a.state.AddChannel(name); err != nil {
		writeMappedError(w, err, addChannelErrors, http.StatusInternalServerError, "failed to add channel: %v")
		return
	}

	writeText(w, http.StatusCreated, "added")
}

func (a *API) handleRemoveChannelByBody(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Name string `json:"name"`
	}
	if !decodeJSONBody(w, r, &payload) {
		return
	}
	if err := a.removeChannelByName(w, payload.Name); err != nil {
		return
	}

	writeText(w, http.StatusOK, "removed")
}

func (a *API) handleRemoveChannelByPath(w http.ResponseWriter, r *http.Request) {
	if err := a.removeChannelByName(w, r.PathValue("name")); err != nil {
		return
	}

	writeText(w, http.StatusOK, "removed")
}

func (a *API) removeChannelByName(w http.ResponseWriter, name string) error {
	validName, ok := requireValidChannelName(w, name)
	if !ok {
		return fmt.Errorf("invalid name")
	}
	if err := a.state.RemoveChannel(validName); err != nil {
		writeMappedError(w, err, removeChannelErrors, http.StatusInternalServerError, "failed to remove channel: %v")
		return err
	}
	return nil
}

func (a *API) handleStopChannel(w http.ResponseWriter, r *http.Request) {
	channel, ok := requireValidChannelName(w, r.PathValue("channel"))
	if !ok {
		return
	}

	if err := a.streams.StopChannel(channel); err != nil {
		if isIdempotentStopError(err) {
			slog.Warn("stop channel: treating shutdown race as success", "channel", channel, "error", err)
		} else {
			writeErrorf(w, http.StatusInternalServerError, "failed to stop: %v", err)
			return
		}
	}

	writeText(w, http.StatusOK, "stopped")
}

func isIdempotentStopError(err error) bool {
	return errors.Is(err, stream.ErrStopTimeout) || errors.Is(err, os.ErrProcessDone)
}

func (a *API) handleActiveStreams(w http.ResponseWriter, r *http.Request) {
	active := a.streams.ActiveChannels()
	writeJSON(w, active)
}

func (a *API) handleGetChannelStatus(w http.ResponseWriter, r *http.Request) {
	statuses := a.state.GetChannelStatus()
	// Log playing counts for channels that have jellych viewers > 0
	counts := a.playback.GetPlayingCounts(a.now())
	for _, s := range statuses {
		if s.Online && counts[s.Name] > 0 {
			slog.Debug("playing streams", "channel", s.Name, "viewers", s.Viewers, "playing", counts[s.Name])
		}
	}
	writeJSON(w, statuses)
}

func (a *API) handleSetChannelStatus(w http.ResponseWriter, r *http.Request) {
	var statuses []Status
	if !decodeJSONBody(w, r, &statuses) {
		return
	}
	a.state.SetChannelStatus(statuses)
	writeText(w, http.StatusOK, "status updated")
}

func (a *API) handleRecordPlaying(w http.ResponseWriter, r *http.Request) {
	channel, ok := requireValidChannelName(w, r.PathValue("channel"))
	if !ok {
		return
	}
	if !a.state.IsConfiguredChannel(channel) {
		http.Error(w, "channel not configured", http.StatusNotFound)
		return
	}
	var payload struct {
		SessionID string `json:"sessionId"`
		Action    string `json:"action"`
	}
	if !decodeJSONBody(w, r, &payload) {
		return
	}
	payload.SessionID = strings.TrimSpace(payload.SessionID)
	if payload.SessionID == "" || len(payload.SessionID) > maxPlaybackSessionIDBytes {
		http.Error(w, "sessionId required", http.StatusBadRequest)
		return
	}

	switch strings.ToLower(strings.TrimSpace(payload.Action)) {
	case "stop":
		a.playback.StopPlaying(channel, payload.SessionID)
	case "start", "ping":
		if !a.playback.RecordPlaying(channel, payload.SessionID, a.now()) {
			http.Error(w, "too many playback sessions", http.StatusTooManyRequests)
			return
		}
	default:
		http.Error(w, "unsupported playback action", http.StatusBadRequest)
		return
	}

	writeText(w, http.StatusOK, "ok")
}

func (a *API) handleGetPlayingCounts(w http.ResponseWriter, r *http.Request) {
	counts := a.playback.GetPlayingCounts(a.now())
	writeJSON(w, counts)
}

// handleJellyfinWebhook accepts simple JSON webhook POSTs from Jellyfin or
// any external service indicating a playback start/stop for a channel.
// Expected payload (flexible): { "action": "start"|"stop", "channel": "name", "sessionId": "..." }
func (a *API) handleJellyfinWebhook(w http.ResponseWriter, r *http.Request) {
	slog.Debug("jellyfin webhook: received request")
	if !a.authorizeJellyfinWebhook(w, r) {
		slog.Warn("jellyfin webhook: authorization failed")
		return
	}

	var payload struct {
		Action    string `json:"action"`
		Channel   string `json:"channel"`
		SessionID string `json:"sessionId"`
	}
	if !decodeJSONBody(w, r, &payload) {
		slog.Warn("jellyfin webhook: failed to parse JSON")
		return
	}
	slog.Info("jellyfin webhook: payload", "action", payload.Action, "channel", payload.Channel, "sessionId", payload.SessionID)

	channel, ok := requireValidChannelName(w, payload.Channel)
	if !ok {
		slog.Warn("jellyfin webhook: invalid channel name", "channel", payload.Channel)
		return
	}
	if !a.state.IsConfiguredChannel(channel) {
		http.Error(w, "channel not configured", http.StatusNotFound)
		return
	}
	payload.SessionID = strings.TrimSpace(payload.SessionID)
	if payload.SessionID == "" || len(payload.SessionID) > maxPlaybackSessionIDBytes {
		slog.Warn("jellyfin webhook: missing sessionId")
		http.Error(w, "sessionId required", http.StatusBadRequest)
		return
	}

	act := strings.ToLower(strings.TrimSpace(payload.Action))
	switch act {
	case "stop", "stopped", "end":
		slog.Info("jellyfin webhook: playback stopped", "channel", channel, "sessionId", payload.SessionID)
		a.playback.StopPlaying(channel, payload.SessionID)
	case "start", "started", "play", "playing":
		slog.Info("jellyfin webhook: playback started", "channel", channel, "sessionId", payload.SessionID)
		// Start stream if not already running
		if err := a.streams.Start(channel); err != nil && !errors.Is(err, stream.ErrAlreadyStarted) {
			slog.Error("jellyfin webhook: failed to start stream", "channel", channel, "error", err)
			writeErrorf(w, http.StatusInternalServerError, "failed to start stream: %v", err)
			return
		}
		// Mark this session as originating from Jellyfin so it isn't
		// auto-removed when web/manager heartbeats are missing.
		if !a.playback.RecordPlaying(channel, payload.SessionID, a.now()) || !a.playback.MarkJellyfinSession(channel, payload.SessionID) {
			a.playback.StopPlaying(channel, payload.SessionID)
			http.Error(w, "too many playback sessions", http.StatusTooManyRequests)
			return
		}
	default:
		slog.Warn("jellyfin webhook: unsupported playback action", "action", payload.Action, "channel", channel)
		http.Error(w, "unsupported playback action", http.StatusBadRequest)
		return
	}

	writeText(w, http.StatusOK, "ok")
}

func (a *API) authorizeJellyfinWebhook(w http.ResponseWriter, r *http.Request) bool {
	expected := a.state.jellyfinWebhookSecret()
	if expected == "" {
		slog.Error("jellyfin webhook: secret not configured")
		http.Error(w, "jellyfin webhook secret is not configured", http.StatusServiceUnavailable)
		return false
	}
	provided := strings.TrimSpace(r.Header.Get("X-Jellych-Secret"))
	if provided == "" {
		slog.Warn("jellyfin webhook: missing X-Jellych-Secret header")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	if subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
		slog.Warn("jellyfin webhook: invalid X-Jellych-Secret header")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	return true
}

func (a *API) handleStreamReady(w http.ResponseWriter, r *http.Request) {
	channel, ok := requireValidChannelName(w, r.PathValue("channel"))
	if !ok {
		return
	}

	minSegments := 1
	if raw := strings.TrimSpace(r.URL.Query().Get("minSegments")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 {
			http.Error(w, "minSegments must be a positive integer", http.StatusBadRequest)
			return
		}
		minSegments = parsed
	}

	count, err := a.streams.PlaylistSegmentCount(channel)
	if err != nil {
		writeErrorf(w, http.StatusInternalServerError, "failed to check stream readiness: %v", err)
		return
	}

	writeJSON(w, map[string]any{
		"ready":       count >= minSegments,
		"segments":    count,
		"minSegments": minSegments,
	})
}

// handleGetM3U returns a generated M3U playlist containing all configured
// channels with metadata indicating online/offline status. The playlist
// entries point to the HLS playlists served under /live/<channel>/index.m3u8.
func (a *API) handleGetM3U(w http.ResponseWriter, r *http.Request) {
	playlist := a.state.BuildM3U(a.state.GetChannels(), a.state.GetChannelStatus(), a.state.GetChannelLogos())
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(playlist))
}

func (a *API) handleGetVODM3U(w http.ResponseWriter, r *http.Request) {
	vods := a.state.GetVODs()
	local := make(map[string]bool, len(vods))
	for _, vod := range vods {
		downloaded, _, err := a.streams.VODDownloadStatus(vod.ID)
		if err != nil && !errors.Is(err, stream.ErrVODDownloadsDisabled) {
			slog.Warn("failed to check vod playback source", "id", vod.ID, "error", err)
			continue
		}
		local[vod.ID] = downloaded
	}
	playlist := a.state.buildVODM3U(vods, local)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(playlist))
}

func (a *API) handleGetVODPlaylist(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	vod, ok := a.state.FindVOD(id)
	if !ok {
		http.NotFound(w, r)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	playlist, err := a.streams.ResolveVODPlaylist(ctx, vod.URL)
	if err != nil {
		if !errors.Is(err, stream.ErrVODManifestRestricted) {
			slog.Warn("failed to resolve vod playlist", "id", id, "error", err)
		} else {
			slog.Warn("vod playlist is restricted by Twitch", "id", id, "error", err)
		}
		writeMappedError(w, err, vodPlaylistErrors, http.StatusBadGateway, "failed to resolve vod playlist: %v")
		return
	}
	playlist, err = a.rewriteVODPlaylist(id, playlist)
	if err != nil {
		slog.Warn("failed to prepare proxied vod playlist", "id", id, "error", err)
		http.Error(w, "failed to prepare vod playlist", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "private, max-age=30")
	if r.Method == http.MethodHead {
		w.Header().Set("Content-Length", strconv.Itoa(len(playlist)))
		return
	}
	_, _ = w.Write(playlist)
}

func (a *API) handleGetLocalVOD(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if _, ok := a.state.FindVOD(id); !ok {
		http.NotFound(w, r)
		return
	}

	localFile, err := a.streams.OpenVODDownload(id)
	if errors.Is(err, stream.ErrVODDownloadsDisabled) || errors.Is(err, stream.ErrVODDownloadNotFound) {
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, "/vod/"+url.PathEscape(id)+"/index.m3u8", http.StatusTemporaryRedirect)
		return
	}
	if err != nil {
		slog.Error("failed to open local vod for playback", "id", id, "error", err)
		http.Error(w, "failed to open local vod", http.StatusInternalServerError)
		return
	}
	defer localFile.Close()
	info, err := localFile.Stat()
	if err != nil {
		slog.Error("failed to inspect local vod for playback", "id", id, "error", err)
		http.Error(w, "failed to open local vod", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "video/x-matroska")
	w.Header().Set("Content-Disposition", "inline")
	http.ServeContent(w, r, id+".mkv", info.ModTime(), localFile)
}
