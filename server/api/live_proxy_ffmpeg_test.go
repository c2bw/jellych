package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Use real HLS demuxing to catch URL rewriting incompatibilities that a plain
// HTTP client cannot detect. Set JELLYCH_TEST_FFMPEG to use a container wrapper.
func TestLiveProxyFFmpegPlayback(t *testing.T) {
	binary := os.Getenv("JELLYCH_TEST_FFMPEG")
	if binary == "" {
		var err error
		binary, err = exec.LookPath("ffmpeg")
		if err != nil {
			t.Skip("ffmpeg is not installed")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dir := t.TempDir()
	generate := exec.CommandContext(ctx, binary,
		"-v", "error", "-f", "lavfi", "-i", "testsrc=size=128x96:rate=10",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-t", "12", "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
		"-g", "20", "-c:a", "aac", "-f", "hls", "-hls_time", "2",
		"-hls_list_size", "0", filepath.Join(dir, "index.m3u8"))
	if output, err := generate.CombinedOutput(); err != nil {
		t.Fatalf("generate HLS: %v\n%s", err, output)
	}
	upstream := httptest.NewServer(http.FileServer(http.Dir(dir)))
	defer upstream.Close()
	playlist, err := os.ReadFile(filepath.Join(dir, "index.m3u8"))
	if err != nil {
		t.Fatal(err)
	}
	// Present a live playlist, as Twitch does, rather than a finite recording.
	var normalized strings.Builder
	for _, line := range strings.Split(string(playlist), "\n") {
		if line == "#EXT-X-ENDLIST" {
			continue
		}
		if line != "" && !strings.HasPrefix(line, "#") {
			line = upstream.URL + "/" + line
		}
		normalized.WriteString(line + "\n")
	}
	operations := completeTestStreamOperations(StreamOperations{
		ResolveLiveProxyPlaylist: func(context.Context, string) ([]byte, error) {
			return []byte(normalized.String()), nil
		},
		Start: func(string) error {
			t.Error("proxy playback unexpectedly started a local remux")
			return nil
		},
	})
	api := NewWithDependencies(nil, Dependencies{Streams: operations, VODMediaHTTPClient: upstream.Client()})
	server := httptest.NewServer(api.Handler())
	defer server.Close()
	started := time.Now()
	decode := exec.CommandContext(ctx, binary, "-v", "info", "-analyzeduration", "3000000",
		"-i", server.URL+"/api/live-proxy/channel/index.m3u8", "-t", "2", "-f", "null", "-")
	if output, err := decode.CombinedOutput(); err != nil {
		t.Fatalf("decode proxied live HLS: %v\n%s", err, output)
	}
	t.Logf("decoded two seconds of proxied H.264/AAC in %v", time.Since(started))
}
