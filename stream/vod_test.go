package stream

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestResolveVODPlaylistSingleflightsAndCaches(t *testing.T) {
	vodPlaylistCache.Lock()
	vodPlaylistCache.entries = nil
	vodPlaylistCache.inflight = nil
	vodPlaylistCache.Unlock()
	originalResolve := resolveVODPlaylistUpstream
	originalFetch := fetchVODPlaylist
	t.Cleanup(func() {
		resolveVODPlaylistUpstream = originalResolve
		fetchVODPlaylist = originalFetch
	})

	var resolves atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	resolveVODPlaylistUpstream = func(context.Context, string) (string, error) {
		if resolves.Add(1) == 1 {
			close(started)
		}
		<-release
		return "https://example.test/index.m3u8", nil
	}
	fetchVODPlaylist = func(context.Context, string) ([]byte, error) {
		return []byte("#EXTM3U\n"), nil
	}

	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := ResolveVODPlaylist(context.Background(), "https://www.twitch.tv/videos/123")
			results <- err
		}()
	}
	<-started
	close(release)
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("unexpected resolve error: %v", err)
		}
	}
	if _, err := ResolveVODPlaylist(context.Background(), "https://www.twitch.tv/videos/123"); err != nil {
		t.Fatalf("unexpected cached resolve error: %v", err)
	}
	if got := resolves.Load(); got != 1 {
		t.Fatalf("expected one upstream resolution, got %d", got)
	}
}

func TestNormalizeHLSPlaylistURLsMakesRelativeURIsAbsolute(t *testing.T) {
	playlist := []byte(`#EXTM3U
#EXT-X-KEY:METHOD=AES-128,URI="keys/key.bin"
#EXTINF:4.0,
segment-000.ts
#EXT-X-MAP:URI="../init.mp4"
https://cdn.example.test/absolute.ts
`)

	got, err := NormalizeHLSPlaylistURLs("https://vod-secure.twitch.tv/path/to/index.m3u8?token=abc", playlist)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	text := string(got)

	for _, want := range []string{
		`URI="https://vod-secure.twitch.tv/path/to/keys/key.bin"`,
		"https://vod-secure.twitch.tv/path/to/segment-000.ts",
		`URI="https://vod-secure.twitch.tv/path/init.mp4"`,
		"https://cdn.example.test/absolute.ts",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected normalized playlist to contain %q, got %q", want, text)
		}
	}
}

func TestNormalizeHLSPlaylistURLsLeavesDataURIsAlone(t *testing.T) {
	playlist := []byte(`#EXTM3U
#EXT-X-KEY:METHOD=AES-128,URI="data:text/plain;base64,abcd"
`)

	got, err := NormalizeHLSPlaylistURLs("https://vod-secure.twitch.tv/index.m3u8", playlist)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !strings.Contains(string(got), `URI="data:text/plain;base64,abcd"`) {
		t.Fatalf("expected data URI to be preserved, got %q", string(got))
	}
}

func TestRestrictedVODManifestErrorIsClassified(t *testing.T) {
	err := fmt.Errorf(`usher playlist request failed: 403 Forbidden: [{"error":"Manifest is restricted","error_code":"vod_manifest_restricted","type":"error"}]`)

	if !isRestrictedVODManifestError(err) {
		t.Fatal("expected restricted VOD manifest error to be classified")
	}
}

func TestRestrictedVODManifestErrorIgnoresUnrelatedErrors(t *testing.T) {
	err := errors.New("usher playlist request failed: 404 Not Found")

	if isRestrictedVODManifestError(err) {
		t.Fatal("did not expect unrelated usher error to be classified")
	}
}
