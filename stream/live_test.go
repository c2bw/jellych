package stream

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testLiveGeneration = "test-generation"

func TestLiveStoreReleasesServedMarkersWithSegments(t *testing.T) {
	for _, tracked := range []bool{false, true} {
		for _, grace := range []time.Duration{0, time.Second} {
			t.Run(fmt.Sprintf("tracked=%v/grace=%v", tracked, grace), func(t *testing.T) {
				var mu sync.RWMutex
				items := make(map[string]map[string][]byte)
				store := newLiveStore(&mu, &items, tracked)
				now := time.Now()
				store.now = func() time.Time { return now }
				store.deleteGrace = grace
				store.StoreObject("channel", "old.ts", []byte("old"))
				store.SetWindowUnlockSegment("channel", "old.ts")
				store.NoteSegmentServed("channel", "old.ts")
				if !store.IsSegmentServed("channel", "old.ts") || !store.IsWindowUnlocked("channel") {
					t.Fatal("serving the startup segment should unlock the window")
				}
				store.DeleteObject("channel", "old.ts")
				if grace > 0 && !store.IsSegmentServed("channel", "old.ts") {
					t.Fatal("marker should remain while the segment is in its grace period")
				}
				now = now.Add(grace)
				store.StoreObject("channel", "next.ts", []byte("next"))
				if store.GetObject("channel", "old.ts") != nil || store.IsSegmentServed("channel", "old.ts") {
					t.Fatal("purged segments must release their served markers")
				}
				store.NoteSegmentServed("channel", "old.ts")
				if len(store.servedSegments) != 0 {
					t.Fatal("a delayed read notification must not recreate a purged marker")
				}
				if !store.IsWindowUnlocked("channel") {
					t.Fatal("purging an old segment must not relock the startup window")
				}
			})
		}
	}
}

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

func TestLiveHandlerReturnsAvailablePlaylistImmediately(t *testing.T) {
	fixture := newLiveTestFixture()
	fixture.installWriter("testchannel", testLiveGeneration)
	fixture.service.minStartupSegments = 3
	playlist := "#EXTM3U\n#EXTINF:2,\nseg0.ts\n"
	fixture.store.StoreObject("testchannel", "index.m3u8", []byte(playlist))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	rec := httptest.NewRecorder()
	fixture.service.LiveHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/live/testchannel/index.m3u8", nil).WithContext(ctx))
	if ctx.Err() != nil || rec.Code != http.StatusOK || rec.Body.String() != playlist {
		t.Fatalf("available playlist was delayed or changed: context=%v status=%d body=%q", ctx.Err(), rec.Code, rec.Body.String())
	}
}

func TestLiveHandlerStopsWaitingWhenProducerExits(t *testing.T) {
	fixture := newLiveTestFixture()
	fixture.installWriter("testchannel", testLiveGeneration)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started := make(chan struct{})
	fixture.service.onActivity = func(string) {
		select {
		case <-started:
		default:
			close(started)
		}
	}
	go func() {
		<-started
		fixture.registry.mu.Lock()
		m := fixture.managers["testchannel"]
		fixture.registry.mu.Unlock()
		fixture.registry.finishManager("testchannel", m)
	}()
	rec := httptest.NewRecorder()
	fixture.service.LiveHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/live/testchannel/index.m3u8", nil).WithContext(ctx))
	if ctx.Err() != nil || rec.Code != http.StatusBadGateway {
		t.Fatalf("producer exit did not fail promptly: context=%v status=%d body=%q", ctx.Err(), rec.Code, rec.Body.String())
	}
}

func TestLiveHandlerReportsStartupFailure(t *testing.T) {
	fixture := newLiveTestFixture()
	fixture.service.canAutoStart = func(string) bool { return true }
	fixture.service.start = func(string) error { return errors.New("temporary resolver failure") }
	rec := httptest.NewRecorder()
	fixture.service.LiveHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/live/testchannel/index.m3u8", nil))
	if rec.Code != http.StatusBadGateway || rec.Header().Get("Retry-After") != "1" {
		t.Fatalf("startup failure: status=%d headers=%v", rec.Code, rec.Header())
	}
}

func TestLiveHandlerAutoStartReturnsFirstSegmentImmediatelyAndProgressiveWindowUnlocks(t *testing.T) {
	fixture := newLiveTestFixture()
	fixture.service.minStartupSegments = 3
	fixture.service.canAutoStart = func(channel string) bool { return channel == "testchannel" }

	oneSegPlaylist := "#EXTM3U\n#EXTINF:2,\nseg0.ts\n"
	fiveSegPlaylist := "#EXTM3U\n#EXTINF:2,\nseg0.ts\n#EXTINF:2,\nseg1.ts\n#EXTINF:2,\nseg2.ts\n#EXTINF:2,\nseg3.ts\n#EXTINF:2,\nseg4.ts\n"
	threeSegWindow := "#EXTM3U\n#EXTINF:2,\nseg0.ts\n#EXTINF:2,\nseg1.ts\n#EXTINF:2,\nseg2.ts\n"

	fixture.service.start = func(channel string) error {
		fixture.store.ResetChannel(channel)
		fixture.store.StoreObject(channel, "seg0.ts", []byte("s0"))
		fixture.store.StoreObject(channel, "index.m3u8", []byte(oneSegPlaylist))
		fixture.installWriter(channel, testLiveGeneration)
		return nil
	}

	// 1. Initial auto-start request (e.g. ffprobe) returns immediately with 1 segment.
	probeReq := httptest.NewRequest(http.MethodGet, "/live/testchannel/index.m3u8", nil)
	probeRec := httptest.NewRecorder()
	fixture.service.LiveHandler().ServeHTTP(probeRec, probeReq)
	if probeRec.Code != http.StatusOK || probeRec.Body.String() != oneSegPlaylist {
		t.Fatalf("expected auto-start probe to get 1-segment playlist immediately, got status=%d body=%q", probeRec.Code, probeRec.Body.String())
	}

	// ffprobe reads only seg0.ts.
	seg0Req := httptest.NewRequest(http.MethodGet, "/live/testchannel/seg0.ts", nil)
	seg0Rec := httptest.NewRecorder()
	fixture.service.LiveHandler().ServeHTTP(seg0Rec, seg0Req)

	// Meanwhile, Jellych's ffmpeg buffers 5 segments into LiveStore.
	for i := 1; i < 5; i++ {
		fixture.store.StoreObject("testchannel", fmt.Sprintf("seg%d.ts", i), []byte("data"))
	}
	fixture.store.StoreObject("testchannel", "index.m3u8", []byte(fiveSegPlaylist))

	// 2. Jellyfin's ffmpeg opens index.m3u8 and receives the trimmed 3-segment window (seg0..seg2)
	// so FFmpeg's -live_start_index -3 starts at seg0 instead of skipping seg0 and seg1.
	ffmpegReq1 := httptest.NewRequest(http.MethodGet, "/live/testchannel/index.m3u8", nil)
	ffmpegRec1 := httptest.NewRecorder()
	fixture.service.LiveHandler().ServeHTTP(ffmpegRec1, ffmpegReq1)
	if got := ffmpegRec1.Body.String(); got != threeSegWindow {
		t.Fatalf("expected trimmed 3-segment initial window %q, got %q", threeSegWindow, got)
	}

	// Jellyfin's ffmpeg reads seg0.ts, seg1.ts, seg2.ts; reading seg2.ts unlocks the full window.
	for _, seg := range []string{"seg0.ts", "seg1.ts", "seg2.ts"} {
		req := httptest.NewRequest(http.MethodGet, "/live/testchannel/"+seg, nil)
		rec := httptest.NewRecorder()
		fixture.service.LiveHandler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected segment %s to succeed, got %d", seg, rec.Code)
		}
	}

	// 3. On the next playlist reload, the full 5-segment playlist is returned immediately.
	ffmpegReq2 := httptest.NewRequest(http.MethodGet, "/live/testchannel/index.m3u8", nil)
	ffmpegRec2 := httptest.NewRecorder()
	fixture.service.LiveHandler().ServeHTTP(ffmpegRec2, ffmpegReq2)
	if got := ffmpegRec2.Body.String(); got != fiveSegPlaylist {
		t.Fatalf("expected unlocked 5-segment playlist %q, got %q", fiveSegPlaylist, got)
	}
}

func TestLiveHandlerWaitsForNextSegmentOnReloadDuringStartupBurst(t *testing.T) {
	fixture := newLiveTestFixture()
	fixture.store.ResetChannel("testchannel")
	fixture.installWriter("testchannel", testLiveGeneration)
	fixture.service.minStartupSegments = 3

	origAdvance := livePlaylistAdvanceTimeout
	origPoll := livePlaylistWaitPoll
	livePlaylistAdvanceTimeout = time.Second
	livePlaylistWaitPoll = 5 * time.Millisecond
	t.Cleanup(func() {
		livePlaylistAdvanceTimeout = origAdvance
		livePlaylistWaitPoll = origPoll
	})

	threeSegPlaylist := "#EXTM3U\n#EXTINF:2,\nseg0.ts\n#EXTINF:2,\nseg1.ts\n#EXTINF:2,\nseg2.ts\n"
	fourSegPlaylist := threeSegPlaylist + "#EXTINF:2,\nseg3.ts\n"
	for i := 0; i < 3; i++ {
		fixture.store.StoreObject("testchannel", fmt.Sprintf("seg%d.ts", i), []byte("data"))
	}
	fixture.store.StoreObject("testchannel", "index.m3u8", []byte(threeSegPlaylist))

	// First playlist fetch sets unlock segment to seg2.ts.
	req1 := httptest.NewRequest(http.MethodGet, "/live/testchannel/index.m3u8", nil)
	rec1 := httptest.NewRecorder()
	fixture.service.LiveHandler().ServeHTTP(rec1, req1)

	// Client reads seg2.ts, unlocking the window and marking seg2.ts as served.
	segReq := httptest.NewRequest(http.MethodGet, "/live/testchannel/seg2.ts", nil)
	segRec := httptest.NewRecorder()
	fixture.service.LiveHandler().ServeHTTP(segRec, segReq)

	// Client reloads index.m3u8 while seg3.ts is arriving 20ms later.
	go func() {
		time.Sleep(20 * time.Millisecond)
		fixture.store.StoreObject("testchannel", "seg3.ts", []byte("data"))
		fixture.store.StoreObject("testchannel", "index.m3u8", []byte(fourSegPlaylist))
	}()

	req2 := httptest.NewRequest(http.MethodGet, "/live/testchannel/index.m3u8", nil)
	rec2 := httptest.NewRecorder()
	fixture.service.LiveHandler().ServeHTTP(rec2, req2)
	if got := rec2.Body.String(); got != fourSegPlaylist {
		t.Fatalf("expected reload to wait for 4th segment playlist %q, got %q", fourSegPlaylist, got)
	}
}

func TestResolveLiveProxyPlaylistCachesUpstreamURLAndFetchesFreshPlaylist(t *testing.T) {
	liveProxyURLCache.Lock()
	liveProxyURLCache.entries = nil
	liveProxyURLCache.inflight = nil
	liveProxyURLCache.Unlock()
	originalResolve := resolveLiveProxyUpstream
	originalFetch := fetchLiveProxyPlaylist
	t.Cleanup(func() {
		resolveLiveProxyUpstream = originalResolve
		fetchLiveProxyPlaylist = originalFetch
	})

	var resolves atomic.Int32
	var fetches atomic.Int32
	resolveLiveProxyUpstream = func(_ context.Context, channel string) (string, error) {
		resolves.Add(1)
		return "https://weaver.example.test/" + channel + "/index.m3u8", nil
	}
	fetchLiveProxyPlaylist = func(_ context.Context, playlistURL string) ([]byte, error) {
		seq := fetches.Add(1)
		return []byte(fmt.Sprintf("#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:%d\n%s/seg-%d.ts\n", seq, playlistURL, seq)), nil
	}

	first, err := ResolveLiveProxyPlaylist(context.Background(), "Caedrel")
	if err != nil {
		t.Fatalf("first ResolveLiveProxyPlaylist: %v", err)
	}
	second, err := ResolveLiveProxyPlaylist(context.Background(), "caedrel")
	if err != nil {
		t.Fatalf("second ResolveLiveProxyPlaylist: %v", err)
	}
	if got := resolves.Load(); got != 1 {
		t.Fatalf("expected 1 upstream URL resolution across polls, got %d", got)
	}
	if got := fetches.Load(); got != 2 {
		t.Fatalf("expected 2 playlist fetches across polls, got %d", got)
	}
	if string(first) == string(second) {
		t.Fatalf("expected each poll to return fresh playlist content, got identical %q", string(first))
	}
}

func TestResolveLiveProxyPlaylistReResolvesWhenCachedUpstreamFails(t *testing.T) {
	liveProxyURLCache.Lock()
	liveProxyURLCache.entries = nil
	liveProxyURLCache.inflight = nil
	liveProxyURLCache.Unlock()
	originalResolve := resolveLiveProxyUpstream
	originalFetch := fetchLiveProxyPlaylist
	t.Cleanup(func() {
		resolveLiveProxyUpstream = originalResolve
		fetchLiveProxyPlaylist = originalFetch
	})

	var resolves atomic.Int32
	resolveLiveProxyUpstream = func(_ context.Context, _ string) (string, error) {
		n := resolves.Add(1)
		return fmt.Sprintf("https://weaver.example.test/session-%d.m3u8", n), nil
	}
	var failFirstCachedFetch atomic.Bool
	fetchLiveProxyPlaylist = func(_ context.Context, playlistURL string) ([]byte, error) {
		if playlistURL == "https://weaver.example.test/session-1.m3u8" && failFirstCachedFetch.Load() {
			return nil, errors.New("upstream playlist returned status 403")
		}
		return []byte("#EXTM3U\n" + playlistURL + "\n"), nil
	}

	if _, err := ResolveLiveProxyPlaylist(context.Background(), "shroud"); err != nil {
		t.Fatalf("initial ResolveLiveProxyPlaylist: %v", err)
	}
	failFirstCachedFetch.Store(true)

	recovered, err := ResolveLiveProxyPlaylist(context.Background(), "shroud")
	if err != nil {
		t.Fatalf("recovered ResolveLiveProxyPlaylist: %v", err)
	}
	if got := resolves.Load(); got != 2 {
		t.Fatalf("expected expired upstream URL to trigger re-resolution, got %d resolves", got)
	}
	if !strings.Contains(string(recovered), "session-2.m3u8") {
		t.Fatalf("expected recovered playlist from session-2, got %q", string(recovered))
	}
}

func TestOfflineLiveStreamErrorIsClassified(t *testing.T) {
	err := errors.New("failed to resolve stream URL: no playback token returned; stream may be offline or unavailable")
	if !isOfflineLiveStreamError(err) {
		t.Fatal("expected offline error to be classified")
	}
	if isOfflineLiveStreamError(errors.New("dial tcp: i/o timeout")) {
		t.Fatal("did not expect network timeout to be classified as offline")
	}
}

func TestResolveLiveStreamURLNormalizesChannelAndReturnsUpstreamURL(t *testing.T) {
	originalResolve := resolveLiveProxyUpstream
	t.Cleanup(func() {
		resolveLiveProxyUpstream = originalResolve
	})

	resolveLiveProxyUpstream = func(_ context.Context, channel string) (string, error) {
		return "https://weaver.example.test/" + channel + ".m3u8", nil
	}

	got, err := ResolveLiveStreamURL(context.Background(), "  Caedrel ")
	if err != nil {
		t.Fatalf("ResolveLiveStreamURL: %v", err)
	}
	if want := "https://weaver.example.test/caedrel.m3u8"; got != want {
		t.Fatalf("ResolveLiveStreamURL = %q; want %q", got, want)
	}

	if _, err := ResolveLiveStreamURL(context.Background(), "bad channel!"); err == nil {
		t.Fatal("expected invalid channel name to fail")
	}
}
