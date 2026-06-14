package stream

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestStartVODDownloadSkipsExistingFile(t *testing.T) {
	dir := t.TempDir()
	SetVODDownloadDir(dir)
	t.Cleanup(func() { SetVODDownloadDir("") })

	path := filepath.Join(dir, "123456789.mkv")
	if err := os.WriteFile(path, []byte("already here"), 0644); err != nil {
		t.Fatalf("failed to create existing vod file: %v", err)
	}

	err := StartVODDownload(context.Background(), "123456789", "https://www.twitch.tv/videos/123456789", "Test VOD", "testchannel")
	if !errors.Is(err, ErrVODDownloadAlreadyExists) {
		t.Fatalf("expected ErrVODDownloadAlreadyExists, got %v", err)
	}
}

func TestVODDownloadExists(t *testing.T) {
	dir := t.TempDir()
	SetVODDownloadDir(dir)
	t.Cleanup(func() { SetVODDownloadDir("") })

	exists, err := VODDownloadExists("123456789")
	if err != nil {
		t.Fatalf("expected missing download check to succeed, got %v", err)
	}
	if exists {
		t.Fatal("did not expect missing download to exist")
	}

	path := filepath.Join(dir, "123456789.mkv")
	if err := os.WriteFile(path, []byte("downloaded"), 0644); err != nil {
		t.Fatalf("failed to create downloaded vod file: %v", err)
	}

	exists, err = VODDownloadExists("123456789")
	if err != nil {
		t.Fatalf("expected existing download check to succeed, got %v", err)
	}
	if !exists {
		t.Fatal("expected downloaded vod file to exist")
	}
}

func TestDeleteVODDownloadRemovesExistingFile(t *testing.T) {
	dir := t.TempDir()
	SetVODDownloadDir(dir)
	t.Cleanup(func() { SetVODDownloadDir("") })

	path := filepath.Join(dir, "123456789.mkv")
	if err := os.WriteFile(path, []byte("downloaded"), 0644); err != nil {
		t.Fatalf("failed to create downloaded vod file: %v", err)
	}

	if err := DeleteVODDownload("123456789"); err != nil {
		t.Fatalf("expected delete to succeed, got %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected downloaded vod file to be removed, stat error was %v", err)
	}
}

func TestDeleteVODDownloadReturnsNotFound(t *testing.T) {
	dir := t.TempDir()
	SetVODDownloadDir(dir)
	t.Cleanup(func() { SetVODDownloadDir("") })

	err := DeleteVODDownload("123456789")
	if !errors.Is(err, ErrVODDownloadNotFound) {
		t.Fatalf("expected ErrVODDownloadNotFound, got %v", err)
	}
}

func TestBuildVODDownloadArgsIncludesInputAndMetadata(t *testing.T) {
	path := filepath.Join("vods", "123456789.mkv")
	inputURL := "https://vod-secure.twitch.tv/index.m3u8?token=test"
	args := buildVODDownloadArgs(inputURL, path, "A great stream", "Streamer")

	for _, want := range []string{
		inputURL,
		"copy",
		"title=A great stream",
		"artist=Streamer",
		"matroska",
		path,
	} {
		if !slices.Contains(args, want) {
			t.Fatalf("expected download args to contain %q, got %#v", want, args)
		}
	}
}

func TestResolveVODDownloadURLTimesOut(t *testing.T) {
	started := time.Now()
	_, err := resolveVODDownloadURLWithTimeout(
		context.Background(),
		"https://www.twitch.tv/videos/123456789",
		20*time.Millisecond,
		func(ctx context.Context, _ string) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		},
	)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context deadline exceeded, got %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("expected resolver timeout promptly, took %v", elapsed)
	}
}
