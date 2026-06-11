package stream

import (
	"strings"
	"testing"
)

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
