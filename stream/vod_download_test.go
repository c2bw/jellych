package stream

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
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
	if err := os.WriteFile(path, nil, 0644); err != nil {
		t.Fatalf("failed to create empty interrupted file: %v", err)
	}
	exists, err = VODDownloadExists("123456789")
	if err != nil {
		t.Fatalf("expected empty download check to succeed, got %v", err)
	}
	if exists {
		t.Fatal("did not expect empty file to count as downloaded")
	}
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

func TestOpenVODDownloadOpensOnlyCompletedFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "123456789.mkv")
	if err := os.WriteFile(path, []byte("downloaded"), 0644); err != nil {
		t.Fatal(err)
	}

	downloader := &VODDownloader{dir: dir, retention: defaultVODDownloadRetention}
	file, err := downloader.Open("123456789")
	if err != nil {
		t.Fatalf("open completed download: %v", err)
	}
	defer file.Close()
	data := make([]byte, len("downloaded"))
	if _, err := file.Read(data); err != nil {
		t.Fatalf("read completed download: %v", err)
	}
	if string(data) != "downloaded" {
		t.Fatalf("opened data = %q; want downloaded", data)
	}
}

func TestOpenVODDownloadRejectsActiveAndIncompleteFiles(t *testing.T) {
	for _, test := range []struct {
		name   string
		active bool
		body   []byte
	}{
		{name: "active", active: true, body: []byte("previous completed file")},
		{name: "empty", body: nil},
		{name: "missing"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			if test.body != nil || test.name == "empty" {
				if err := os.WriteFile(filepath.Join(dir, "123456789.mkv"), test.body, 0644); err != nil {
					t.Fatal(err)
				}
			}
			downloader := &VODDownloader{dir: dir, retention: defaultVODDownloadRetention}
			if test.active {
				downloader.active = map[string]*vodDownload{"123456789": newVODDownload()}
			}

			file, err := downloader.Open("123456789")
			if file != nil {
				_ = file.Close()
				t.Fatal("expected no file")
			}
			if !errors.Is(err, ErrVODDownloadNotFound) {
				t.Fatalf("open error = %v; want %v", err, ErrVODDownloadNotFound)
			}
		})
	}
}

func TestActiveConversionDoesNotExposeOriginalVOD(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "123456789.mkv")
	if err := os.WriteFile(path, []byte("original download"), 0644); err != nil {
		t.Fatal(err)
	}
	download := newVODDownload()
	download.progress.Operation = "convert"
	download.progress.Downloaded = true
	downloader := &VODDownloader{
		dir:       dir,
		retention: defaultVODDownloadRetention,
		active:    map[string]*vodDownload{"123456789": download},
	}

	file, err := downloader.Open("123456789")
	if file != nil {
		_ = file.Close()
		t.Fatal("conversion unexpectedly exposed the original VOD")
	}
	if !errors.Is(err, ErrVODDownloadNotFound) {
		t.Fatalf("open error = %v; want %v", err, ErrVODDownloadNotFound)
	}

	downloaded, _, err := downloader.Status("123456789")
	if err != nil {
		t.Fatalf("check completed VOD during conversion: %v", err)
	}
	if downloaded {
		t.Fatal("conversion unexpectedly reported local playback as available")
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

func TestVODDownloaderStopPreventsNewStarts(t *testing.T) {
	downloader := &VODDownloader{
		dir:       t.TempDir(),
		retention: defaultVODDownloadRetention,
	}

	if err := downloader.Stop(); err != nil {
		t.Fatalf("expected stop to succeed, got %v", err)
	}

	err := downloader.Start(context.Background(), "123456789", "https://www.twitch.tv/videos/123456789", "Test VOD", "testchannel")
	if !errors.Is(err, ErrVODDownloadsStopping) {
		t.Fatalf("expected ErrVODDownloadsStopping, got %v", err)
	}
}

func TestVODDownloaderStopPreventsResolvingDownloadFromStartingProcess(t *testing.T) {
	downloader := &VODDownloader{retention: defaultVODDownloadRetention}
	download := newVODDownload()
	ctx, cancel := context.WithCancel(context.Background())
	download.cancelOperation = cancel
	downloader.active = map[string]*vodDownload{"123456789": download}
	go func() {
		<-ctx.Done()
		downloader.finishDownload("123456789", download, "", "", time.Now(), ctx.Err())
	}()

	if err := downloader.Stop(); err != nil {
		t.Fatalf("expected stop to succeed, got %v", err)
	}
	if downloader.prepareStart("123456789", download) {
		t.Fatal("expected shutdown to prevent resolving download from starting ffmpeg")
	}
}

func TestVODDownloaderRequestCancellationStopsResolution(t *testing.T) {
	dir := t.TempDir()
	resolveStarted := make(chan struct{})
	commandStarted := make(chan struct{}, 1)
	downloader := &VODDownloader{
		dir:       dir,
		retention: defaultVODDownloadRetention,
		resolveURL: func(ctx context.Context, _ string) (string, error) {
			close(resolveStarted)
			<-ctx.Done()
			return "", ctx.Err()
		},
		commandContext: func(ctx context.Context, name string, args ...string) *exec.Cmd {
			commandStarted <- struct{}{}
			return exec.CommandContext(ctx, name, args...)
		},
	}

	requestCtx, cancelRequest := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- downloader.Start(requestCtx, "123456789", "https://www.twitch.tv/videos/123456789", "Test VOD", "testchannel")
	}()
	<-resolveStarted
	cancelRequest()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("start error = %v; want context cancellation", err)
	}
	select {
	case <-commandStarted:
		t.Fatal("request cancellation allowed ffmpeg to start")
	default:
	}
}

func TestVODDownloaderAcceptedJobOwnsCommandContext(t *testing.T) {
	operationContext := make(chan context.Context, 1)
	downloader := &VODDownloader{
		dir:       t.TempDir(),
		retention: defaultVODDownloadRetention,
		resolveURL: func(context.Context, string) (string, error) {
			return "https://example.test/index.m3u8", nil
		},
		commandContext: func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
			operationContext <- ctx
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestVODDownloadCommandHelper$")
			cmd.Env = append(os.Environ(), "GO_WANT_VOD_DOWNLOAD_HELPER=1")
			return cmd
		},
	}

	requestCtx, cancelRequest := context.WithCancel(context.Background())
	if err := downloader.Start(requestCtx, "123456789", "https://www.twitch.tv/videos/123456789", "Test VOD", "testchannel"); err != nil {
		t.Fatalf("start download: %v", err)
	}
	operationCtx := <-operationContext
	cancelRequest()

	select {
	case <-operationCtx.Done():
		t.Fatal("accepted job inherited request cancellation")
	case <-time.After(100 * time.Millisecond):
	}
	downloader.Lock()
	download := downloader.active["123456789"]
	downloader.Unlock()
	if download == nil || !downloader.prepareStart("123456789", download) {
		t.Fatal("accepted job stopped after request context ended")
	}

	if err := downloader.Stop(); err != nil {
		t.Fatalf("stop downloader: %v", err)
	}
	select {
	case <-operationCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("explicit stop did not cancel the command context")
	}
}

func TestVODDownloadCommandHelper(t *testing.T) {
	if os.Getenv("GO_WANT_VOD_DOWNLOAD_HELPER") != "1" {
		return
	}
	for {
		time.Sleep(time.Second)
	}
}

func TestFinishVODDownloadSignalsDoneAfterRemovingPartialFile(t *testing.T) {
	downloader := &VODDownloader{retention: defaultVODDownloadRetention}
	download := newVODDownload()
	downloader.active = map[string]*vodDownload{"123456789": download}
	dir := t.TempDir()
	tempPath := filepath.Join(dir, "123456789.part")
	outputPath := filepath.Join(dir, "123456789.mkv")
	if err := os.WriteFile(tempPath, []byte("partial"), 0644); err != nil {
		t.Fatalf("failed to create partial output file: %v", err)
	}

	downloader.finishDownload("123456789", download, tempPath, outputPath, time.Now(), errors.New("interrupted"))
	<-download.done

	assertVODDownloadTestFileExists(t, tempPath, false)
	assertVODDownloadTestFileExists(t, outputPath, false)
	downloader.Lock()
	_, active := downloader.active["123456789"]
	downloader.Unlock()
	if active {
		t.Fatal("expected interrupted download to be removed from active map")
	}
}

func TestFinishVODDownloadAtomicallyFinalizesSuccessfulFile(t *testing.T) {
	dir := t.TempDir()
	tempPath := filepath.Join(dir, "123456789.part")
	outputPath := filepath.Join(dir, "123456789.mkv")
	if err := os.WriteFile(tempPath, []byte("complete vod"), 0644); err != nil {
		t.Fatalf("failed to create partial output file: %v", err)
	}

	downloader := &VODDownloader{retention: defaultVODDownloadRetention}
	download := newVODDownload()
	downloader.active = map[string]*vodDownload{"123456789": download}
	downloader.finishDownload("123456789", download, tempPath, outputPath, time.Now(), nil)
	<-download.done

	assertVODDownloadTestFileExists(t, tempPath, false)
	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("failed to read finalized vod: %v", err)
	}
	if got, want := string(data), "complete vod"; got != want {
		t.Fatalf("expected finalized data %q, got %q", want, got)
	}
}

func TestInterruptedPartialIsNotReportedAsDownloadedAndIsCleaned(t *testing.T) {
	dir := t.TempDir()
	partialDir := vodPartialDir(dir)
	if err := os.MkdirAll(partialDir, 0755); err != nil {
		t.Fatalf("failed to create partial directory: %v", err)
	}
	partialPath := filepath.Join(partialDir, "123456789-crashed.part")
	if err := os.WriteFile(partialPath, []byte("partial"), 0644); err != nil {
		t.Fatalf("failed to create interrupted partial: %v", err)
	}

	downloader := &VODDownloader{dir: dir, retention: defaultVODDownloadRetention}
	downloaded, _, err := downloader.Status("123456789")
	if err != nil {
		t.Fatalf("failed to check interrupted download: %v", err)
	}
	if downloaded {
		t.Fatal("interrupted partial was reported as downloaded")
	}
	deleted, err := downloader.cleanupExpired(time.Now())
	if err != nil {
		t.Fatalf("failed to reconcile interrupted download: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("expected one interrupted partial to be deleted, got %d", deleted)
	}
	assertVODDownloadTestFileExists(t, partialPath, false)
}

func TestRemoveVODWithArtifactsDeletesFilesBeforeMetadata(t *testing.T) {
	dir := t.TempDir()
	partialDir := vodPartialDir(dir)
	if err := os.MkdirAll(partialDir, 0755); err != nil {
		t.Fatalf("failed to create partial directory: %v", err)
	}
	outputPath := filepath.Join(dir, "123456789.mkv")
	partialPath := filepath.Join(partialDir, "123456789-crashed.part")
	for path, data := range map[string]string{outputPath: "complete", partialPath: "partial"} {
		if err := os.WriteFile(path, []byte(data), 0644); err != nil {
			t.Fatalf("failed to create test artifact %s: %v", path, err)
		}
	}

	downloader := &VODDownloader{dir: dir, retention: defaultVODDownloadRetention}
	metadataRemoved := false
	err := downloader.RemoveWithArtifacts("123456789", func() error {
		assertVODDownloadTestFileExists(t, outputPath, false)
		assertVODDownloadTestFileExists(t, partialPath, false)
		metadataRemoved = true
		return nil
	})
	if err != nil {
		t.Fatalf("expected cascade removal to succeed, got %v", err)
	}
	if !metadataRemoved {
		t.Fatal("expected metadata removal callback")
	}
}

func TestRemoveVODWithArtifactsCancelsActiveDownloadAndBlocksRestart(t *testing.T) {
	dir := t.TempDir()
	downloader := &VODDownloader{dir: dir, retention: defaultVODDownloadRetention}
	download := newVODDownload()
	ctx, cancel := context.WithCancel(context.Background())
	download.cancelOperation = cancel
	downloader.active = map[string]*vodDownload{"123456789": download}
	finished := make(chan struct{})
	go func() {
		<-ctx.Done()
		downloader.finishDownload("123456789", download, "", filepath.Join(dir, "123456789.mkv"), time.Now(), ctx.Err())
		close(finished)
	}()

	metadataStarted := make(chan struct{})
	releaseMetadata := make(chan struct{})
	removeErr := make(chan error, 1)
	go func() {
		removeErr <- downloader.RemoveWithArtifacts("123456789", func() error {
			close(metadataStarted)
			<-releaseMetadata
			return nil
		})
	}()

	<-metadataStarted
	<-finished
	if err := downloader.Start(context.Background(), "123456789", "https://www.twitch.tv/videos/123456789", "Test", "channel"); !errors.Is(err, ErrVODRemovalInProgress) {
		t.Fatalf("expected restart during metadata removal to fail, got %v", err)
	}
	close(releaseMetadata)
	if err := <-removeErr; err != nil {
		t.Fatalf("expected active cascade removal to succeed, got %v", err)
	}
}

func TestRemoveMetadataIfNoDownloadProtectsActiveAndCompletedDownloads(t *testing.T) {
	dir := t.TempDir()
	downloader := &VODDownloader{dir: dir, retention: defaultVODDownloadRetention}
	downloader.active = map[string]*vodDownload{"123456789": newVODDownload()}
	called := false
	remove := func() error {
		called = true
		return nil
	}
	if err := downloader.RemoveMetadataIfNoDownload("123456789", remove); !errors.Is(err, ErrVODDownloadProtected) {
		t.Fatalf("expected active download to be protected, got %v", err)
	}
	delete(downloader.active, "123456789")
	writeVODDownloadTestFile(t, dir, "123456789.mkv", time.Now())
	if err := downloader.RemoveMetadataIfNoDownload("123456789", remove); !errors.Is(err, ErrVODDownloadProtected) {
		t.Fatalf("expected completed download to be protected, got %v", err)
	}
	if called {
		t.Fatal("did not expect metadata callback for protected download")
	}
}

func TestRemoveMetadataIfNoDownloadBlocksConcurrentStart(t *testing.T) {
	dir := t.TempDir()
	downloader := &VODDownloader{dir: dir, retention: defaultVODDownloadRetention}
	metadataStarted := make(chan struct{})
	releaseMetadata := make(chan struct{})
	removeErr := make(chan error, 1)
	go func() {
		removeErr <- downloader.RemoveMetadataIfNoDownload("123456789", func() error {
			close(metadataStarted)
			<-releaseMetadata
			return nil
		})
	}()

	<-metadataStarted
	if err := downloader.Start(context.Background(), "123456789", "https://www.twitch.tv/videos/123456789", "Test", "channel"); !errors.Is(err, ErrVODRemovalInProgress) {
		t.Fatalf("expected download startup to be blocked during pruning, got %v", err)
	}
	close(releaseMetadata)
	if err := <-removeErr; err != nil {
		t.Fatalf("expected metadata pruning to succeed, got %v", err)
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
	args := buildVODDownloadArgs(inputURL, path, "A great stream", "Streamer", VODDownloadPresetOriginal)

	for _, want := range []string{
		inputURL,
		"copy",
		"-y",
		"-progress",
		"pipe:1",
		"title=A great stream",
		"artist=Streamer",
		"jellych_download_preset=original",
		"matroska",
		path,
	} {
		if !slices.Contains(args, want) {
			t.Fatalf("expected download args to contain %q, got %#v", want, args)
		}
	}
}

func TestCompletedVODDownloadPresetIsReadOnceAndCached(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "123456789.mkv")
	if err := os.WriteFile(path, []byte("downloaded"), 0644); err != nil {
		t.Fatal(err)
	}
	downloader := &VODDownloader{dir: dir, retention: defaultVODDownloadRetention}
	originalProbe := probeVODDownloadMetadata
	probeCalls := 0
	probeVODDownloadMetadata = func(gotPath string) (vodDownloadMetadata, error) {
		probeCalls++
		if gotPath != path {
			t.Fatalf("expected probe path %q, got %q", path, gotPath)
		}
		return vodDownloadMetadata{
			Preset: VODDownloadPresetHEVC, OriginalSize: 12345,
			DurationSeconds: 10, VideoCodec: "h264", VideoWidth: 1920, VideoHeight: 1080,
		}, nil
	}
	t.Cleanup(func() { probeVODDownloadMetadata = originalProbe })

	for range 2 {
		progress, err := downloader.Progress("123456789")
		if err != nil {
			t.Fatal(err)
		}
		if progress.Preset != "hevc" {
			t.Fatalf("expected HEVC preset, got %q", progress.Preset)
		}
		if progress.OriginalSize != 12345 {
			t.Fatalf("expected original size metadata, got %d", progress.OriginalSize)
		}
		if progress.VideoCodec != "h264" || progress.VideoWidth != 1920 || progress.VideoHeight != 1080 {
			t.Fatalf("unexpected video metadata: %#v", progress)
		}
		if progress.TotalBitrate != 8 {
			t.Fatalf("expected total bitrate 8 bps, got %d", progress.TotalBitrate)
		}
	}
	if probeCalls != 1 {
		t.Fatalf("expected one metadata probe, got %d", probeCalls)
	}
}

func TestCompletedVODDownloadPresetProbeFailureIsNotCached(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "123456789.mkv")
	if err := os.WriteFile(path, []byte("downloaded"), 0644); err != nil {
		t.Fatal(err)
	}
	downloader := &VODDownloader{dir: dir, retention: defaultVODDownloadRetention}
	originalProbe := probeVODDownloadMetadata
	probeCalls := 0
	probeVODDownloadMetadata = func(string) (vodDownloadMetadata, error) {
		probeCalls++
		if probeCalls == 1 {
			return vodDownloadMetadata{}, errors.New("temporary probe failure")
		}
		return vodDownloadMetadata{Preset: VODDownloadPresetVP9}, nil
	}
	t.Cleanup(func() { probeVODDownloadMetadata = originalProbe })

	first, err := downloader.Progress("123456789")
	if err != nil {
		t.Fatal(err)
	}
	if !first.Downloaded || first.Preset != "" {
		t.Fatalf("expected downloaded file with unknown preset after probe failure, got %#v", first)
	}
	second, err := downloader.Progress("123456789")
	if err != nil {
		t.Fatal(err)
	}
	if second.Preset != "" || probeCalls != 1 {
		t.Fatalf("expected failed probe to be backed off, got preset %q after %d calls", second.Preset, probeCalls)
	}
	downloader.Lock()
	cached := downloader.presets["123456789"]
	cached.retryAt = time.Time{}
	downloader.presets["123456789"] = cached
	downloader.Unlock()
	third, err := downloader.Progress("123456789")
	if err != nil {
		t.Fatal(err)
	}
	if third.Preset != "vp9" {
		t.Fatalf("expected retry after backoff to discover VP9 preset, got %q", third.Preset)
	}
	if probeCalls != 2 {
		t.Fatalf("expected probe retry after backoff, got %d calls", probeCalls)
	}
}

func TestBuildVODDownloadArgsUsesPresetCodecs(t *testing.T) {
	tests := []struct {
		name   string
		preset VODDownloadPreset
		want   []string
	}{
		{name: "original", preset: VODDownloadPresetOriginal, want: []string{"-c", "copy"}},
		{name: "h264", preset: VODDownloadPresetH264, want: []string{"libx264", "medium", "23", "aac", "128k", "copy"}},
		{name: "hevc", preset: VODDownloadPresetHEVC, want: []string{"libx265", "medium", "25", "-x265-params", x265ThreadParams(availableCPUCount()), "aac", "128k", "copy"}},
		{name: "vp9", preset: VODDownloadPresetVP9, want: []string{"libvpx-vp9", "32", "0", "good", "2", "-threads", fmt.Sprintf("%d", vpxThreadCount(availableCPUCount())), "-row-mt", "1", "libopus", "128k", "copy"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := buildVODDownloadArgs("input", "output.mkv", "title", "channel", tt.preset)
			for _, want := range append([]string{"0:v?", "0:a?", "0:s?", "matroska", "output.mkv"}, tt.want...) {
				if !slices.Contains(args, want) {
					t.Fatalf("expected %s args to contain %q, got %#v", tt.name, want, args)
				}
			}
		})
	}
}

func TestX265ThreadParamsScalesWithAvailableCPUs(t *testing.T) {
	tests := []struct {
		cpus int
		want string
	}{
		{cpus: 0, want: "pools=1:frame-threads=1"},
		{cpus: 1, want: "pools=1:frame-threads=1"},
		{cpus: 8, want: "pools=8:frame-threads=2"},
		{cpus: 24, want: "pools=24:frame-threads=6"},
		{cpus: 64, want: "pools=64:frame-threads=6"},
	}

	for _, tt := range tests {
		if got := x265ThreadParams(tt.cpus); got != tt.want {
			t.Errorf("x265ThreadParams(%d) = %q, want %q", tt.cpus, got, tt.want)
		}
	}
}

func TestVPXThreadCountUsesAvailableCPUsUpToRecommendedLimit(t *testing.T) {
	tests := []struct {
		cpus int
		want int
	}{
		{cpus: 0, want: 1},
		{cpus: 1, want: 1},
		{cpus: 8, want: 8},
		{cpus: 16, want: 16},
		{cpus: 24, want: 16},
	}

	for _, tt := range tests {
		if got := vpxThreadCount(tt.cpus); got != tt.want {
			t.Errorf("vpxThreadCount(%d) = %d, want %d", tt.cpus, got, tt.want)
		}
	}
}

func TestEffectiveCPUCountRespectsCgroupQuota(t *testing.T) {
	tests := []struct {
		name    string
		logical int
		cpuMax  string
		want    int
	}{
		{name: "unlimited", logical: 8, cpuMax: "max 100000", want: 8},
		{name: "one CPU quota", logical: 8, cpuMax: "100000 100000", want: 1},
		{name: "fractional quota rounds up", logical: 8, cpuMax: "150000 100000", want: 2},
		{name: "quota cannot exceed affinity", logical: 4, cpuMax: "800000 100000", want: 4},
		{name: "invalid data", logical: 8, cpuMax: "invalid", want: 8},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := effectiveCPUCount(tt.logical, tt.cpuMax); got != tt.want {
				t.Errorf("effectiveCPUCount(%d, %q) = %d, want %d", tt.logical, tt.cpuMax, got, tt.want)
			}
		})
	}
}

func TestVODPresetCommandsContainResolvedThreadSettings(t *testing.T) {
	commands := VODPresetCommands()
	if !strings.Contains(commands["hevc"], "-x265-params "+x265ThreadParams(availableCPUCount())) {
		t.Fatalf("HEVC command does not contain resolved x265 threading: %q", commands["hevc"])
	}
	if !strings.Contains(commands["vp9"], fmt.Sprintf("-threads %d -row-mt 1", vpxThreadCount(availableCPUCount()))) {
		t.Fatalf("VP9 command does not contain resolved libvpx threading: %q", commands["vp9"])
	}
}

func TestBuildVODConversionArgsIncludesSourceMetadataAndOriginalSize(t *testing.T) {
	args := buildVODConversionArgs("original.mkv", "converted.part", VODDownloadPresetHEVC, 987654)
	for _, want := range []string{
		"original.mkv", "converted.part", "libx265", "0:v?", "0:a?", "0:s?",
		"jellych_download_preset=hevc", "jellych_original_size=987654", "matroska",
	} {
		if !slices.Contains(args, want) {
			t.Fatalf("expected conversion args to contain %q, got %#v", want, args)
		}
	}
}

func TestFinalizeVODConversionReplacesOriginalAndRemovesBackup(t *testing.T) {
	dir := t.TempDir()
	originalPath := filepath.Join(dir, "123.mkv")
	tempPath := filepath.Join(dir, "123.part")
	backupPath := filepath.Join(dir, "123.original")
	if err := os.WriteFile(originalPath, []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tempPath, []byte("converted"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := finalizeVODConversion(tempPath, originalPath, backupPath); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(originalPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "converted" {
		t.Fatalf("expected converted output, got %q", got)
	}
	if _, err := os.Stat(backupPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected backup removal, got %v", err)
	}
}

func TestRecoverInterruptedConversionRestoresOriginal(t *testing.T) {
	dir := t.TempDir()
	partialDir := vodPartialDir(dir)
	if err := os.MkdirAll(partialDir, 0755); err != nil {
		t.Fatal(err)
	}
	downloader := &VODDownloader{dir: dir, retention: defaultVODDownloadRetention}
	backupPath := vodConversionBackupPath(dir, "123")
	if err := os.WriteFile(backupPath, []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}
	changed, err := downloader.recoverInterruptedConversion(dir, "123")
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected interrupted conversion recovery")
	}
	got, err := os.ReadFile(vodDownloadPath(dir, "123"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "original" {
		t.Fatalf("expected restored original, got %q", got)
	}
}

func TestDeleteDoesNotRemoveOnlyConversionBackup(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(vodPartialDir(dir), 0755); err != nil {
		t.Fatal(err)
	}
	backupPath := vodConversionBackupPath(dir, "123")
	if err := os.WriteFile(backupPath, []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}
	downloader := &VODDownloader{dir: dir}
	if err := downloader.Delete("123"); !errors.Is(err, ErrVODDownloadNotFound) {
		t.Fatalf("expected missing primary error, got %v", err)
	}
	if _, err := os.Stat(backupPath); err != nil {
		t.Fatalf("expected recovery backup to remain, got %v", err)
	}
}

func TestConvertRejectsNonOriginalAndOriginalTarget(t *testing.T) {
	dir := t.TempDir()
	path := vodDownloadPath(dir, "123")
	if err := os.WriteFile(path, []byte("downloaded"), 0644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	downloader := &VODDownloader{
		dir: dir,
		presets: map[string]vodPresetCacheEntry{
			"123": {size: info.Size(), modTime: info.ModTime(), metadata: vodDownloadMetadata{Preset: VODDownloadPresetHEVC}},
		},
	}
	if err := downloader.Convert(context.Background(), "123", VODDownloadPresetVP9); !errors.Is(err, ErrVODConversionRequiresOriginal) {
		t.Fatalf("expected original-only error, got %v", err)
	}
	if err := downloader.Convert(context.Background(), "123", VODDownloadPresetOriginal); !errors.Is(err, ErrVODConversionTargetOriginal) {
		t.Fatalf("expected compressed-target error, got %v", err)
	}
}

func TestParseVODDownloadPreset(t *testing.T) {
	for _, value := range []string{"", "original", "h264", "hevc", "vp9", " VP9 "} {
		if _, err := ParseVODDownloadPreset(value); err != nil {
			t.Fatalf("expected %q to be valid: %v", value, err)
		}
	}
	if _, err := ParseVODDownloadPreset("custom"); err == nil {
		t.Fatal("expected unknown preset to be rejected")
	}
}

func TestUpdateVODDownloadProgressCalculatesByteRate(t *testing.T) {
	download := newVODDownload()
	vodDownloadState.Lock()
	vodDownloadState.active = map[string]*vodDownload{"active": download}
	vodDownloadState.Unlock()
	t.Cleanup(func() { clearVODDownload("active", download) })

	download.lastTotalSize = 1000
	download.lastTotalSizeUpdate = time.Now().Add(-time.Second)
	updateVODDownloadProgress("active", download, "total_size", "12345")
	updateVODDownloadProgress("active", download, "speed", "1.5x")

	got, err := GetVODDownloadProgress("active")
	if err != nil {
		t.Fatalf("expected progress lookup to succeed, got %v", err)
	}
	if !got.Active {
		t.Fatal("expected active progress")
	}
	if got.TotalSize != 12345 {
		t.Fatalf("expected total size to be recorded, got %d", got.TotalSize)
	}
	if got.BytesPerSecond <= 0 {
		t.Fatalf("expected byte rate to be recorded, got %d", got.BytesPerSecond)
	}
	if got.Speed != "1.5x" {
		t.Fatalf("expected speed to be recorded, got %q", got.Speed)
	}
}

func TestUpdateVODDownloadProgressCalculatesETA(t *testing.T) {
	for _, operation := range []string{"download", "convert"} {
		t.Run(operation, func(t *testing.T) {
			download := newVODDownload()
			download.progress.Operation = operation
			download.totalDuration = 100 * time.Second
			vodDownloadState.Lock()
			vodDownloadState.active = map[string]*vodDownload{"active": download}
			vodDownloadState.Unlock()
			t.Cleanup(func() { clearVODDownload("active", download) })

			updateVODDownloadProgress("active", download, "out_time_us", "25000000")
			updateVODDownloadProgress("active", download, "speed", "0.5x")
			got, err := GetVODDownloadProgress("active")
			if err != nil {
				t.Fatal(err)
			}
			if got.ETASeconds != 150 {
				t.Fatalf("expected 150 seconds remaining, got %d", got.ETASeconds)
			}
		})
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
