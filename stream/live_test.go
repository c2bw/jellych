package stream

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
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
