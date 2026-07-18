package twitch

import (
	"testing"

	"github.com/c2bw/jellych/stream"
)

func TestPruningWorksWhenDownloadsDisabled(t *testing.T) {
	downloads := stream.NewVODDownloader()
	downloads.SetDir("")

	removed := false
	pruned, err := pruneVODIfNoDownload(downloads, "123456789", func() error {
		removed = true
		return nil
	})
	if err != nil {
		t.Fatalf("expected VOD pruning to proceed when downloads are disabled: %v", err)
	}
	if !pruned || !removed {
		t.Fatal("expected metadata removal callback")
	}
}
