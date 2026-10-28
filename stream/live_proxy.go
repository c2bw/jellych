package stream

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

const liveProxyURLCacheTTL = 2 * time.Minute
const maxLiveProxyURLCacheEntries = 256
const liveProxyResolutionTimeout = 30 * time.Second

// ErrLiveStreamOffline indicates the requested channel is offline or not playable.
var ErrLiveStreamOffline = errors.New("live stream offline or unavailable")

var resolveLiveProxyUpstream = func(ctx context.Context, channel string) (string, error) {
	upstreamURL, _, err := resolveLiveStreamURL(ctx, channel)
	if err != nil {
		if isOfflineLiveStreamError(err) {
			return "", fmt.Errorf("%w: %v", ErrLiveStreamOffline, err)
		}
		return "", err
	}
	return upstreamURL, nil
}

var liveProxyHTTPClient = &http.Client{Timeout: liveProxyResolutionTimeout}

var fetchLiveProxyPlaylist = func(ctx context.Context, playlistURL string) ([]byte, error) {
	return FetchAndNormalizeHLSPlaylistWithClient(ctx, liveProxyHTTPClient, playlistURL)
}

type liveProxyURLCacheEntry struct {
	url       string
	expiresAt time.Time
}

type liveProxyURLCall struct {
	done chan struct{}
	url  string
	err  error
}

var liveProxyURLCache = struct {
	sync.Mutex
	entries  map[string]liveProxyURLCacheEntry
	inflight map[string]*liveProxyURLCall
}{}

// ResolveLiveStreamURL resolves a fresh upstream Twitch HLS media playlist URL
// for channel without fetching or rewriting the playlist body.
func ResolveLiveStreamURL(ctx context.Context, channel string) (string, error) {
	key := strings.ToLower(strings.TrimSpace(channel))
	if err := ValidateChannelName(key); err != nil {
		return "", err
	}
	return resolveLiveProxyUpstream(ctx, key)
}

// ResolveLiveProxyPlaylist resolves the upstream live HLS media playlist for
// channel and returns the normalized playlist with absolute segment URLs.
// The resolved upstream weaver URL is cached across polls while each call
// fetches the latest live sliding-window playlist.
func ResolveLiveProxyPlaylist(ctx context.Context, channel string) ([]byte, error) {
	key := strings.ToLower(strings.TrimSpace(channel))
	if err := ValidateChannelName(key); err != nil {
		return nil, err
	}

	if cachedURL, ok := getCachedLiveProxyURL(key); ok {
		data, err := fetchLiveProxyPlaylist(ctx, cachedURL)
		if err == nil {
			return data, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		evictCachedLiveProxyURL(key, cachedURL)
	}

	upstreamURL, err := resolveCachedLiveProxyURL(ctx, key)
	if err != nil {
		return nil, err
	}
	data, err := fetchLiveProxyPlaylist(ctx, upstreamURL)
	if err != nil {
		evictCachedLiveProxyURL(key, upstreamURL)
		return nil, err
	}
	return data, nil
}

func getCachedLiveProxyURL(channel string) (string, bool) {
	now := time.Now()
	liveProxyURLCache.Lock()
	defer liveProxyURLCache.Unlock()
	entry, ok := liveProxyURLCache.entries[channel]
	if !ok {
		return "", false
	}
	if !now.Before(entry.expiresAt) {
		delete(liveProxyURLCache.entries, channel)
		return "", false
	}
	entry.expiresAt = now.Add(liveProxyURLCacheTTL)
	liveProxyURLCache.entries[channel] = entry
	return entry.url, true
}

func evictCachedLiveProxyURL(channel, url string) {
	liveProxyURLCache.Lock()
	defer liveProxyURLCache.Unlock()
	if entry, ok := liveProxyURLCache.entries[channel]; ok && entry.url == url {
		delete(liveProxyURLCache.entries, channel)
	}
}

func resolveCachedLiveProxyURL(ctx context.Context, channel string) (string, error) {
	now := time.Now()
	liveProxyURLCache.Lock()
	if entry, ok := liveProxyURLCache.entries[channel]; ok {
		if now.Before(entry.expiresAt) {
			entry.expiresAt = now.Add(liveProxyURLCacheTTL)
			liveProxyURLCache.entries[channel] = entry
			liveProxyURLCache.Unlock()
			return entry.url, nil
		}
		delete(liveProxyURLCache.entries, channel)
	}
	if call := liveProxyURLCache.inflight[channel]; call != nil {
		liveProxyURLCache.Unlock()
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-call.done:
			return call.url, call.err
		}
	}
	if liveProxyURLCache.entries == nil {
		liveProxyURLCache.entries = make(map[string]liveProxyURLCacheEntry)
	}
	if liveProxyURLCache.inflight == nil {
		liveProxyURLCache.inflight = make(map[string]*liveProxyURLCall)
	}
	call := &liveProxyURLCall{done: make(chan struct{})}
	liveProxyURLCache.inflight[channel] = call
	liveProxyURLCache.Unlock()

	go runLiveProxyURLResolution(channel, call)

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-call.done:
		return call.url, call.err
	}
}

func runLiveProxyURLResolution(channel string, call *liveProxyURLCall) {
	ctx, cancel := context.WithTimeout(context.Background(), liveProxyResolutionTimeout)
	defer cancel()
	upstreamURL, err := resolveLiveProxyUpstream(ctx, channel)

	liveProxyURLCache.Lock()
	call.url = upstreamURL
	call.err = err
	if err == nil && upstreamURL != "" {
		now := time.Now()
		for cachedChannel, entry := range liveProxyURLCache.entries {
			if now.After(entry.expiresAt) {
				delete(liveProxyURLCache.entries, cachedChannel)
			}
		}
		if len(liveProxyURLCache.entries) >= maxLiveProxyURLCacheEntries {
			for cachedChannel := range liveProxyURLCache.entries {
				delete(liveProxyURLCache.entries, cachedChannel)
				break
			}
		}
		liveProxyURLCache.entries[channel] = liveProxyURLCacheEntry{
			url:       upstreamURL,
			expiresAt: now.Add(liveProxyURLCacheTTL),
		}
	}
	delete(liveProxyURLCache.inflight, channel)
	close(call.done)
	liveProxyURLCache.Unlock()
}

func isOfflineLiveStreamError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no playback token returned") ||
		strings.Contains(msg, "stream may be offline") ||
		strings.Contains(msg, "no playable stream url returned") ||
		strings.Contains(msg, "404 not found")
}
