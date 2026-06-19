package stream

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

var liveURLMu sync.RWMutex
var liveBaseURL string
var serverBaseURLMu sync.RWMutex
var serverBaseURL string

var liveStoreMu sync.RWMutex
var liveStore map[string]map[string][]byte
var defaultLiveStore = NewLiveStore(&liveStoreMu, &liveStore)
var defaultLiveService = &LiveService{
	store:    defaultLiveStore,
	registry: defaultStreamRegistry,
	start:    func(channel string) error { return startLiveChannel(channel) },
}

const (
	liveWritePrefix      = "/_live-write/"
	liveReadPrefix       = "/live/"
	liveWriteTokenHeader = "X-Jellych-Live-Write-Token"
	maxLiveObjectBytes   = 64 << 20
)

var liveWriteToken = newLiveWriteToken()

var (
	livePlaylistWaitTimeout = 15 * time.Second
	livePlaylistWaitPoll    = 200 * time.Millisecond
)

// SetLiveBaseURL configures the local HTTP base URL used by ffmpeg.
func SetLiveBaseURL(raw string) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimRight(raw, "/")
	liveURLMu.Lock()
	liveBaseURL = raw
	liveURLMu.Unlock()
}

// SetServerBaseURL configures the public server URL used to prefix HLS
// segment URIs when requested. This value should be set by the caller with
// the same value passed to api.SetPlaylistBaseURL (typically from SERVER_URL).
func SetServerBaseURL(raw string) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimRight(raw, "/")
	serverBaseURLMu.Lock()
	serverBaseURL = raw
	serverBaseURLMu.Unlock()
}

func getServerBaseURL() string {
	serverBaseURLMu.RLock()
	defer serverBaseURLMu.RUnlock()
	return serverBaseURL
}

func getLiveBaseURL() string {
	liveURLMu.RLock()
	defer liveURLMu.RUnlock()
	return liveBaseURL
}

func getLiveWriteToken() string {
	return liveWriteToken
}

func newLiveWriteToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("failed to generate live write token: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

type LiveStore struct {
	mu    *sync.RWMutex
	items *map[string]map[string][]byte
}

func NewLiveStore(mu *sync.RWMutex, items *map[string]map[string][]byte) *LiveStore {
	return &LiveStore{mu: mu, items: items}
}

func (s *LiveStore) storeMap() map[string]map[string][]byte {
	if *s.items == nil {
		*s.items = make(map[string]map[string][]byte)
	}
	return *s.items
}

func resetLiveChannel(channel string) {
	defaultLiveStore.ResetChannel(channel)
}

func (s *LiveStore) ResetChannel(channel string) {
	channel = normalizeLiveChannel(channel)
	s.mu.Lock()
	s.storeMap()[channel] = make(map[string][]byte)
	s.mu.Unlock()
}

func clearLiveChannel(channel string) {
	defaultLiveStore.ClearChannel(channel)
}

func (s *LiveStore) ClearChannel(channel string) {
	channel = normalizeLiveChannel(channel)
	s.mu.Lock()
	if items := *s.items; items != nil {
		delete(items, channel)
	}
	s.mu.Unlock()
}

func storeLiveObject(channel, name string, data []byte) {
	defaultLiveStore.StoreObject(channel, name, data)
}

func (s *LiveStore) StoreObject(channel, name string, data []byte) {
	channel = normalizeLiveChannel(channel)
	name = normalizeLiveObjectName(name)
	if channel == "" || name == "" {
		return
	}

	s.mu.Lock()
	items := s.storeMap()
	if items[channel] == nil {
		items[channel] = make(map[string][]byte)
	}
	items[channel][name] = cloneBytes(data)
	s.mu.Unlock()
}

func getLiveObject(channel, name string) []byte {
	return defaultLiveStore.GetObject(channel, name)
}

func (s *LiveStore) GetObject(channel, name string) []byte {
	channel = normalizeLiveChannel(channel)
	name = normalizeLiveObjectName(name)
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := *s.items
	if items == nil {
		return nil
	}
	channelStore := items[channel]
	if channelStore == nil {
		return nil
	}
	data, ok := channelStore[name]
	if !ok {
		return nil
	}
	return cloneBytes(data)
}

func deleteLiveObject(channel, name string) {
	defaultLiveStore.DeleteObject(channel, name)
}

func (s *LiveStore) DeleteObject(channel, name string) {
	channel = normalizeLiveChannel(channel)
	name = normalizeLiveObjectName(name)
	s.mu.Lock()
	defer s.mu.Unlock()
	items := *s.items
	if items == nil {
		return
	}
	if channelStore := items[channel]; channelStore != nil {
		delete(channelStore, name)
	}
}

type LiveService struct {
	store    *LiveStore
	registry *StreamRegistry
	start    func(string) error
}

// LiveHandler serves in-memory HLS playlists and segments.
func LiveHandler() http.Handler {
	return defaultLiveService.LiveHandler()
}

func (s *LiveService) LiveHandler() http.Handler {
	return http.HandlerFunc(s.handleLive)
}

// LiveWriteHandler accepts internal ffmpeg HLS writes.
func LiveWriteHandler() http.Handler {
	return defaultLiveService.LiveWriteHandler()
}

func (s *LiveService) LiveWriteHandler() http.Handler {
	return http.HandlerFunc(s.handleLiveWrite)
}

func (s *LiveService) handleLive(w http.ResponseWriter, r *http.Request) {
	channel, objectName, ok := parseLivePath(r.URL.Path, liveReadPrefix)
	if !ok {
		http.NotFound(w, r)
		return
	}

	switch r.Method {
	case http.MethodGet, http.MethodHead:
		data := s.store.GetObject(channel, objectName)
		if data == nil && isLivePlaylist(objectName) {
			if !s.registry.IsChannelActive(channel) {
				if err := s.start(channel); err != nil && !errors.Is(err, ErrAlreadyStarted) {
					slog.Warn("failed to auto-start live stream for playlist request", "channel", channel, "error", err)
				}
			}
			if s.registry.IsChannelActive(channel) {
				data = s.waitForObject(r.Context(), channel, objectName, livePlaylistWaitTimeout, livePlaylistWaitPoll)
			}
		}
		if data == nil {
			if isLivePlaylist(objectName) && s.registry.IsChannelActive(channel) {
				w.Header().Set("Retry-After", "1")
				http.Error(w, "playlist not ready", http.StatusServiceUnavailable)
				return
			}
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", liveContentType(objectName))
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		if r.Method == http.MethodGet {
			_, _ = w.Write(data)
		}
	default:
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func isLivePlaylist(objectName string) bool {
	return strings.EqualFold(objectName, "index.m3u8")
}

func (s *LiveService) waitForObject(ctx context.Context, channel, objectName string, timeout, pollInterval time.Duration) []byte {
	if timeout <= 0 {
		return s.store.GetObject(channel, objectName)
	}
	if pollInterval <= 0 {
		pollInterval = 100 * time.Millisecond
	}

	deadline := time.Now().Add(timeout)
	for {
		if data := s.store.GetObject(channel, objectName); data != nil {
			return data
		}
		if time.Now().After(deadline) {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(pollInterval):
		}
	}
}

func (s *LiveService) handleLiveWrite(w http.ResponseWriter, r *http.Request) {
	if !authorizeLiveWrite(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	channel, objectName, ok := parseLivePath(r.URL.Path, liveWritePrefix)
	if !ok {
		http.NotFound(w, r)
		return
	}

	switch r.Method {
	case http.MethodPut:
		r.Body = http.MaxBytesReader(w, r.Body, maxLiveObjectBytes)
		data, err := io.ReadAll(r.Body)
		if err != nil {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				http.Error(w, "live object too large", http.StatusRequestEntityTooLarge)
				return
			}
			slog.Warn("failed to read live object", "error", err, "remote", r.RemoteAddr)
			http.Error(w, "failed to read live object", http.StatusBadRequest)
			return
		}
		s.store.StoreObject(channel, objectName, data)
		w.WriteHeader(http.StatusNoContent)
	case http.MethodDelete:
		s.store.DeleteObject(channel, objectName)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "PUT, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func authorizeLiveWrite(r *http.Request) bool {
	provided := strings.TrimSpace(r.Header.Get(liveWriteTokenHeader))
	expected := getLiveWriteToken()
	if provided == "" || expected == "" {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
		slog.Warn("invalid live write token", "remote", r.RemoteAddr)
		return false
	}
	return true
}

func parseLivePath(rawPath, prefix string) (string, string, bool) {
	path := strings.TrimPrefix(rawPath, prefix)
	if path == rawPath || path == "" {
		return "", "", false
	}
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	channel := normalizeLiveChannel(parts[0])
	objectName := normalizeLiveObjectName(parts[1])
	if err := ValidateChannelName(channel); err != nil || objectName == "" {
		return "", "", false
	}
	return channel, objectName, true
}

func normalizeLiveChannel(channel string) string {
	return strings.ToLower(strings.TrimSpace(channel))
}

func normalizeLiveObjectName(name string) string {
	return strings.TrimSpace(name)
}

func cloneBytes(data []byte) []byte {
	if data == nil {
		return nil
	}
	out := make([]byte, len(data))
	copy(out, data)
	return out
}

func liveContentType(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".m3u8":
		return "application/vnd.apple.mpegurl"
	case ".ts":
		return "video/mp2t"
	default:
		return "application/octet-stream"
	}
}
