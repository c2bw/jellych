package stream

import (
	"slices"
	"testing"
)

func TestBuildFFmpegHLSArgsIncludesPublicBaseURL(t *testing.T) {
	args := buildFFmpegHLSArgs("testchannel", "https://example.test/upstream.m3u8", "http://127.0.0.1/_live-write/testchannel/index.m3u8", " https://example.test/root/ ")

	baseIndex := slices.Index(args, "-hls_base_url")
	if baseIndex == -1 || baseIndex+1 >= len(args) {
		t.Fatal("expected -hls_base_url argument")
	}
	if got, want := args[baseIndex+1], "https://example.test/root/live/testchannel/"; got != want {
		t.Fatalf("expected hls base url %q, got %q", want, got)
	}

	if got, want := args[len(args)-1], "http://127.0.0.1/_live-write/testchannel/index.m3u8"; got != want {
		t.Fatalf("expected playlist URL %q, got %q", want, got)
	}
}

func TestBuildFFmpegHLSArgsKeepsTrailingSegments(t *testing.T) {
	args := buildFFmpegHLSArgs("testchannel", "https://example.test/upstream.m3u8", "http://127.0.0.1/_live-write/testchannel/index.m3u8", "")

	thresholdIndex := slices.Index(args, "-hls_delete_threshold")
	if thresholdIndex == -1 || thresholdIndex+1 >= len(args) {
		t.Fatal("expected -hls_delete_threshold argument")
	}
	if got, want := args[thresholdIndex+1], "30"; got != want {
		t.Fatalf("expected hls delete threshold %q, got %q", want, got)
	}
}

func TestBuildFFmpegHLSArgsOmitsEmptyPublicBaseURL(t *testing.T) {
	args := buildFFmpegHLSArgs("testchannel", "https://example.test/upstream.m3u8", "http://127.0.0.1/_live-write/testchannel/index.m3u8", " ")

	if slices.Contains(args, "-hls_base_url") {
		t.Fatal("did not expect -hls_base_url for empty public base URL")
	}
}
