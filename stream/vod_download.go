package stream

import (
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

var vodDownloadIDRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

var vodDownloadState = struct {
	sync.Mutex
	dir    string
	active map[string]*exec.Cmd
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

// StartVODDownload starts streamlink in the background and saves the VOD to disk.
func StartVODDownload(id, vodURL string) error {
	id = strings.TrimSpace(id)
	vodURL = strings.TrimSpace(vodURL)
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
		vodDownloadState.active = make(map[string]*exec.Cmd)
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
	cmd := exec.Command("streamlink", "--output", outputPath, vodURL, "best")
	vodDownloadState.active[id] = cmd
	vodDownloadState.Unlock()

	if err := os.MkdirAll(dir, 0755); err != nil {
		clearVODDownload(id, cmd)
		return fmt.Errorf("failed to create vods folder: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		clearVODDownload(id, cmd)
		return fmt.Errorf("failed to get streamlink stderr pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		clearVODDownload(id, cmd)
		return fmt.Errorf("failed to get streamlink stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		clearVODDownload(id, cmd)
		return fmt.Errorf("failed to start streamlink: %w", err)
	}

	startedAt := time.Now()
	go logCommandOutput("streamlink-vod", id, stderr)
	go logCommandOutput("streamlink-vod", id, stdout)
	go func() {
		err := cmd.Wait()
		clearVODDownload(id, cmd)
		if err != nil {
			slog.Error("vod download exited", "id", id, "output", outputPath, "elapsed", time.Since(startedAt), "error", err)
			return
		}
		slog.Info("vod download finished", "id", id, "output", outputPath, "elapsed", time.Since(startedAt))
	}()

	slog.Info("vod download started", "id", id, "output", outputPath, "pid", cmd.Process.Pid)
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

func clearVODDownload(id string, cmd *exec.Cmd) {
	vodDownloadState.Lock()
	defer vodDownloadState.Unlock()
	if vodDownloadState.active != nil && vodDownloadState.active[id] == cmd {
		delete(vodDownloadState.active, id)
	}
}

func vodDownloadPath(dir, id string) string {
	return filepath.Join(dir, id+".mp4")
}
