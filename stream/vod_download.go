package stream

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

var ErrVODDownloadsDisabled = errors.New("vod downloads folder not configured")
var ErrVODDownloadAlreadyStarted = errors.New("vod download already started")
var ErrVODDownloadAlreadyExists = errors.New("vod download already exists")
var ErrVODDownloadNotFound = errors.New("vod download not found")

const vodURLResolutionTimeout = 20 * time.Second
const vodDownloadCleanupInterval = time.Hour
const defaultVODDownloadRetention = 30 * 24 * time.Hour

var vodDownloadIDRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type vodDownload struct{}

var vodDownloadState = struct {
	sync.Mutex
	dir       string
	retention time.Duration
	active    map[string]*vodDownload
}{
	retention: defaultVODDownloadRetention,
}

// SetVODDownloadDir configures the folder used by manual VOD downloads.
func SetVODDownloadDir(dir string) {
	vodDownloadState.Lock()
	defer vodDownloadState.Unlock()
	vodDownloadState.dir = strings.TrimSpace(dir)
}

// SetVODDownloadRetention configures how long completed VOD downloads are kept.
func SetVODDownloadRetention(retention time.Duration) {
	vodDownloadState.Lock()
	defer vodDownloadState.Unlock()
	vodDownloadState.retention = retention
}

// StartVODDownloadCleanup removes expired downloads immediately and once per hour.
func StartVODDownloadCleanup(ctx context.Context) {
	runVODDownloadCleanup(time.Now())

	go func() {
		ticker := time.NewTicker(vodDownloadCleanupInterval)
		defer ticker.Stop()
		for {
			select {
			case now := <-ticker.C:
				runVODDownloadCleanup(now)
			case <-ctx.Done():
				return
			}
		}
	}()
}

// VODDownloadExists reports whether a finished VOD download exists on disk.
func VODDownloadExists(id string) (bool, error) {
	exists, _, err := VODDownloadStatus(id)
	return exists, err
}

// VODDownloadStatus reports whether a completed download exists and when it
// becomes eligible for retention cleanup.
func VODDownloadStatus(id string) (bool, time.Time, error) {
	id = strings.TrimSpace(id)
	if !vodDownloadIDRE.MatchString(id) {
		return false, time.Time{}, fmt.Errorf("invalid vod id")
	}

	vodDownloadState.Lock()
	dir := vodDownloadState.dir
	retention := vodDownloadState.retention
	if dir == "" {
		vodDownloadState.Unlock()
		return false, time.Time{}, ErrVODDownloadsDisabled
	}
	if vodDownloadState.active != nil {
		if _, ok := vodDownloadState.active[id]; ok {
			vodDownloadState.Unlock()
			return false, time.Time{}, nil
		}
	}
	outputPath := vodDownloadPath(dir, id)
	vodDownloadState.Unlock()

	info, err := os.Stat(outputPath)
	if err == nil {
		if !info.Mode().IsRegular() {
			return false, time.Time{}, nil
		}
		return true, info.ModTime().Add(retention), nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, time.Time{}, nil
	}
	return false, time.Time{}, fmt.Errorf("failed to check vod output file: %w", err)
}

// StartVODDownload resolves a Twitch VOD and downloads it to a tagged MKV.
func StartVODDownload(ctx context.Context, id, vodURL, title, channel string) error {
	id = strings.TrimSpace(id)
	vodURL = strings.TrimSpace(vodURL)
	title = strings.TrimSpace(title)
	channel = strings.TrimSpace(channel)
	if !vodDownloadIDRE.MatchString(id) {
		return fmt.Errorf("invalid vod id")
	}
	if vodURL == "" {
		return fmt.Errorf("vod url is required")
	}

	vodDownloadState.Lock()
	dir := vodDownloadState.dir
	if dir == "" {
		vodDownloadState.Unlock()
		return ErrVODDownloadsDisabled
	}
	if vodDownloadState.active == nil {
		vodDownloadState.active = make(map[string]*vodDownload)
	}
	if _, ok := vodDownloadState.active[id]; ok {
		vodDownloadState.Unlock()
		return ErrVODDownloadAlreadyStarted
	}
	outputPath := vodDownloadPath(dir, id)
	if _, err := os.Stat(outputPath); err == nil {
		vodDownloadState.Unlock()
		return ErrVODDownloadAlreadyExists
	} else if !errors.Is(err, os.ErrNotExist) {
		vodDownloadState.Unlock()
		return fmt.Errorf("failed to check vod output file: %w", err)
	}
	download := &vodDownload{}
	vodDownloadState.active[id] = download
	vodDownloadState.Unlock()

	if err := os.MkdirAll(dir, 0755); err != nil {
		clearVODDownload(id, download)
		return fmt.Errorf("failed to create vods folder: %w", err)
	}

	inputURL, err := resolveVODDownloadURL(ctx, vodURL)
	if err != nil {
		clearVODDownload(id, download)
		return err
	}

	cmd := exec.Command("ffmpeg", buildVODDownloadArgs(inputURL, outputPath, title, channel)...)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		clearVODDownload(id, download)
		return fmt.Errorf("failed to get ffmpeg stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		_ = os.Remove(outputPath)
		clearVODDownload(id, download)
		return fmt.Errorf("failed to start ffmpeg: %w", err)
	}

	startedAt := time.Now()
	go logCommandOutput("ffmpeg-vod", id, stderr)
	go func() {
		err := cmd.Wait()
		if err != nil {
			clearVODDownload(id, download)
			_ = os.Remove(outputPath)
			slog.Error("vod download exited", "id", id, "output", outputPath, "elapsed", time.Since(startedAt), "error", err)
			return
		}
		completedAt := time.Now()
		if err := os.Chtimes(outputPath, completedAt, completedAt); err != nil {
			slog.Error("failed to record vod download completion time", "id", id, "output", outputPath, "error", err)
		}
		clearVODDownload(id, download)
		slog.Info("vod download finished", "id", id, "output", outputPath, "elapsed", time.Since(startedAt))
	}()

	slog.Info("vod download started", "id", id, "output", outputPath, "ffmpeg_pid", cmd.Process.Pid)
	return nil
}

// DeleteVODDownload removes a previously downloaded VOD file from disk.
func DeleteVODDownload(id string) error {
	id = strings.TrimSpace(id)
	if !vodDownloadIDRE.MatchString(id) {
		return fmt.Errorf("invalid vod id")
	}

	vodDownloadState.Lock()
	defer vodDownloadState.Unlock()
	dir := vodDownloadState.dir
	if dir == "" {
		return ErrVODDownloadsDisabled
	}
	if vodDownloadState.active != nil {
		if _, ok := vodDownloadState.active[id]; ok {
			return ErrVODDownloadAlreadyStarted
		}
	}

	outputPath := vodDownloadPath(dir, id)
	if err := os.Remove(outputPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrVODDownloadNotFound
		}
		return fmt.Errorf("failed to delete vod download: %w", err)
	}
	slog.Info("vod download deleted", "id", id, "output", outputPath)
	return nil
}

func clearVODDownload(id string, download *vodDownload) {
	vodDownloadState.Lock()
	defer vodDownloadState.Unlock()
	if vodDownloadState.active != nil && vodDownloadState.active[id] == download {
		delete(vodDownloadState.active, id)
	}
}

func vodDownloadPath(dir, id string) string {
	return filepath.Join(dir, id+".mkv")
}

func runVODDownloadCleanup(now time.Time) {
	deleted, err := cleanupExpiredVODDownloads(now)
	if err != nil {
		slog.Error("failed to clean up expired vod downloads", "error", err)
	}
	if deleted > 0 {
		slog.Info("cleaned up expired vod downloads", "deleted", deleted)
	}
}

func cleanupExpiredVODDownloads(now time.Time) (int, error) {
	vodDownloadState.Lock()
	dir := vodDownloadState.dir
	retention := vodDownloadState.retention
	vodDownloadState.Unlock()

	if dir == "" {
		return 0, nil
	}

	dirInfo, err := os.Stat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("failed to inspect vod downloads folder: %w", err)
	}
	if !dirInfo.IsDir() {
		return 0, fmt.Errorf("vod downloads path is not a directory: %s", dir)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, fmt.Errorf("failed to read vod downloads folder: %w", err)
	}

	cutoff := now.Add(-retention)
	deleted := 0
	var cleanupErrs []error
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if filepath.Ext(name) != ".mkv" {
			continue
		}
		id := strings.TrimSuffix(name, ".mkv")
		if !vodDownloadIDRE.MatchString(id) {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("failed to inspect %q: %w", name, err))
			continue
		}
		if !info.Mode().IsRegular() {
			continue
		}
		if !info.ModTime().Before(cutoff) {
			continue
		}

		path := filepath.Join(dir, name)
		removed, err := removeExpiredVODDownload(dir, id, path)
		if err != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("failed to delete %q: %w", name, err))
			continue
		}
		if !removed {
			continue
		}
		deleted++
		slog.Info("expired vod download deleted", "id", id, "output", path, "age", now.Sub(info.ModTime()))
	}
	return deleted, errors.Join(cleanupErrs...)
}

func removeExpiredVODDownload(dir, id, path string) (bool, error) {
	vodDownloadState.Lock()
	defer vodDownloadState.Unlock()

	if vodDownloadState.dir != dir {
		return false, nil
	}
	if vodDownloadState.active != nil {
		if _, ok := vodDownloadState.active[id]; ok {
			return false, nil
		}
	}
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func resolveVODDownloadURL(ctx context.Context, vodURL string) (string, error) {
	return resolveVODDownloadURLWithTimeout(ctx, vodURL, vodURLResolutionTimeout, resolveVODStreamURL)
}

func resolveVODDownloadURLWithTimeout(
	ctx context.Context,
	vodURL string,
	timeout time.Duration,
	resolve func(context.Context, string) (string, error),
) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	inputURL, err := resolve(ctx, vodURL)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", fmt.Errorf("timed out resolving VOD stream URL: %w", context.DeadlineExceeded)
		}
		return "", err
	}
	return inputURL, nil
}

func buildVODDownloadArgs(inputURL, outputPath, title, channel string) []string {
	args := []string{
		"-hide_banner",
		"-loglevel", "warning",
		"-nostdin",
		"-i", inputURL,
		"-map", "0:v?",
		"-map", "0:a?",
		"-map", "0:s?",
		"-c", "copy",
	}
	if title != "" {
		args = append(args, "-metadata", "title="+title)
	}
	if channel != "" {
		args = append(args, "-metadata", "artist="+channel)
	}
	return append(args, "-f", "matroska", outputPath)
}
