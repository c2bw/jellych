package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/c2bw/jellych/stream"
)

func TestVODPlaybackKeepsUnreadMediaAlive(t *testing.T) {
	start := time.Date(2026, time.July, 12, 0, 0, 0, 0, time.UTC)
	now := start
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("segment"))
	}))
	defer upstream.Close()
	state := &APIState{}
	state.SetVODs([]VOD{
		{ID: "123", URL: "https://www.twitch.tv/videos/123"},
		{ID: "456", URL: "https://www.twitch.tv/videos/456"},
	})
	operations := defaultStreamOperations()
	operations.ResolveVODPlaylist = func(context.Context, string) ([]byte, error) {
		return []byte("#EXTM3U\n#EXTINF:2,\n" + upstream.URL + "/early.ts\n#EXTINF:2,\n" + upstream.URL + "/late.ts\n#EXT-X-ENDLIST\n"), nil
	}
	a := NewWithDependencies(state, Dependencies{Streams: operations, Now: func() time.Time { return now }})
	handler := a.Handler()
	get := func(path string, wantStatus int) string {
		t.Helper()
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != wantStatus {
			t.Fatalf("GET %s = %d: %s", path, response.Code, response.Body.String())
		}
		return response.Body.String()
	}
	mediaPaths := func(playlist string) []string {
		var paths []string
		for _, line := range strings.Split(playlist, "\n") {
			if strings.HasPrefix(line, "/vod/") {
				paths = append(paths, line)
			}
		}
		return paths
	}
	active := mediaPaths(get("/vod/123/index.m3u8", http.StatusOK))
	inactive := mediaPaths(get("/vod/456/index.m3u8", http.StatusOK))
	if len(active) != 2 || len(inactive) != 2 {
		t.Fatal("expected both playlist segments to be proxied")
	}
	now = start.Add(11 * time.Hour)
	get(active[0], http.StatusOK)
	now = start.Add(13 * time.Hour)
	// Other playlist requests trigger pruning while this VOD is playing.
	a.vodMediaRegistry.prune(now)
	get(active[1], http.StatusOK)
	get(inactive[1], http.StatusNotFound)

	// Activity on one recording must not extend another recording's lease.
	now = now.Add(vodMediaTokenTTL + time.Second)
	a.vodMediaRegistry.prune(now)
	get(active[0], http.StatusNotFound)
	if len(a.vodMediaRegistry.byToken) != 0 || len(a.vodMediaRegistry.byURL) != 0 || len(a.vodMediaRegistry.vodExpiresAt) != 0 {
		t.Fatal("inactive VOD tokens and playlist leases should be reclaimed")
	}
}

func TestLiveMediaActivityDoesNotRetainOldSegments(t *testing.T) {
	start := time.Date(2026, time.July, 12, 0, 0, 0, 0, time.UTC)
	a := New()
	oldToken, err := a.liveMediaRegistry.register("channel", "https://example.test/old.ts", start)
	if err != nil {
		t.Fatal(err)
	}
	newToken, err := a.liveMediaRegistry.register("channel", "https://example.test/new.ts", start)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := a.liveMediaRegistry.lookup("channel", newToken, start.Add(4*time.Minute)); !ok {
		t.Fatal("recent live segment should remain available for retries")
	}
	now := start.Add(6 * time.Minute)
	a.liveMediaRegistry.prune(now)
	if _, ok := a.liveMediaRegistry.lookup("channel", oldToken, now); ok {
		t.Fatal("live activity must not retain old segments from the same channel")
	}
	if _, ok := a.liveMediaRegistry.lookup("channel", newToken, now); !ok {
		t.Fatal("recently read segment should retain its retry window")
	}
}

func TestLiveProxyReclaimsTokensBeforeCapacityIsExhausted(t *testing.T) {
	start := time.Date(2026, time.July, 12, 0, 0, 0, 0, time.UTC)
	now := start
	a := NewWithDependencies(nil, Dependencies{Now: func() time.Time { return now }})
	// Ten channels publishing a segment every two seconds formerly exhausted
	// the shared cap in under twelve hours. Seed that history without pruning.
	for i := 0; i < maxVODMediaTokens; i++ {
		now = start.Add(time.Duration(i/10) * 2 * time.Second)
		if _, err := a.liveMediaRegistry.register(fmt.Sprintf("channel%d", i%10), fmt.Sprintf("https://example.test/%d.ts", i), now); err != nil {
			t.Fatal(err)
		}
	}
	now = now.Add(2 * time.Second)
	playlist, err := a.rewriteLiveProxyPlaylist("channel0", []byte("#EXTM3U\n#EXTINF:2,\nhttps://example.test/latest.ts\n"))
	if err != nil {
		t.Fatalf("live playlist after eleven hours: %v", err)
	}
	if !strings.Contains(string(playlist), "/api/live-proxy/channel0/media/") {
		t.Fatal("latest live segment must still be proxied")
	}
	if len(a.liveMediaRegistry.byToken) > 5000 || len(a.liveMediaRegistry.byURL) > 5000 {
		t.Fatal("live registry retained more than the recent buffering window")
	}
}

func TestCopyVODMediaObjectWithinLimit(t *testing.T) {
	for _, test := range []struct {
		name  string
		body  string
		limit int64
	}{
		{name: "under limit", body: "media", limit: 6},
		{name: "exact limit", body: "media", limit: 5},
	} {
		t.Run(test.name, func(t *testing.T) {
			var dst bytes.Buffer
			if err := copyVODMediaObject(&dst, bytes.NewBufferString(test.body), test.limit); err != nil {
				t.Fatalf("copy media object: %v", err)
			}
			if got := dst.String(); got != test.body {
				t.Fatalf("copied body = %q; want %q", got, test.body)
			}
		})
	}
}

func TestCopyVODMediaObjectRejectsOverflowWithoutWritingExtraByte(t *testing.T) {
	var dst bytes.Buffer
	err := copyVODMediaObject(&dst, bytes.NewBufferString("media!"), 5)
	if !errors.Is(err, errVODMediaObjectTooLarge) {
		t.Fatalf("copy error = %v; want %v", err, errVODMediaObjectTooLarge)
	}
	if got := dst.String(); got != "media" {
		t.Fatalf("copied body = %q; want only the five permitted bytes", got)
	}
}

func TestVODMediaRegistryRegisterDefersBulkPruning(t *testing.T) {
	now := time.Date(2026, time.July, 12, 12, 0, 0, 0, time.UTC)
	expired := vodMediaTarget{
		vodID:     "old-vod",
		url:       "https://example.test/old.ts",
		expiresAt: now.Add(-time.Second),
	}
	registry := &vodMediaRegistry{
		byToken: map[string]vodMediaTarget{"expired-token": expired},
		byURL:   map[string]string{expired.vodID + "\x00" + expired.url: "expired-token"},
	}

	if _, err := registry.register("new-vod", "https://example.test/new.ts", now); err != nil {
		t.Fatalf("register media URL: %v", err)
	}
	if _, ok := registry.byToken["expired-token"]; !ok {
		t.Fatal("register unexpectedly scanned and pruned unrelated tokens")
	}

	registry.prune(now)
	if _, ok := registry.byToken["expired-token"]; ok {
		t.Fatal("explicit prune retained expired token")
	}
	if _, ok := registry.byURL[expired.vodID+"\x00"+expired.url]; ok {
		t.Fatal("explicit prune retained expired reverse lookup")
	}
	if len(registry.byToken) != 1 {
		t.Fatalf("explicit prune removed active token; token count = %d", len(registry.byToken))
	}
}

func TestVODMediaRegistryLookupOnlyChecksRequestedToken(t *testing.T) {
	now := time.Date(2026, time.July, 12, 12, 0, 0, 0, time.UTC)
	active := vodMediaTarget{
		vodID:     "active-vod",
		url:       "https://example.test/active.ts",
		expiresAt: now.Add(time.Minute),
	}
	expired := vodMediaTarget{
		vodID:     "old-vod",
		url:       "https://example.test/old.ts",
		expiresAt: now.Add(-time.Second),
	}
	registry := &vodMediaRegistry{
		byToken: map[string]vodMediaTarget{
			"active-token":  active,
			"expired-token": expired,
		},
		byURL: map[string]string{
			active.vodID + "\x00" + active.url:   "active-token",
			expired.vodID + "\x00" + expired.url: "expired-token",
		},
	}

	got, ok := registry.lookup(active.vodID, "active-token", now)
	if !ok || got != active.url {
		t.Fatalf("lookup = %q, %v; want %q, true", got, ok, active.url)
	}
	if _, ok := registry.byToken["expired-token"]; !ok {
		t.Fatal("lookup unexpectedly scanned and pruned unrelated tokens")
	}

	if _, ok := registry.lookup(expired.vodID, "expired-token", now); ok {
		t.Fatal("lookup accepted expired token")
	}
	if _, ok := registry.byToken["expired-token"]; ok {
		t.Fatal("lookup retained requested expired token")
	}
	if _, ok := registry.byURL[expired.vodID+"\x00"+expired.url]; ok {
		t.Fatal("lookup retained requested expired reverse lookup")
	}
}

func TestLiveProxyPlaylistAndMediaForUnconfiguredChannel(t *testing.T) {
	upstreamMedia := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/seg-0.ts" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "video/mp2t")
		_, _ = w.Write([]byte("live-segment-bytes"))
	}))
	defer upstreamMedia.Close()

	state := &APIState{}
	// Channel "unfollowed" is intentionally not added to state.SetChannels.
	apiInstance := NewWithDependencies(state, Dependencies{
		VODMediaHTTPClient: upstreamMedia.Client(),
		Streams: completeTestStreamOperations(StreamOperations{
			ResolveLiveProxyPlaylist: func(_ context.Context, channel string) ([]byte, error) {
				if channel == "offlinechan" {
					return nil, fmt.Errorf("%w: offline", stream.ErrLiveStreamOffline)
				}
				return []byte("#EXTM3U\n#EXTINF:2.0,\n" + upstreamMedia.URL + "/seg-0.ts\n"), nil
			},
		}),
	})
	handler := apiInstance.Handler()

	// 1. Offline channel returns 404.
	offlineReq := httptest.NewRequest(http.MethodGet, "/api/live-proxy/offlinechan/index.m3u8", nil)
	offlineRec := httptest.NewRecorder()
	handler.ServeHTTP(offlineRec, offlineReq)
	if offlineRec.Code != http.StatusNotFound {
		t.Fatalf("offline status = %d; want %d", offlineRec.Code, http.StatusNotFound)
	}

	// 2. Unconfigured live channel returns rewritten playlist with Cache-Control: no-store.
	playlistReq := httptest.NewRequest(http.MethodGet, "/api/live-proxy/unfollowed/index.m3u8", nil)
	playlistRec := httptest.NewRecorder()
	handler.ServeHTTP(playlistRec, playlistReq)
	if playlistRec.Code != http.StatusOK {
		t.Fatalf("playlist status = %d; want %d: %s", playlistRec.Code, http.StatusOK, playlistRec.Body.String())
	}
	if got := playlistRec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q; want no-store", got)
	}
	var mediaPath string
	for _, line := range strings.Split(playlistRec.Body.String(), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "/api/live-proxy/unfollowed/media/") {
			mediaPath = line
			break
		}
	}
	if mediaPath == "" {
		t.Fatalf("playlist did not contain rewritten media path: %q", playlistRec.Body.String())
	}

	// 3. Rewritten media token proxies the segment bytes.
	mediaReq := httptest.NewRequest(http.MethodGet, mediaPath, nil)
	mediaRec := httptest.NewRecorder()
	handler.ServeHTTP(mediaRec, mediaReq)
	if mediaRec.Code != http.StatusOK {
		t.Fatalf("media status = %d; want %d", mediaRec.Code, http.StatusOK)
	}
	if got := mediaRec.Body.String(); got != "live-segment-bytes" {
		t.Fatalf("media body = %q; want %q", got, "live-segment-bytes")
	}
}

func TestLiveStreamURLEndpointReturnsUpstreamURLAndMapsOfflineToNotFound(t *testing.T) {
	apiInstance := NewWithDependencies(&APIState{}, Dependencies{
		Streams: completeTestStreamOperations(StreamOperations{
			ResolveLiveStreamURL: func(_ context.Context, channel string) (string, error) {
				if channel == "offlinechan" {
					return "", fmt.Errorf("%w: offline", stream.ErrLiveStreamOffline)
				}
				return "https://video-weaver.example.test/v1/playlist/" + channel + ".m3u8", nil
			},
		}),
	})
	handler := apiInstance.Handler()

	offlineReq := httptest.NewRequest(http.MethodGet, "/api/live-url/offlinechan", nil)
	offlineRec := httptest.NewRecorder()
	handler.ServeHTTP(offlineRec, offlineReq)
	if offlineRec.Code != http.StatusNotFound {
		t.Fatalf("offline status = %d; want %d", offlineRec.Code, http.StatusNotFound)
	}

	liveReq := httptest.NewRequest(http.MethodGet, "/api/live-url/Caedrel", nil)
	liveRec := httptest.NewRecorder()
	handler.ServeHTTP(liveRec, liveReq)
	if liveRec.Code != http.StatusOK {
		t.Fatalf("live status = %d; want %d: %s", liveRec.Code, http.StatusOK, liveRec.Body.String())
	}
	if got := liveRec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q; want no-store", got)
	}
	for _, want := range []string{`"channel": "caedrel"`, `"url": "https://video-weaver.example.test/v1/playlist/caedrel.m3u8"`} {
		if !strings.Contains(liveRec.Body.String(), want) {
			t.Fatalf("response %q did not contain %q", liveRec.Body.String(), want)
		}
	}
}
