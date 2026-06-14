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

func TestCleanupExpiredVODDownloads(t *testing.T) {
	dir := t.TempDir()
	SetVODDownloadDir(dir)
	SetVODDownloadRetention(30 * 24 * time.Hour)
	t.Cleanup(func() {
		SetVODDownloadDir("")
		SetVODDownloadRetention(defaultVODDownloadRetention)
	})

	now := time.Date(2026, time.June, 14, 12, 0, 0, 0, time.UTC)
	writeVODDownloadTestFile(t, dir, "expired.mkv", now.Add(-31*24*time.Hour))
	writeVODDownloadTestFile(t, dir, "recent.mkv", now.Add(-29*24*time.Hour))
	writeVODDownloadTestFile(t, dir, "unrelated.txt", now.Add(-60*24*time.Hour))
	writeVODDownloadTestFile(t, dir, "invalid name.mkv", now.Add(-60*24*time.Hour))
	if err := os.Mkdir(filepath.Join(dir, "directory.mkv"), 0755); err != nil {
		t.Fatalf("failed to create test directory: %v", err)
	}

	deleted, err := cleanupExpiredVODDownloads(now)
	if err != nil {
		t.Fatalf("expected cleanup to succeed, got %v", err)
	}
	if deleted != 1 {
		t.Fatalf("expected one expired download to be deleted, got %d", deleted)
	}
	assertVODDownloadTestFileExists(t, filepath.Join(dir, "expired.mkv"), false)
	for _, name := range []string{"recent.mkv", "unrelated.txt", "invalid name.mkv", "directory.mkv"} {
		assertVODDownloadTestFileExists(t, filepath.Join(dir, name), true)
	}
}

func TestCleanupExpiredVODDownloadsSkipsActiveDownload(t *testing.T) {
	dir := t.TempDir()
	SetVODDownloadDir(dir)
	SetVODDownloadRetention(24 * time.Hour)
	t.Cleanup(func() {
		SetVODDownloadDir("")
		SetVODDownloadRetention(defaultVODDownloadRetention)
	})

	now := time.Date(2026, time.June, 14, 12, 0, 0, 0, time.UTC)
	writeVODDownloadTestFile(t, dir, "active.mkv", now.Add(-48*time.Hour))
	download := &vodDownload{}
	vodDownloadState.Lock()
	vodDownloadState.active = map[string]*vodDownload{"active": download}
	vodDownloadState.Unlock()
	t.Cleanup(func() { clearVODDownload("active", download) })

	deleted, err := cleanupExpiredVODDownloads(now)
	if err != nil {
		t.Fatalf("expected cleanup to succeed, got %v", err)
	}
	if deleted != 0 {
		t.Fatalf("expected active download to be preserved, deleted %d files", deleted)
	}
	assertVODDownloadTestFileExists(t, filepath.Join(dir, "active.mkv"), true)
}

func TestRemoveExpiredVODDownloadRechecksActiveState(t *testing.T) {
	dir := t.TempDir()
	SetVODDownloadDir(dir)
	t.Cleanup(func() { SetVODDownloadDir("") })

	path := filepath.Join(dir, "active.mkv")
	writeVODDownloadTestFile(t, dir, "active.mkv", time.Now().Add(-48*time.Hour))

	download := &vodDownload{}
	vodDownloadState.Lock()
	vodDownloadState.active = map[string]*vodDownload{"active": download}
	vodDownloadState.Unlock()
	t.Cleanup(func() { clearVODDownload("active", download) })

	removed, err := removeExpiredVODDownload(dir, "active", path)
	if err != nil {
		t.Fatalf("expected active download recheck to succeed, got %v", err)
	}
	if removed {
		t.Fatal("expected active download to be preserved")
	}
	assertVODDownloadTestFileExists(t, path, true)
}

func TestCleanupExpiredVODDownloadsToleratesMissingDirectory(t *testing.T) {
	SetVODDownloadDir(filepath.Join(t.TempDir(), "missing"))
	SetVODDownloadRetention(24 * time.Hour)
	t.Cleanup(func() {
		SetVODDownloadDir("")
		SetVODDownloadRetention(defaultVODDownloadRetention)
	})

	deleted, err := cleanupExpiredVODDownloads(time.Now())
	if err != nil {
		t.Fatalf("expected missing directory to be ignored, got %v", err)
	}
	if deleted != 0 {
		t.Fatalf("expected no deletions, got %d", deleted)
	}
}

func TestCleanupExpiredVODDownloadsReportsReadError(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(file, []byte("test"), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}
	SetVODDownloadDir(file)
	SetVODDownloadRetention(24 * time.Hour)
	t.Cleanup(func() {
		SetVODDownloadDir("")
		SetVODDownloadRetention(defaultVODDownloadRetention)
	})

	if _, err := cleanupExpiredVODDownloads(time.Now()); err == nil {
		t.Fatal("expected cleanup to report directory read error")
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

func writeVODDownloadTestFile(t *testing.T, dir, name string, modTime time.Time) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("downloaded"), 0644); err != nil {
		t.Fatalf("failed to create %s: %v", name, err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatalf("failed to set modification time for %s: %v", name, err)
	}
}

func assertVODDownloadTestFileExists(t *testing.T, path string, want bool) {
	t.Helper()
	_, err := os.Stat(path)
	if want && err != nil {
		t.Fatalf("expected %s to exist, got %v", path, err)
	}
	if !want && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected %s to be absent, got %v", path, err)
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
