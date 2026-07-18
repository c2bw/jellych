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

const (
	liveWritePrefix           = "/_live-write/"
	liveReadPrefix            = "/live/"
	liveWriteTokenHeader      = "X-Jellych-Live-Write-Token"
	liveWriteGenerationHeader = "X-Jellych-Live-Generation"
	maxLiveObjectBytes        = 64 << 20
	maxLiveChannelBytes       = 256 << 20
	maxLiveStoreBytes         = 1 << 30
	liveSegmentDeleteGrace    = 30 * time.Second
)

var (
	livePlaylistWaitTimeout = 15 * time.Second
	livePlaylistWaitPoll    = 200 * time.Millisecond
	liveSegmentWaitTimeout  = 2 * time.Second
	liveSegmentWaitPoll     = 50 * time.Millisecond
)

func newLiveWriteToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("failed to generate live write token: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

type LiveStore struct {
	mu              *sync.RWMutex
	items           *map[string]map[string][]byte
	channelBytes    map[string]int
	totalBytes      int
	trackedBytes    bool
	deleteAfter     map[string]map[string]time.Time
	deleteGrace     time.Duration
	maxChannelBytes int
	maxTotalBytes   int
	now             func() time.Time
}

// NewLiveStore wraps caller-owned synchronized storage. Capacity checks scan
// that storage so direct caller mutations remain visible. New code should use
// NewIsolatedLiveStore when external backing-map access is not required.
func NewLiveStore(mu *sync.RWMutex, items *map[string]map[string][]byte) *LiveStore {
	return newLiveStore(mu, items, false)
}

// NewIsolatedLiveStore returns a store that exclusively owns its backing map
// and maintains constant-time capacity accounting.
func NewIsolatedLiveStore() *LiveStore {
	mu := &sync.RWMutex{}
	items := make(map[string]map[string][]byte)
	return newLiveStore(mu, &items, true)
}

func newLiveStore(mu *sync.RWMutex, items *map[string]map[string][]byte, trackedBytes bool) *LiveStore {
	return &LiveStore{
		mu:              mu,
		items:           items,
		channelBytes:    make(map[string]int),
		trackedBytes:    trackedBytes,
		deleteAfter:     make(map[string]map[string]time.Time),
		deleteGrace:     liveSegmentDeleteGrace,
		maxChannelBytes: maxLiveChannelBytes,
		maxTotalBytes:   maxLiveStoreBytes,
		now:             time.Now,
	}
}

func (s *LiveStore) storeMap() map[string]map[string][]byte {
	if *s.items == nil {
		*s.items = make(map[string]map[string][]byte)
	}
	return *s.items
}

func (s *LiveStore) ResetChannel(channel string) {
	channel = normalizeLiveChannel(channel)
	s.mu.Lock()
	s.releaseChannelBytesLocked(channel)
	s.storeMap()[channel] = make(map[string][]byte)
	delete(s.deleteAfter, channel)
	s.mu.Unlock()
}

func (s *LiveStore) ClearChannel(channel string) {
	channel = normalizeLiveChannel(channel)
	s.mu.Lock()
	s.releaseChannelBytesLocked(channel)
	if items := *s.items; items != nil {
		delete(items, channel)
	}
	delete(s.deleteAfter, channel)
	s.mu.Unlock()
}

func (s *LiveStore) StoreObject(channel, name string, data []byte) bool {
	channel = normalizeLiveChannel(channel)
	name = normalizeLiveObjectName(name)
	if channel == "" || name == "" {
		return false
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeDeletedLocked(channel, s.now())
	items := s.storeMap()
	if items[channel] == nil {
		items[channel] = make(map[string][]byte)
	}
	previousSize := len(items[channel][name])
	channelBytes, totalBytes := s.capacityAfterStoreLocked(channel, previousSize, len(data))
	if s.maxChannelBytes > 0 && channelBytes > s.maxChannelBytes {
		return false
	}
	if s.maxTotalBytes > 0 && totalBytes > s.maxTotalBytes {
		return false
	}
	items[channel][name] = cloneBytes(data)
	if s.trackedBytes {
		s.channelBytes[channel] = channelBytes
		s.totalBytes = totalBytes
	}
	if pending := s.deleteAfter[channel]; pending != nil {
		delete(pending, name)
		if len(pending) == 0 {
			delete(s.deleteAfter, channel)
		}
	}
	return true
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
	if pending := s.deleteAfter[channel]; pending != nil {
		if deadline, ok := pending[name]; ok && !s.now().Before(deadline) {
			return nil
		}
	}
	data, ok := channelStore[name]
	if !ok {
		return nil
	}
	return cloneBytes(data)
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
	channelStore := items[channel]
	if channelStore == nil {
		return
	}

	now := s.now()
	s.purgeDeletedLocked(channel, now)
	if _, ok := channelStore[name]; !ok {
		return
	}
	if isLiveSegment(name) && s.deleteGrace > 0 {
		if s.deleteAfter[channel] == nil {
			s.deleteAfter[channel] = make(map[string]time.Time)
		}
		if _, pending := s.deleteAfter[channel][name]; !pending {
			s.deleteAfter[channel][name] = now.Add(s.deleteGrace)
		}
		return
	}
	s.deleteObjectLocked(channel, name)
}

func (s *LiveStore) purgeDeletedLocked(channel string, now time.Time) {
	pending := s.deleteAfter[channel]
	if pending == nil {
		return
	}
	channelStore := (*s.items)[channel]
	for name, deadline := range pending {
		if now.Before(deadline) {
			continue
		}
		if channelStore != nil {
			s.deleteObjectLocked(channel, name)
		}
		delete(pending, name)
	}
	if len(pending) == 0 {
		delete(s.deleteAfter, channel)
	}
}

func (s *LiveStore) capacityAfterStoreLocked(channel string, previousSize, nextSize int) (int, int) {
	if s.trackedBytes {
		return s.channelBytes[channel] - previousSize + nextSize, s.totalBytes - previousSize + nextSize
	}

	channelBytes := -previousSize
	totalBytes := -previousSize
	for storedChannel, objects := range s.storeMap() {
		for _, object := range objects {
			totalBytes += len(object)
			if storedChannel == channel {
				channelBytes += len(object)
			}
		}
	}
	return channelBytes + nextSize, totalBytes + nextSize
}

func (s *LiveStore) releaseChannelBytesLocked(channel string) {
	if !s.trackedBytes {
		return
	}
	s.totalBytes -= s.channelBytes[channel]
	delete(s.channelBytes, channel)
}

func (s *LiveStore) deleteObjectLocked(channel, name string) {
	items := *s.items
	if items == nil || items[channel] == nil {
		return
	}
	data, ok := items[channel][name]
	if !ok {
		return
	}
	delete(items[channel], name)
	if !s.trackedBytes {
		return
	}
	s.channelBytes[channel] -= len(data)
	s.totalBytes -= len(data)
	if s.channelBytes[channel] == 0 {
		delete(s.channelBytes, channel)
	}
}

type LiveService struct {
	store        *LiveStore
	registry     *StreamRegistry
	start        func(string) error
	canAutoStart func(string) bool
	writeToken   string
}

func (s *LiveService) LiveHandler() http.Handler {
	return http.HandlerFunc(s.handleLive)
}

func (s *LiveService) LiveWriteHandler() http.Handler {
	return http.HandlerFunc(s.handleLiveWrite)
}

func (s *LiveService) handleLive(w http.ResponseWriter, r *http.Request) {
	// Playlists and segment names are reused while a channel runs. In
	// particular, never let a browser or reverse proxy cache a transient 404.
	w.Header().Set("Cache-Control", "no-store")

	channel, objectName, ok := parseLivePath(r.URL.Path, liveReadPrefix)
	if !ok {
		http.NotFound(w, r)
		return
	}

	switch r.Method {
	case http.MethodGet, http.MethodHead:
		data := s.store.GetObject(channel, objectName)
		if data == nil && isLivePlaylist(objectName) {
			if r.Method == http.MethodGet && s.isAutoStartAllowed(channel) && !s.registry.IsChannelActive(channel) {
				if err := s.start(channel); err != nil && !errors.Is(err, ErrAlreadyStarted) {
					slog.Warn("failed to auto-start live stream for playlist request", "channel", channel, "error", err)
				}
			}
			if s.registry.IsChannelActive(channel) {
				data = s.waitForObject(r.Context(), channel, objectName, livePlaylistWaitTimeout, livePlaylistWaitPoll)
			}
		}
		if data == nil && isLiveSegment(objectName) && s.registry.IsChannelActive(channel) {
			waitStarted := time.Now()
			data = s.waitForObject(r.Context(), channel, objectName, liveSegmentWaitTimeout, liveSegmentWaitPoll)
			if data != nil {
				slog.Info("served delayed live segment", "channel", channel, "segment", objectName, "wait", time.Since(waitStarted))
			} else if r.Context().Err() == nil {
				slog.Warn("live segment still missing after wait", "channel", channel, "segment", objectName, "wait", time.Since(waitStarted))
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

func (s *LiveService) isAutoStartAllowed(channel string) bool {
	return s.canAutoStart != nil && s.canAutoStart(channel)
}

func isLivePlaylist(objectName string) bool {
	return strings.EqualFold(objectName, "index.m3u8")
}

func isLiveSegment(objectName string) bool {
	switch strings.ToLower(filepath.Ext(objectName)) {
	case ".ts", ".m4s", ".aac", ".mp4":
		return true
	default:
		return false
	}
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
	if !s.authorizeLiveWrite(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	channel, objectName, ok := parseLivePath(r.URL.Path, liveWritePrefix)
	if !ok {
		http.NotFound(w, r)
		return
	}
	generation := strings.TrimSpace(r.Header.Get(liveWriteGenerationHeader))
	if generation == "" {
		http.Error(w, "missing live generation", http.StatusConflict)
		return
	}
	if !s.registry.isCurrentLiveWriter(channel, generation) {
		http.Error(w, "stale live writer", http.StatusConflict)
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
		stored := false
		if !s.registry.commitLiveWrite(channel, generation, func() {
			stored = s.store.StoreObject(channel, objectName, data)
		}) {
			http.Error(w, "stale live writer", http.StatusConflict)
			return
		}
		if !stored {
			http.Error(w, "live media store capacity exceeded", http.StatusInsufficientStorage)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case http.MethodDelete:
		if !s.registry.commitLiveWrite(channel, generation, func() {
			s.store.DeleteObject(channel, objectName)
		}) {
			http.Error(w, "stale live writer", http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "PUT, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *LiveService) authorizeLiveWrite(r *http.Request) bool {
	return authorizeLiveWriteToken(r, s.writeToken)
}

func authorizeLiveWriteToken(r *http.Request, expected string) bool {
	provided := strings.TrimSpace(r.Header.Get(liveWriteTokenHeader))
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
