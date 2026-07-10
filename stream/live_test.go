package stream

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLiveHandlerIsReadOnly(t *testing.T) {
	resetLiveChannel("testchannel")
	t.Cleanup(func() { clearLiveChannel("testchannel") })

	req := httptest.NewRequest(http.MethodPut, "/live/testchannel/index.m3u8", strings.NewReader("#EXTM3U\n"))
	rec := httptest.NewRecorder()

	LiveHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status %d, got %d", http.StatusMethodNotAllowed, rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != "GET, HEAD" {
		t.Fatalf("expected read-only Allow header, got %q", got)
	}
}

func TestLiveWriteHandlerRequiresToken(t *testing.T) {
	req := httptest.NewRequest(http.MethodPut, "/_live-write/testchannel/index.m3u8", strings.NewReader("#EXTM3U\n"))
	rec := httptest.NewRecorder()

	LiveWriteHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestLiveWriteHandlerStoresReadableObject(t *testing.T) {
	resetLiveChannel("testchannel")
	t.Cleanup(func() { clearLiveChannel("testchannel") })

	body := "#EXTM3U\n#EXTINF:2,\nsegment0.ts\n"
	writeReq := httptest.NewRequest(http.MethodPut, "/_live-write/testchannel/index.m3u8", strings.NewReader(body))
	writeReq.Header.Set(liveWriteTokenHeader, getLiveWriteToken())
	writeRec := httptest.NewRecorder()

	LiveWriteHandler().ServeHTTP(writeRec, writeReq)

	if writeRec.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d", http.StatusNoContent, writeRec.Code)
	}

	readReq := httptest.NewRequest(http.MethodGet, "/live/testchannel/index.m3u8", nil)
	readRec := httptest.NewRecorder()

	LiveHandler().ServeHTTP(readRec, readReq)

	if readRec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, readRec.Code)
	}
	if got := readRec.Body.String(); got != body {
		t.Fatalf("expected body %q, got %q", body, got)
	}
}

func TestLiveWriteHandlerRetainsRecentlyDeletedSegment(t *testing.T) {
	resetLiveChannel("testchannel")
	t.Cleanup(func() { clearLiveChannel("testchannel") })

	writeLiveObject(t, http.MethodPut, "/_live-write/testchannel/segment0.ts", "segment data")
	writeLiveObject(t, http.MethodDelete, "/_live-write/testchannel/segment0.ts", "")

	req := httptest.NewRequest(http.MethodGet, "/live/testchannel/segment0.ts", nil)
	rec := httptest.NewRecorder()
	LiveHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d during deletion grace period, got %d", http.StatusOK, rec.Code)
	}
	if got, want := rec.Body.String(), "segment data"; got != want {
		t.Fatalf("expected body %q, got %q", want, got)
	}
}

func TestLiveStoreExpiresDeletedSegment(t *testing.T) {
	var mu sync.RWMutex
	var items map[string]map[string][]byte
	store := NewLiveStore(&mu, &items)
	store.deleteGrace = 10 * time.Second
	now := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }

	store.StoreObject("testchannel", "segment0.ts", []byte("segment data"))
	store.DeleteObject("testchannel", "segment0.ts")
	if got := store.GetObject("testchannel", "segment0.ts"); string(got) != "segment data" {
		t.Fatalf("expected segment during grace period, got %q", got)
	}

	now = now.Add(store.deleteGrace)
	if got := store.GetObject("testchannel", "segment0.ts"); got != nil {
		t.Fatalf("expected expired segment to be unavailable, got %q", got)
	}
}

func TestLiveHandlerPreventsCachingMissingSegments(t *testing.T) {
	clearLiveChannel("testchannel")

	req := httptest.NewRequest(http.MethodGet, "/live/testchannel/missing.ts", nil)
	rec := httptest.NewRecorder()
	LiveHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d", http.StatusNotFound, rec.Code)
	}
	if got, want := rec.Header().Get("Cache-Control"), "no-store"; got != want {
		t.Fatalf("expected Cache-Control %q, got %q", want, got)
	}
}

func TestLiveHandlerWaitsForActiveSegment(t *testing.T) {
	var storeMu sync.RWMutex
	var items map[string]map[string][]byte
	store := NewLiveStore(&storeMu, &items)

	var registryMu sync.Mutex
	managers := map[string]*manager{
		"testchannel": {started: true},
	}
	service := &LiveService{
		store:    store,
		registry: NewStreamRegistry(&registryMu, &managers),
		start:    func(string) error { return nil },
	}

	stored := make(chan struct{})
	go func() {
		time.Sleep(20 * time.Millisecond)
		store.StoreObject("testchannel", "segment0.ts", []byte("segment data"))
		close(stored)
	}()

	req := httptest.NewRequest(http.MethodGet, "/live/testchannel/segment0.ts", nil)
	rec := httptest.NewRecorder()
	service.LiveHandler().ServeHTTP(rec, req)
	<-stored

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d for delayed segment, got %d", http.StatusOK, rec.Code)
	}
	if got, want := rec.Body.String(), "segment data"; got != want {
		t.Fatalf("expected body %q, got %q", want, got)
	}
}

func writeLiveObject(t *testing.T, method, path, body string) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set(liveWriteTokenHeader, getLiveWriteToken())
	rec := httptest.NewRecorder()
	LiveWriteHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status %d for %s %s, got %d", http.StatusNoContent, method, path, rec.Code)
	}
}

func TestLiveHandlerAutoStartsInactivePlaylistRequest(t *testing.T) {
	clearLiveChannel("testchannel")
	t.Cleanup(func() { clearLiveChannel("testchannel") })

	origStart := startLiveChannel
	started := false
	startLiveChannel = func(channel string) error {
		started = channel == "testchannel"
		resetLiveChannel(channel)
		storeLiveObject(channel, "index.m3u8", []byte("#EXTM3U\n"))
		mu.Lock()
		if mgrs == nil {
			mgrs = make(map[string]*manager)
		}
		mgrs[channel] = &manager{started: true}
		mu.Unlock()
		return nil
	}
	t.Cleanup(func() {
		startLiveChannel = origStart
		mu.Lock()
		delete(mgrs, "testchannel")
		mu.Unlock()
	})

	req := httptest.NewRequest(http.MethodGet, "/live/testchannel/index.m3u8", nil)
	rec := httptest.NewRecorder()

	LiveHandler().ServeHTTP(rec, req)

	if !started {
		t.Fatal("expected playlist request to auto-start channel")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	if got := rec.Body.String(); got != "#EXTM3U\n" {
		t.Fatalf("expected playlist body, got %q", got)
	}
}

func TestLiveWriteHandlerRejectsOversizedObject(t *testing.T) {
	resetLiveChannel("testchannel")
	t.Cleanup(func() { clearLiveChannel("testchannel") })

	tooLarge := bytes.Repeat([]byte("a"), maxLiveObjectBytes+1)
	req := httptest.NewRequest(http.MethodPut, "/_live-write/testchannel/segment.ts", bytes.NewReader(tooLarge))
	req.Header.Set(liveWriteTokenHeader, getLiveWriteToken())
	rec := httptest.NewRecorder()

	LiveWriteHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected status %d, got %d", http.StatusRequestEntityTooLarge, rec.Code)
	}
}

func TestLiveHandlerReturnsServiceUnavailableForActiveChannelWhenPlaylistMissing(t *testing.T) {
	resetLiveChannel("testchannel")
	t.Cleanup(func() { clearLiveChannel("testchannel") })

	origTimeout := livePlaylistWaitTimeout
	origPoll := livePlaylistWaitPoll
	livePlaylistWaitTimeout = 20 * time.Millisecond
	livePlaylistWaitPoll = 5 * time.Millisecond
	t.Cleanup(func() {
		livePlaylistWaitTimeout = origTimeout
		livePlaylistWaitPoll = origPoll
	})

	mu.Lock()
	if mgrs == nil {
		mgrs = make(map[string]*manager)
	}
	mgrs["testchannel"] = &manager{started: true}
	mu.Unlock()
	t.Cleanup(func() {
		mu.Lock()
		delete(mgrs, "testchannel")
		mu.Unlock()
	})

	req := httptest.NewRequest(http.MethodGet, "/live/testchannel/index.m3u8", nil)
	rec := httptest.NewRecorder()

	LiveHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status %d, got %d", http.StatusServiceUnavailable, rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "1" {
		t.Fatalf("expected Retry-After header %q, got %q", "1", got)
	}
}
