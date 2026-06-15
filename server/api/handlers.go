package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/c2bw/jellych/stream"
)

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

func handlePing(w http.ResponseWriter, r *http.Request) {
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

func handleChannels(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, GetChannels())
}

func handleVODs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, buildVODResponses(GetVODs()))
}

type vodResponse struct {
	VOD
	Downloaded          bool   `json:"downloaded"`
	EstimatedDeletionAt string `json:"estimatedDeletionAt,omitempty"`
}

func buildVODResponses(vods []VOD) []vodResponse {
	out := make([]vodResponse, 0, len(vods))
	for _, vod := range vods {
		downloaded, deletionAt, err := stream.VODDownloadStatus(vod.ID)
		if err != nil && !errors.Is(err, stream.ErrVODDownloadsDisabled) {
			slog.Warn("failed to check vod download status", "id", vod.ID, "error", err)
		}
		var estimatedDeletionAt string
		if downloaded && !deletionAt.IsZero() {
			estimatedDeletionAt = deletionAt.Format(time.RFC3339)
		}
		out = append(out, vodResponse{
			VOD:                 vod,
			Downloaded:          downloaded,
			EstimatedDeletionAt: estimatedDeletionAt,
		})
	}
	return out
}

func handleAddVOD(w http.ResponseWriter, r *http.Request) {
	var vod VOD
	if err := json.NewDecoder(r.Body).Decode(&vod); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	vod = PrepareVOD(vod)
	if err := AddVOD(vod); err != nil {
		if errors.Is(err, ErrVODAlreadyExists) {
			http.Error(w, "vod already exists", http.StatusConflict)
			return
		}
		http.Error(w, "failed to add vod: "+err.Error(), http.StatusBadRequest)
		return
	}

	writeJSON(w, vod)
}

func handleRemoveVOD(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if err := RemoveVOD(id); err != nil {
		if errors.Is(err, ErrVODNotFound) {
			http.Error(w, "vod not found", http.StatusNotFound)
			return
		}
		writeErrorf(w, http.StatusInternalServerError, "failed to remove vod: %v", err)
		return
	}

	writeText(w, http.StatusOK, "removed")
}

func handleDownloadVOD(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	vod, ok := FindVOD(id)
	if !ok {
		http.Error(w, "vod not found", http.StatusNotFound)
		return
	}

	if err := stream.StartVODDownload(r.Context(), vod.ID, vod.URL, vod.Title, vod.Channel); err != nil {
		switch {
		case errors.Is(err, stream.ErrVODDownloadsDisabled):
			http.Error(w, "vod downloads folder is not configured; start jellych with --vods <folder>", http.StatusServiceUnavailable)
		case errors.Is(err, stream.ErrVODDownloadAlreadyStarted):
			http.Error(w, "vod download already started", http.StatusConflict)
		case errors.Is(err, stream.ErrVODDownloadAlreadyExists):
			http.Error(w, "vod already downloaded", http.StatusConflict)
		case errors.Is(err, context.DeadlineExceeded):
			http.Error(w, "timed out resolving vod stream URL", http.StatusGatewayTimeout)
		default:
			writeErrorf(w, http.StatusInternalServerError, "failed to start vod download: %v", err)
		}
		return
	}

	writeText(w, http.StatusAccepted, "download started")
}

func handleDeleteVODDownload(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if _, ok := FindVOD(id); !ok {
		http.Error(w, "vod not found", http.StatusNotFound)
		return
	}

	if err := stream.DeleteVODDownload(id); err != nil {
		switch {
		case errors.Is(err, stream.ErrVODDownloadsDisabled):
			http.Error(w, "vod downloads folder is not configured; start jellych with --vods <folder>", http.StatusServiceUnavailable)
		case errors.Is(err, stream.ErrVODDownloadAlreadyStarted):
			http.Error(w, "vod download is still running", http.StatusConflict)
		case errors.Is(err, stream.ErrVODDownloadNotFound):
			http.Error(w, "downloaded vod not found", http.StatusNotFound)
		default:
			writeErrorf(w, http.StatusInternalServerError, "failed to delete vod download: %v", err)
		}
		return
	}

	writeText(w, http.StatusOK, "download deleted")
}

func handleStartStream(w http.ResponseWriter, r *http.Request) {
	channel, ok := requireValidChannelName(w, r.PathValue("channel"))
	if !ok {
		return
	}

	if err := stream.Start(channel); err != nil {
		if errors.Is(err, stream.ErrAlreadyStarted) {
			http.Error(w, "stream already started", http.StatusConflict)
			return
		}
		writeErrorf(w, http.StatusInternalServerError, "failed to start: %v", err)
		return
	}

	writeText(w, http.StatusOK, "started")
}

func handleAddChannel(w http.ResponseWriter, r *http.Request) {
	var c Channel
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	name, ok := requireValidChannelName(w, c.Name)
	if !ok {
		return
	}
	if err := AddChannel(name); err != nil {
		if errors.Is(err, ErrChannelAlreadyExists) {
			http.Error(w, "channel already exists", http.StatusConflict)
			return
		}
		writeErrorf(w, http.StatusInternalServerError, "failed to add channel: %v", err)
		return
	}

	writeText(w, http.StatusCreated, "added")
}

func handleRemoveChannelByBody(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if err := removeChannelByName(w, payload.Name); err != nil {
		return
	}

	writeText(w, http.StatusOK, "removed")
}

func handleRemoveChannelByPath(w http.ResponseWriter, r *http.Request) {
	if err := removeChannelByName(w, r.PathValue("name")); err != nil {
		return
	}

	writeText(w, http.StatusOK, "removed")
}

func removeChannelByName(w http.ResponseWriter, name string) error {
	validName, ok := requireValidChannelName(w, name)
	if !ok {
		return fmt.Errorf("invalid name")
	}
	if err := RemoveChannel(validName); err != nil {
		if errors.Is(err, ErrChannelNotFound) {
			http.Error(w, "channel not found", http.StatusNotFound)
			return err
		}
		writeErrorf(w, http.StatusInternalServerError, "failed to remove channel: %v", err)
		return err
	}
	return nil
}

func handleStopChannel(w http.ResponseWriter, r *http.Request) {
	channel, ok := requireValidChannelName(w, r.PathValue("channel"))
	if !ok {
		return
	}

	if err := stream.StopChannel(channel); err != nil {
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

func handleActiveStreams(w http.ResponseWriter, r *http.Request) {
	active := stream.ActiveChannels()
	writeJSON(w, active)
}

func handleGetChannelStatus(w http.ResponseWriter, r *http.Request) {
	statuses := GetChannelStatus()
	// Log playing counts for channels that have jellych viewers > 0
	counts := GetPlayingCounts(time.Now())
	for _, s := range statuses {
		if s.Online && counts[s.Name] > 0 {
			slog.Debug("playing streams", "channel", s.Name, "viewers", s.Viewers, "playing", counts[s.Name])
		}
	}
	writeJSON(w, statuses)
}

func handleSetChannelStatus(w http.ResponseWriter, r *http.Request) {
	var statuses []Status
	if err := json.NewDecoder(r.Body).Decode(&statuses); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	SetChannelStatus(statuses)
	writeText(w, http.StatusOK, "status updated")
}

func handleRecordPlaying(w http.ResponseWriter, r *http.Request) {
	channel, ok := requireValidChannelName(w, r.PathValue("channel"))
	if !ok {
		return
	}
	var payload struct {
		SessionID string `json:"sessionId"`
		Action    string `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if payload.SessionID == "" {
		http.Error(w, "sessionId required", http.StatusBadRequest)
		return
	}

	switch strings.ToLower(strings.TrimSpace(payload.Action)) {
	case "stop":
		StopPlaying(channel, payload.SessionID)
	default:
		RecordPlaying(channel, payload.SessionID, time.Now())
	}

	writeText(w, http.StatusOK, "ok")
}

func handleGetPlayingCounts(w http.ResponseWriter, r *http.Request) {
	counts := GetPlayingCounts(time.Now())
	writeJSON(w, counts)
}

// handleJellyfinWebhook accepts simple JSON webhook POSTs from Jellyfin or
// any external service indicating a playback start/stop for a channel.
// Expected payload (flexible): { "action": "start"|"stop", "channel": "name", "sessionId": "..." }
func handleJellyfinWebhook(w http.ResponseWriter, r *http.Request) {
	slog.Debug("jellyfin webhook: received request")
	if !authorizeJellyfinWebhook(w, r) {
		slog.Warn("jellyfin webhook: authorization failed")
		return
	}

	var payload struct {
		Action    string `json:"action"`
		Channel   string `json:"channel"`
		SessionID string `json:"sessionId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		slog.Warn("jellyfin webhook: failed to parse JSON", "error", err)
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	slog.Info("jellyfin webhook: payload", "action", payload.Action, "channel", payload.Channel, "sessionId", payload.SessionID)

	channel, ok := requireValidChannelName(w, payload.Channel)
	if !ok {
		slog.Warn("jellyfin webhook: invalid channel name", "channel", payload.Channel)
		return
	}
	if strings.TrimSpace(payload.SessionID) == "" {
		slog.Warn("jellyfin webhook: missing sessionId")
		http.Error(w, "sessionId required", http.StatusBadRequest)
		return
	}

	act := strings.ToLower(strings.TrimSpace(payload.Action))
	switch act {
	case "stop", "stopped", "end":
		slog.Info("jellyfin webhook: playback stopped", "channel", channel, "sessionId", payload.SessionID)
		StopPlaying(channel, payload.SessionID)
	default:
		slog.Info("jellyfin webhook: playback started", "channel", channel, "sessionId", payload.SessionID)
		// Start stream if not already running
		if err := stream.Start(channel); err != nil && !errors.Is(err, stream.ErrAlreadyStarted) {
			slog.Error("jellyfin webhook: failed to start stream", "channel", channel, "error", err)
			writeErrorf(w, http.StatusInternalServerError, "failed to start stream: %v", err)
			return
		}
		// Mark this session as originating from Jellyfin so it isn't
		// auto-removed when web/manager heartbeats are missing.
		MarkJellyfinSession(channel, payload.SessionID)
		RecordPlaying(channel, payload.SessionID, time.Now())
	}

	writeText(w, http.StatusOK, "ok")
}

func authorizeJellyfinWebhook(w http.ResponseWriter, r *http.Request) bool {
	expected := jellyfinWebhookSecret()
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

func handleStreamReady(w http.ResponseWriter, r *http.Request) {
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

	count, err := stream.PlaylistSegmentCount(channel)
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
func handleGetM3U(w http.ResponseWriter, r *http.Request) {
	playlist := BuildM3U(GetChannels(), GetChannelStatus(), GetChannelLogos())
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(playlist))
}

func handleGetVODM3U(w http.ResponseWriter, r *http.Request) {
	playlist := BuildVODM3U(GetVODs())
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(playlist))
}

func handleGetVODPlaylist(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	vod, ok := FindVOD(id)
	if !ok {
		http.NotFound(w, r)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	playlist, err := stream.ResolveVODPlaylist(ctx, vod.URL)
	if err != nil {
		slog.Warn("failed to resolve vod playlist", "id", id, "error", err)
		writeErrorf(w, http.StatusBadGateway, "failed to resolve vod playlist: %v", err)
		return
	}

	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	_, _ = w.Write(playlist)
}
