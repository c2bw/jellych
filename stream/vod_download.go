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

var vodDownloadIDRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type vodDownload struct{}

var vodDownloadState = struct {
	sync.Mutex
	dir    string
	active map[string]*vodDownload
}{}

// SetVODDownloadDir configures the folder used by manual VOD downloads.
func SetVODDownloadDir(dir string) {
	vodDownloadState.Lock()
	defer vodDownloadState.Unlock()
	vodDownloadState.dir = strings.TrimSpace(dir)
}

// VODDownloadExists reports whether a finished VOD download exists on disk.
func VODDownloadExists(id string) (bool, error) {
	id = strings.TrimSpace(id)
	if !vodDownloadIDRE.MatchString(id) {
		return false, fmt.Errorf("invalid vod id")
	}

	vodDownloadState.Lock()
	dir := vodDownloadState.dir
	if dir == "" {
		vodDownloadState.Unlock()
		return false, ErrVODDownloadsDisabled
	}
	if vodDownloadState.active != nil {
		if _, ok := vodDownloadState.active[id]; ok {
			vodDownloadState.Unlock()
			return false, nil
		}
	}
	outputPath := vodDownloadPath(dir, id)
	vodDownloadState.Unlock()

	info, err := os.Stat(outputPath)
	if err == nil {
		return !info.IsDir(), nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, fmt.Errorf("failed to check vod output file: %w", err)
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
		clearVODDownload(id, download)
		if err != nil {
			_ = os.Remove(outputPath)
			slog.Error("vod download exited", "id", id, "output", outputPath, "elapsed", time.Since(startedAt), "error", err)
			return
		}
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
