package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/c2bw/jellych/stream"
)

var liveProxyPlaylistErrors = []handlerError{
	{err: stream.ErrLiveStreamOffline, status: http.StatusNotFound, message: "stream is offline or unavailable"},
}

func (a *API) rewriteLiveProxyPlaylist(channel string, playlist []byte) ([]byte, error) {
	now := a.now()
	a.liveMediaRegistry.prune(now)
	return stream.RewriteHLSPlaylistURLs(playlist, func(rawURL string) (string, error) {
		trimmed := strings.TrimSpace(rawURL)
		if strings.HasPrefix(trimmed, "data:") {
			return rawURL, nil
		}
		token, err := a.liveMediaRegistry.register(channel, trimmed, now)
		if err != nil {
			return "", err
		}
		return "/api/live-proxy/" + url.PathEscape(channel) + "/media/" + token, nil
	})
}

func (a *API) handleGetLiveStreamURL(w http.ResponseWriter, r *http.Request) {
	channel, ok := requireValidChannelName(w, r.PathValue("channel"))
	if !ok {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	streamURL, err := a.streams.ResolveLiveStreamURL(ctx, channel)
	if err != nil {
		if !errors.Is(err, stream.ErrLiveStreamOffline) {
			slog.Warn("failed to resolve live stream URL", "channel", channel, "error", err)
		}
		writeMappedError(w, err, liveProxyPlaylistErrors, http.StatusBadGateway, "failed to resolve live stream URL: %v")
		return
	}

	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, map[string]string{
		"channel": channel,
		"url":     streamURL,
	})
}

func (a *API) handleGetLiveProxyPlaylist(w http.ResponseWriter, r *http.Request) {
	channel, ok := requireValidChannelName(w, r.PathValue("channel"))
	if !ok {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	playlist, err := a.streams.ResolveLiveProxyPlaylist(ctx, channel)
	if err != nil {
		if !errors.Is(err, stream.ErrLiveStreamOffline) {
			slog.Warn("failed to resolve live proxy playlist", "channel", channel, "error", err)
		}
		writeMappedError(w, err, liveProxyPlaylistErrors, http.StatusBadGateway, "failed to resolve live stream playlist: %v")
		return
	}
	playlist, err = a.rewriteLiveProxyPlaylist(channel, playlist)
	if err != nil {
		slog.Warn("failed to prepare proxied live playlist", "channel", channel, "error", err)
		http.Error(w, "failed to prepare live stream playlist", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodHead {
		w.Header().Set("Content-Length", strconv.Itoa(len(playlist)))
		return
	}
	_, _ = w.Write(playlist)
}

func (a *API) handleGetLiveProxyMedia(w http.ResponseWriter, r *http.Request) {
	channel, ok := requireValidChannelName(w, r.PathValue("channel"))
	if !ok {
		return
	}
	targetURL, ok := a.liveMediaRegistry.lookup(channel, strings.TrimSpace(r.PathValue("token")), a.now())
	if !ok {
		http.NotFound(w, r)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	upstream, err := http.NewRequestWithContext(ctx, r.Method, targetURL, nil)
	if err != nil {
		http.Error(w, "invalid upstream media request", http.StatusBadGateway)
		return
	}
	for _, header := range []string{"Range", "If-Range", "If-None-Match", "If-Modified-Since"} {
		if value := r.Header.Get(header); value != "" {
			upstream.Header.Set(header, value)
		}
	}
	resp, err := a.vodMediaHTTPClient.Do(upstream)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			slog.Warn("failed to proxy live stream media", "channel", channel, "error", err)
		}
		http.Error(w, "failed to fetch live stream media", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if isVODPlaylistResponse(targetURL, resp.Header.Get("Content-Type")) && resp.StatusCode >= 200 && resp.StatusCode < 300 && r.Method == http.MethodGet {
		playlistURL := targetURL
		if resp.Request != nil && resp.Request.URL != nil {
			playlistURL = resp.Request.URL.String()
		}
		a.proxyNestedLiveProxyPlaylist(w, channel, playlistURL, resp)
		return
	}
	if resp.ContentLength > maxProxiedVODMediaBytes {
		http.Error(w, "live stream media object too large", http.StatusBadGateway)
		return
	}
	copyVODMediaHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	if r.Method == http.MethodHead || resp.StatusCode == http.StatusNotModified {
		return
	}
	if err := copyVODMediaObject(w, resp.Body, maxProxiedVODMediaBytes); err != nil {
		if errors.Is(err, errVODMediaObjectTooLarge) {
			slog.Warn("aborting oversized live stream media response", "channel", channel, "limit", maxProxiedVODMediaBytes)
		} else if r.Context().Err() == nil {
			slog.Warn("live stream media response ended before it could be completed", "channel", channel, "error", err)
		}
		panic(http.ErrAbortHandler)
	}
}

func (a *API) proxyNestedLiveProxyPlaylist(w http.ResponseWriter, channel, targetURL string, resp *http.Response) {
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxProxiedVODPlaylistBytes+1))
	if err != nil || len(body) > maxProxiedVODPlaylistBytes {
		http.Error(w, "upstream live playlist too large", http.StatusBadGateway)
		return
	}
	normalized, err := stream.NormalizeHLSPlaylistURLs(targetURL, body)
	if err != nil {
		http.Error(w, "invalid upstream live playlist", http.StatusBadGateway)
		return
	}
	rewritten, err := a.rewriteLiveProxyPlaylist(channel, normalized)
	if err != nil {
		http.Error(w, "failed to prepare live playlist", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(rewritten)
}
