package stream

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestStartVODDownloadSkipsExistingFile(t *testing.T) {
	dir := t.TempDir()
	SetVODDownloadDir(dir)
	t.Cleanup(func() { SetVODDownloadDir("") })

	path := filepath.Join(dir, "123456789.mp4")
	if err := os.WriteFile(path, []byte("already here"), 0644); err != nil {
		t.Fatalf("failed to create existing vod file: %v", err)
	}

	err := StartVODDownload("123456789", "https://www.twitch.tv/videos/123456789")
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

	path := filepath.Join(dir, "123456789.mp4")
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

	path := filepath.Join(dir, "123456789.mp4")
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
