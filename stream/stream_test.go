package stream

import (
	"slices"
	"testing"
)

func TestBuildFFmpegHLSArgsUsesRelativeSegmentURLs(t *testing.T) {
	args := buildFFmpegHLSArgs("https://example.test/upstream.m3u8", "http://127.0.0.1/_live-write/testchannel/index.m3u8")

	if slices.Contains(args, "-hls_base_url") {
		t.Fatal("did not expect -hls_base_url; segment URLs must remain relative to the playlist")
	}

	if got, want := args[len(args)-1], "http://127.0.0.1/_live-write/testchannel/index.m3u8"; got != want {
		t.Fatalf("expected playlist URL %q, got %q", want, got)
	}
}

func TestBuildFFmpegHLSArgsKeepsTrailingSegments(t *testing.T) {
	args := buildFFmpegHLSArgs("https://example.test/upstream.m3u8", "http://127.0.0.1/_live-write/testchannel/index.m3u8")

	thresholdIndex := slices.Index(args, "-hls_delete_threshold")
	if thresholdIndex == -1 || thresholdIndex+1 >= len(args) {
		t.Fatal("expected -hls_delete_threshold argument")
	}
	if got, want := args[thresholdIndex+1], "30"; got != want {
		t.Fatalf("expected hls delete threshold %q, got %q", want, got)
	}
}

func TestBuildFFmpegHLSArgsUsesUniqueUncachedSegments(t *testing.T) {
	args := buildFFmpegHLSArgs("https://example.test/upstream.m3u8", "http://127.0.0.1/_live-write/testchannel/index.m3u8")

	assertArgValue(t, args, "-hls_start_number_source", "epoch")
	assertArgValue(t, args, "-hls_allow_cache", "0")
}

func assertArgValue(t *testing.T, args []string, name, want string) {
	t.Helper()
	index := slices.Index(args, name)
	if index == -1 || index+1 >= len(args) {
		t.Fatalf("expected %s argument", name)
	}
	if got := args[index+1]; got != want {
		t.Fatalf("expected %s value %q, got %q", name, want, got)
	}
}
