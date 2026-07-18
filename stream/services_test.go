package stream

import (
	"errors"
	"testing"
)

func TestServicesOwnIndependentRuntimeState(t *testing.T) {
	one := NewServices("http://127.0.0.1:8080")
	two := NewServices("http://127.0.0.1:8081")

	one.live.store.StoreObject("channel", "index.m3u8", []byte("#EXTM3U\n"))
	if got := two.live.store.GetObject("channel", "index.m3u8"); got != nil {
		t.Fatalf("second service inherited first service's live media: %q", got)
	}
	if one.live.writeToken == two.live.writeToken {
		t.Fatal("independent services unexpectedly share a live-write token")
	}

	one.Downloads.SetDir(t.TempDir())
	if _, _, err := two.Downloads.Status("123"); !errors.Is(err, ErrVODDownloadsDisabled) {
		t.Fatalf("second service inherited first service's download directory: %v", err)
	}
}
