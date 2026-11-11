package twitch

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/c2bw/jellych/server/api"
	"github.com/c2bw/jellych/stream"
	"github.com/c2bw/jellych/twitch/manager"
)

func TestPruningProtectsInterruptedConversionBeforeRecovery(t *testing.T) {
	dir := t.TempDir()
	partialDir := filepath.Join(dir, ".jellych-partials")
	if err := os.MkdirAll(partialDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(partialDir, "123.original"), []byte("original recording"), 0600); err != nil {
		t.Fatal(err)
	}
	downloads := stream.NewVODDownloader()
	downloads.SetDir(dir)
	pruned, err := pruneVODIfNoDownload(downloads, "123", func() error {
		t.Error("metadata for a recoverable recording must not be removed")
		return nil
	})
	if err != nil || pruned {
		t.Fatalf("interrupted conversion pruned=%v, error=%v", pruned, err)
	}
}

func TestStartRecoversConversionsBeforeReturning(t *testing.T) {
	configDir := t.TempDir()
	state := &api.APIState{}
	m, err := manager.StartWithState(configDir, state)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.AddVOD(api.VOD{ID: "123", URL: "https://www.twitch.tv/videos/123"}); err != nil {
		_ = m.Close()
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	partialDir := filepath.Join(dir, ".jellych-partials")
	if err := os.MkdirAll(partialDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(partialDir, "123.original"), []byte("original recording"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(partialDir, "123-interrupted.part"), []byte("incomplete encode"), 0600); err != nil {
		t.Fatal(err)
	}
	downloads := stream.NewVODDownloader()
	downloads.SetDir(dir)
	// Disable network work; recovery must complete synchronously at startup.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stop, err := StartContext(ctx, nil, configDir, state, downloads)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	file, err := downloads.Open("123")
	if err != nil {
		t.Fatalf("recovered recording is not playable: %v", err)
	}
	defer file.Close()
	contents, err := io.ReadAll(file)
	if err != nil || string(contents) != "original recording" {
		t.Fatalf("recovered bytes=%q, error=%v", contents, err)
	}
	if _, ok := state.FindVOD("123"); !ok {
		t.Fatal("recovery must preserve persisted VOD metadata")
	}
	entries, err := os.ReadDir(partialDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("interrupted artifacts left after startup: %v, error=%v", entries, err)
	}
}

func TestStartStopsBeforeSynchronizationWhenRecoveryFails(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".jellych-partials"), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	downloads := stream.NewVODDownloader()
	downloads.SetDir(dir)
	stop, err := StartContext(context.Background(), nil, t.TempDir(), &api.APIState{}, downloads)
	if stop != nil {
		stop()
		t.Fatal("startup must not start synchronization after a recovery failure")
	}
	if err == nil {
		t.Fatal("expected startup to report the recovery failure")
	}
}

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
