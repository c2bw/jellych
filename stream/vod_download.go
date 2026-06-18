package stream

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
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

type vodDownload struct {
	progress            VODDownloadProgress
	lastTotalSize       int64
	lastTotalSizeUpdate time.Time
}

// VODDownloadProgress is a snapshot of an active or completed VOD download.
type VODDownloadProgress struct {
	Active         bool      `json:"active"`
	Downloaded     bool      `json:"downloaded"`
	Progress       string    `json:"progress,omitempty"`
	TotalSize      int64     `json:"totalSize,omitempty"`
	BytesPerSecond int64     `json:"bytesPerSecond,omitempty"`
	Speed          string    `json:"speed,omitempty"`
	StartedAt      time.Time `json:"startedAt,omitempty,omitzero"`
	UpdatedAt      time.Time `json:"updatedAt,omitempty,omitzero"`
}

type VODDownloader struct {
	sync.Mutex
	dir       string
	retention time.Duration
	active    map[string]*vodDownload
}

var vodDownloadState = &VODDownloader{retention: defaultVODDownloadRetention}

// SetVODDownloadDir configures the folder used by manual VOD downloads.
func SetVODDownloadDir(dir string) {
	vodDownloadState.SetDir(dir)
}

// SetDir configures the folder used by manual VOD downloads.
func (d *VODDownloader) SetDir(dir string) {
	d.Lock()
	defer d.Unlock()
	d.dir = strings.TrimSpace(dir)
}

// SetVODDownloadRetention configures how long completed VOD downloads are kept.
func SetVODDownloadRetention(retention time.Duration) {
	vodDownloadState.SetRetention(retention)
}

// SetRetention configures how long completed VOD downloads are kept.
func (d *VODDownloader) SetRetention(retention time.Duration) {
	d.Lock()
	defer d.Unlock()
	d.retention = retention
}

// StartVODDownloadCleanup removes expired downloads immediately and once per hour.
func StartVODDownloadCleanup(ctx context.Context) {
	vodDownloadState.StartCleanup(ctx)
}

// StartCleanup removes expired downloads immediately and once per hour.
func (d *VODDownloader) StartCleanup(ctx context.Context) {
	d.runCleanup(time.Now())

	go func() {
		ticker := time.NewTicker(vodDownloadCleanupInterval)
		defer ticker.Stop()
		for {
			select {
			case now := <-ticker.C:
				d.runCleanup(now)
			case <-ctx.Done():
				return
			}
		}
	}()
}

// VODDownloadExists reports whether a finished VOD download exists on disk.
func VODDownloadExists(id string) (bool, error) {
	exists, _, err := vodDownloadState.Status(id)
	return exists, err
}

// VODDownloadStatus reports whether a completed download exists and when it
// becomes eligible for retention cleanup.
func VODDownloadStatus(id string) (bool, time.Time, error) {
	return vodDownloadState.Status(id)
}

// Status reports whether a completed download exists and when it becomes
// eligible for retention cleanup.
func (d *VODDownloader) Status(id string) (bool, time.Time, error) {
	id = strings.TrimSpace(id)
	if !vodDownloadIDRE.MatchString(id) {
		return false, time.Time{}, fmt.Errorf("invalid vod id")
	}

	d.Lock()
	dir := d.dir
	retention := d.retention
	if dir == "" {
		d.Unlock()
		return false, time.Time{}, ErrVODDownloadsDisabled
	}
	if d.active != nil {
		if _, ok := d.active[id]; ok {
			d.Unlock()
			return false, time.Time{}, nil
		}
	}
	outputPath := vodDownloadPath(dir, id)
	d.Unlock()

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

// GetVODDownloadProgress returns the current progress for an active download,
// or the completed file status when no download is running.
func GetVODDownloadProgress(id string) (VODDownloadProgress, error) {
	return vodDownloadState.Progress(id)
}

// Progress returns the current progress for an active download, or completed
// file status when no download is running.
func (d *VODDownloader) Progress(id string) (VODDownloadProgress, error) {
	id = strings.TrimSpace(id)
	if !vodDownloadIDRE.MatchString(id) {
		return VODDownloadProgress{}, fmt.Errorf("invalid vod id")
	}

	d.Lock()
	if d.active != nil {
		if download, ok := d.active[id]; ok && download != nil {
			progress := download.progress
			progress.Active = true
			d.Unlock()
			return progress, nil
		}
	}
	d.Unlock()

	downloaded, size, err := d.completedFile(id)
	if err != nil {
		return VODDownloadProgress{}, err
	}
	return VODDownloadProgress{Downloaded: downloaded, TotalSize: size}, nil
}

// StartVODDownload resolves a Twitch VOD and downloads it to a tagged MKV.
func StartVODDownload(ctx context.Context, id, vodURL, title, channel string) error {
	return vodDownloadState.Start(ctx, id, vodURL, title, channel)
}

// Start resolves a Twitch VOD and downloads it to a tagged MKV.
func (d *VODDownloader) Start(ctx context.Context, id, vodURL, title, channel string) error {
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

	d.Lock()
	dir := d.dir
	if dir == "" {
		d.Unlock()
		return ErrVODDownloadsDisabled
	}
	if d.active == nil {
		d.active = make(map[string]*vodDownload)
	}
	if _, ok := d.active[id]; ok {
		d.Unlock()
		return ErrVODDownloadAlreadyStarted
	}
	outputPath := vodDownloadPath(dir, id)
	if _, err := os.Stat(outputPath); err == nil {
		d.Unlock()
		return ErrVODDownloadAlreadyExists
	} else if !errors.Is(err, os.ErrNotExist) {
		d.Unlock()
		return fmt.Errorf("failed to check vod output file: %w", err)
	}
	download := newVODDownload()
	d.active[id] = download
	d.Unlock()

	if err := os.MkdirAll(dir, 0755); err != nil {
		d.clear(id, download)
		return fmt.Errorf("failed to create vods folder: %w", err)
	}

	inputURL, err := resolveVODDownloadURL(ctx, vodURL)
	if err != nil {
		d.clear(id, download)
		return err
	}

	cmd := exec.Command("ffmpeg", buildVODDownloadArgs(inputURL, outputPath, title, channel)...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		d.clear(id, download)
		return fmt.Errorf("failed to get ffmpeg progress pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		d.clear(id, download)
		return fmt.Errorf("failed to get ffmpeg stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		_ = os.Remove(outputPath)
		d.clear(id, download)
		return fmt.Errorf("failed to start ffmpeg: %w", err)
	}

	startedAt := time.Now()
	d.setProgressState(id, download, "running")
	go d.readProgress(id, download, stdout)
	go logCommandOutput("ffmpeg-vod", id, stderr)
	go func() {
		err := cmd.Wait()
		if err != nil {
			d.clear(id, download)
			_ = os.Remove(outputPath)
			slog.Error("vod download exited", "id", id, "output", outputPath, "elapsed", time.Since(startedAt), "error", err)
			return
		}
		completedAt := time.Now()
		if err := os.Chtimes(outputPath, completedAt, completedAt); err != nil {
			slog.Error("failed to record vod download completion time", "id", id, "output", outputPath, "error", err)
		}
		d.clear(id, download)
		slog.Info("vod download finished", "id", id, "output", outputPath, "elapsed", time.Since(startedAt))
	}()

	slog.Info("vod download started", "id", id, "output", outputPath, "ffmpeg_pid", cmd.Process.Pid)
	return nil
}

// DeleteVODDownload removes a previously downloaded VOD file from disk.
func DeleteVODDownload(id string) error {
	return vodDownloadState.Delete(id)
}

// Delete removes a previously downloaded VOD file from disk.
func (d *VODDownloader) Delete(id string) error {
	id = strings.TrimSpace(id)
	if !vodDownloadIDRE.MatchString(id) {
		return fmt.Errorf("invalid vod id")
	}

	d.Lock()
	defer d.Unlock()
	dir := d.dir
	if dir == "" {
		return ErrVODDownloadsDisabled
	}
	if d.active != nil {
		if _, ok := d.active[id]; ok {
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
	vodDownloadState.clear(id, download)
}

func (d *VODDownloader) clear(id string, download *vodDownload) {
	d.Lock()
	defer d.Unlock()
	if d.active != nil && d.active[id] == download {
		delete(d.active, id)
	}
}

func newVODDownload() *vodDownload {
	now := time.Now()
	progress := VODDownloadProgress{
		Active:    true,
		Progress:  "resolving",
		StartedAt: now,
		UpdatedAt: now,
	}
	return &vodDownload{progress: progress}
}

func (d *VODDownloader) setProgressState(id string, download *vodDownload, state string) {
	d.updateProgress(id, download, "progress", state)
}

func (d *VODDownloader) readProgress(id string, download *vodDownload, r io.Reader) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			slog.Debug("ffmpeg-vod progress", "id", id, "line", line)
			continue
		}
		d.updateProgress(id, download, strings.TrimSpace(key), strings.TrimSpace(value))
	}
	if err := scanner.Err(); err != nil {
		slog.Debug("ffmpeg-vod progress stream ended with error", "id", id, "error", err)
	}
}

func updateVODDownloadProgress(id string, download *vodDownload, key, value string) {
	vodDownloadState.updateProgress(id, download, key, value)
}

func (d *VODDownloader) updateProgress(id string, download *vodDownload, key, value string) {
	d.Lock()
	defer d.Unlock()
	if d.active == nil || d.active[id] != download {
		return
	}

	progress := &download.progress
	progress.Active = true
	progress.UpdatedAt = time.Now()

	switch key {
	case "total_size":
		if totalSize, err := strconv.ParseInt(value, 10, 64); err == nil && totalSize >= 0 {
			updateVODDownloadByteRate(download, totalSize, progress.UpdatedAt)
			progress.TotalSize = totalSize
		}
	case "speed":
		progress.Speed = value
	case "progress":
		progress.Progress = value
	}
}

func updateVODDownloadByteRate(download *vodDownload, totalSize int64, updatedAt time.Time) {
	if download.lastTotalSizeUpdate.IsZero() {
		download.lastTotalSize = totalSize
		download.lastTotalSizeUpdate = updatedAt
		return
	}

	elapsed := updatedAt.Sub(download.lastTotalSizeUpdate)
	delta := totalSize - download.lastTotalSize
	if elapsed > 0 && delta >= 0 {
		download.progress.BytesPerSecond = int64(float64(delta) / elapsed.Seconds())
	}
	download.lastTotalSize = totalSize
	download.lastTotalSizeUpdate = updatedAt
}

func vodDownloadPath(dir, id string) string {
	return filepath.Join(dir, id+".mkv")
}

func (d *VODDownloader) completedFile(id string) (bool, int64, error) {
	id = strings.TrimSpace(id)
	if !vodDownloadIDRE.MatchString(id) {
		return false, 0, fmt.Errorf("invalid vod id")
	}

	d.Lock()
	dir := d.dir
	if dir == "" {
		d.Unlock()
		return false, 0, ErrVODDownloadsDisabled
	}
	if d.active != nil {
		if _, ok := d.active[id]; ok {
			d.Unlock()
			return false, 0, nil
		}
	}
	outputPath := vodDownloadPath(dir, id)
	d.Unlock()

	info, err := os.Stat(outputPath)
	if err == nil {
		if !info.Mode().IsRegular() {
			return false, 0, nil
		}
		return true, info.Size(), nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, 0, nil
	}
	return false, 0, fmt.Errorf("failed to check vod output file: %w", err)
}

func (d *VODDownloader) runCleanup(now time.Time) {
	deleted, err := d.cleanupExpired(now)
	if err != nil {
		slog.Error("failed to clean up expired vod downloads", "error", err)
	}
	if deleted > 0 {
		slog.Info("cleaned up expired vod downloads", "deleted", deleted)
	}
}

func cleanupExpiredVODDownloads(now time.Time) (int, error) {
	return vodDownloadState.cleanupExpired(now)
}

func (d *VODDownloader) cleanupExpired(now time.Time) (int, error) {
	d.Lock()
	dir := d.dir
	retention := d.retention
	d.Unlock()

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
		removed, err := d.removeExpired(dir, id, path)
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
	return vodDownloadState.removeExpired(dir, id, path)
}

func (d *VODDownloader) removeExpired(dir, id, path string) (bool, error) {
	d.Lock()
	defer d.Unlock()

	if d.dir != dir {
		return false, nil
	}
	if d.active != nil {
		if _, ok := d.active[id]; ok {
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
		"-nostats",
		"-progress", "pipe:1",
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
