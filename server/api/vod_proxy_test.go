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
