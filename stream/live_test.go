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

const testLiveGeneration = "test-generation"

func TestLiveStoreEnforcesCapacity(t *testing.T) {
	store := NewIsolatedLiveStore()
	store.maxChannelBytes = 5
	store.maxTotalBytes = 8

	if !store.StoreObject("one", "a.ts", []byte("12345")) {
		t.Fatal("expected first object to fit")
	}
	if store.StoreObject("one", "b.ts", []byte("1")) {
		t.Fatal("expected per-channel capacity rejection")
	}
	if !store.StoreObject("two", "a.ts", []byte("123")) {
		t.Fatal("expected second channel to fit global capacity")
	}
	if store.StoreObject("three", "a.ts", []byte("1")) {
		t.Fatal("expected global capacity rejection")
	}
}

func TestIsolatedLiveStoreUpdatesCapacityAccounting(t *testing.T) {
	store := NewIsolatedLiveStore()
	store.maxChannelBytes = 5
	store.maxTotalBytes = 8

	if !store.StoreObject("existing", "segment.ts", []byte("12345")) {
		t.Fatal("expected initial object to fit")
	}
	if !store.StoreObject("existing", "segment.ts", []byte("12")) {
		t.Fatal("expected smaller replacement to fit")
	}
	if !store.StoreObject("existing", "playlist.m3u8", []byte("345")) {
		t.Fatal("expected replacement bytes to be released from channel capacity")
	}
	if !store.StoreObject("other", "segment.ts", []byte("678")) {
		t.Fatal("expected replacement bytes to be released from total capacity")
	}

	store.ClearChannel("existing")
	if !store.StoreObject("third", "segment.ts", []byte("12345")) {
		t.Fatal("expected clearing a channel to release its capacity")
	}

	store.ResetChannel("third")
	if !store.StoreObject("third", "segment.ts", []byte("12345")) {
		t.Fatal("expected resetting a channel to release its capacity")
	}
}

func TestLegacyLiveStoreObservesCallerOwnedBackingMap(t *testing.T) {
	var mu sync.RWMutex
	items := map[string]map[string][]byte{
		"external": {"segment.ts": []byte("12345")},
	}
	store := NewLiveStore(&mu, &items)
	store.maxTotalBytes = 5

	if store.StoreObject("other", "segment.ts", []byte("1")) {
		t.Fatal("expected caller-owned object to count toward capacity")
	}
}

func TestIsolatedLiveStoreReleasesExpiredSegmentCapacity(t *testing.T) {
	store := NewIsolatedLiveStore()
	store.deleteGrace = 10 * time.Second
	store.maxChannelBytes = 5
	store.maxTotalBytes = 5
	now := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }

	if !store.StoreObject("channel", "old.ts", []byte("12345")) {
		t.Fatal("expected initial segment to fit")
	}
	store.DeleteObject("channel", "old.ts")
	if store.StoreObject("channel", "new.ts", []byte("12345")) {
		t.Fatal("expected segment in deletion grace period to retain capacity")
	}

	now = now.Add(store.deleteGrace)
	if !store.StoreObject("channel", "new.ts", []byte("12345")) {
		t.Fatal("expected expired segment capacity to be released")
	}
}

func TestLiveHandlerIsReadOnly(t *testing.T) {
	fixture := newLiveTestFixture()

	req := httptest.NewRequest(http.MethodPut, "/live/testchannel/index.m3u8", strings.NewReader("#EXTM3U\n"))
	rec := httptest.NewRecorder()

	fixture.service.LiveHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status %d, got %d", http.StatusMethodNotAllowed, rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != "GET, HEAD" {
		t.Fatalf("expected read-only Allow header, got %q", got)
	}
}

func TestLiveWriteHandlerRequiresToken(t *testing.T) {
	fixture := newLiveTestFixture()
	req := httptest.NewRequest(http.MethodPut, "/_live-write/testchannel/index.m3u8", strings.NewReader("#EXTM3U\n"))
	rec := httptest.NewRecorder()

	fixture.service.LiveWriteHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestLiveWriteHandlerStoresReadableObject(t *testing.T) {
	fixture := newLiveTestFixture()
	fixture.store.ResetChannel("testchannel")
	fixture.installWriter("testchannel", testLiveGeneration)

	body := "#EXTM3U\n#EXTINF:2,\nsegment0.ts\n"
	writeReq := httptest.NewRequest(http.MethodPut, "/_live-write/testchannel/index.m3u8", strings.NewReader(body))
	writeReq.Header.Set(liveWriteTokenHeader, fixture.writeToken)
	writeReq.Header.Set(liveWriteGenerationHeader, testLiveGeneration)
	writeRec := httptest.NewRecorder()

	fixture.service.LiveWriteHandler().ServeHTTP(writeRec, writeReq)

	if writeRec.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d", http.StatusNoContent, writeRec.Code)
	}

	readReq := httptest.NewRequest(http.MethodGet, "/live/testchannel/index.m3u8", nil)
	readRec := httptest.NewRecorder()

	fixture.service.LiveHandler().ServeHTTP(readRec, readReq)

	if readRec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, readRec.Code)
	}
	if got := readRec.Body.String(); got != body {
		t.Fatalf("expected body %q, got %q", body, got)
	}
}

func TestLiveWriteHandlerRejectsStaleGeneration(t *testing.T) {
	fixture := newLiveTestFixture()
	fixture.installWriter("testchannel", "current-generation")

	staleReq := httptest.NewRequest(http.MethodPut, "/_live-write/testchannel/index.m3u8", strings.NewReader("stale"))
	staleReq.Header.Set(liveWriteTokenHeader, fixture.writeToken)
	staleReq.Header.Set(liveWriteGenerationHeader, "old-generation")
	staleRec := httptest.NewRecorder()
	fixture.service.LiveWriteHandler().ServeHTTP(staleRec, staleReq)

	if staleRec.Code != http.StatusConflict {
		t.Fatalf("expected stale writer status %d, got %d", http.StatusConflict, staleRec.Code)
	}
	if got := fixture.store.GetObject("testchannel", "index.m3u8"); got != nil {
		t.Fatalf("stale writer unexpectedly stored data %q", got)
	}

	currentReq := httptest.NewRequest(http.MethodPut, "/_live-write/testchannel/index.m3u8", strings.NewReader("current"))
	currentReq.Header.Set(liveWriteTokenHeader, fixture.writeToken)
	currentReq.Header.Set(liveWriteGenerationHeader, "current-generation")
	currentRec := httptest.NewRecorder()
	fixture.service.LiveWriteHandler().ServeHTTP(currentRec, currentReq)

	if currentRec.Code != http.StatusNoContent {
		t.Fatalf("expected current writer status %d, got %d", http.StatusNoContent, currentRec.Code)
	}
	if got := fixture.store.GetObject("testchannel", "index.m3u8"); string(got) != "current" {
		t.Fatalf("expected current writer data, got %q", got)
	}

	fixture.registry.mu.Lock()
	fixture.managers["testchannel"].state = streamStopping
	fixture.registry.mu.Unlock()
	lateReq := httptest.NewRequest(http.MethodPut, "/_live-write/testchannel/index.m3u8", strings.NewReader("late"))
	lateReq.Header.Set(liveWriteTokenHeader, fixture.writeToken)
	lateReq.Header.Set(liveWriteGenerationHeader, "current-generation")
	lateRec := httptest.NewRecorder()
	fixture.service.LiveWriteHandler().ServeHTTP(lateRec, lateReq)

	if lateRec.Code != http.StatusConflict {
		t.Fatalf("expected stopping writer status %d, got %d", http.StatusConflict, lateRec.Code)
	}
	if got := fixture.store.GetObject("testchannel", "index.m3u8"); string(got) != "current" {
		t.Fatalf("late writer replaced current data with %q", got)
	}
}

func TestLiveWriteHandlerRetainsRecentlyDeletedSegment(t *testing.T) {
	fixture := newLiveTestFixture()
	fixture.store.ResetChannel("testchannel")
	fixture.installWriter("testchannel", testLiveGeneration)

	writeLiveObject(t, fixture, http.MethodPut, "/_live-write/testchannel/segment0.ts", "segment data")
	writeLiveObject(t, fixture, http.MethodDelete, "/_live-write/testchannel/segment0.ts", "")

	req := httptest.NewRequest(http.MethodGet, "/live/testchannel/segment0.ts", nil)
	rec := httptest.NewRecorder()
	fixture.service.LiveHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d during deletion grace period, got %d", http.StatusOK, rec.Code)
	}
	if got, want := rec.Body.String(), "segment data"; got != want {
		t.Fatalf("expected body %q, got %q", want, got)
	}
}

func TestLiveStoreExpiresDeletedSegment(t *testing.T) {
	store := NewIsolatedLiveStore()
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
	fixture := newLiveTestFixture()

	req := httptest.NewRequest(http.MethodGet, "/live/testchannel/missing.ts", nil)
	rec := httptest.NewRecorder()
	fixture.service.LiveHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d", http.StatusNotFound, rec.Code)
	}
	if got, want := rec.Header().Get("Cache-Control"), "no-store"; got != want {
		t.Fatalf("expected Cache-Control %q, got %q", want, got)
	}
}

func TestLiveHandlerWaitsForActiveSegment(t *testing.T) {
	store := NewIsolatedLiveStore()

	var registryMu sync.Mutex
	managers := map[string]*manager{
		"testchannel": {state: streamRunning},
	}
	service := &LiveService{
		store:    store,
		registry: newStreamRegistry(&registryMu, &managers),
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

func writeLiveObject(t *testing.T, fixture *liveTestFixture, method, path, body string) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set(liveWriteTokenHeader, fixture.writeToken)
	req.Header.Set(liveWriteGenerationHeader, testLiveGeneration)
	rec := httptest.NewRecorder()
	fixture.service.LiveWriteHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status %d for %s %s, got %d", http.StatusNoContent, method, path, rec.Code)
	}
}

func TestLiveHandlerAutoStartsConfiguredInactivePlaylistRequest(t *testing.T) {
	fixture := newLiveTestFixture()
	started := false
	fixture.service.start = func(channel string) error {
		started = channel == "testchannel"
		fixture.store.ResetChannel(channel)
		fixture.store.StoreObject(channel, "index.m3u8", []byte("#EXTM3U\n"))
		fixture.installWriter(channel, testLiveGeneration)
		return nil
	}
	fixture.service.canAutoStart = func(channel string) bool { return channel == "testchannel" }

	req := httptest.NewRequest(http.MethodGet, "/live/testchannel/index.m3u8", nil)
	rec := httptest.NewRecorder()

	fixture.service.LiveHandler().ServeHTTP(rec, req)

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

func TestLiveHandlerDoesNotAutoStartUnconfiguredChannel(t *testing.T) {
	fixture := newLiveTestFixture()
	started := false
	fixture.service.start = func(string) error { started = true; return nil }
	fixture.service.canAutoStart = func(string) bool { return false }

	req := httptest.NewRequest(http.MethodGet, "/live/unknownchannel/index.m3u8", nil)
	rec := httptest.NewRecorder()
	fixture.service.LiveHandler().ServeHTTP(rec, req)

	if started {
		t.Fatal("unconfigured channel request unexpectedly started a stream")
	}
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d", http.StatusNotFound, rec.Code)
	}
}

func TestLiveHandlerHeadDoesNotAutoStartConfiguredChannel(t *testing.T) {
	fixture := newLiveTestFixture()
	started := false
	fixture.service.start = func(string) error { started = true; return nil }
	fixture.service.canAutoStart = func(string) bool { return true }

	req := httptest.NewRequest(http.MethodHead, "/live/testchannel/index.m3u8", nil)
	rec := httptest.NewRecorder()
	fixture.service.LiveHandler().ServeHTTP(rec, req)

	if started {
		t.Fatal("HEAD request unexpectedly started a stream")
	}
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d", http.StatusNotFound, rec.Code)
	}
}

func TestLiveWriteHandlerRejectsOversizedObject(t *testing.T) {
	fixture := newLiveTestFixture()
	fixture.store.ResetChannel("testchannel")
	fixture.installWriter("testchannel", testLiveGeneration)

	tooLarge := bytes.Repeat([]byte("a"), maxLiveObjectBytes+1)
	req := httptest.NewRequest(http.MethodPut, "/_live-write/testchannel/segment.ts", bytes.NewReader(tooLarge))
	req.Header.Set(liveWriteTokenHeader, fixture.writeToken)
	req.Header.Set(liveWriteGenerationHeader, testLiveGeneration)
	rec := httptest.NewRecorder()

	fixture.service.LiveWriteHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected status %d, got %d", http.StatusRequestEntityTooLarge, rec.Code)
	}
}

func TestLiveHandlerReturnsServiceUnavailableForActiveChannelWhenPlaylistMissing(t *testing.T) {
	fixture := newLiveTestFixture()
	fixture.store.ResetChannel("testchannel")

	origTimeout := livePlaylistWaitTimeout
	origPoll := livePlaylistWaitPoll
	livePlaylistWaitTimeout = 20 * time.Millisecond
	livePlaylistWaitPoll = 5 * time.Millisecond
	t.Cleanup(func() {
		livePlaylistWaitTimeout = origTimeout
		livePlaylistWaitPoll = origPoll
	})

	fixture.installWriter("testchannel", testLiveGeneration)

	req := httptest.NewRequest(http.MethodGet, "/live/testchannel/index.m3u8", nil)
	rec := httptest.NewRecorder()

	fixture.service.LiveHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status %d, got %d", http.StatusServiceUnavailable, rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "1" {
		t.Fatalf("expected Retry-After header %q, got %q", "1", got)
	}
}
