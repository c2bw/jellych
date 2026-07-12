package stream

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// StartVODDownload resolves a Twitch VOD and downloads it without transcoding.

func StartVODDownload(ctx context.Context, id, vodURL, title, channel string) error {
	return StartVODDownloadWithPreset(ctx, id, vodURL, title, channel, VODDownloadPresetOriginal)
}

// StartVODDownloadWithPreset resolves a Twitch VOD and downloads it using preset.
func StartVODDownloadWithPreset(ctx context.Context, id, vodURL, title, channel string, preset VODDownloadPreset) error {
	return StartVODDownloadWithPresetAndDuration(ctx, id, vodURL, title, channel, preset, 0)
}

// StartVODDownloadWithPresetAndDuration resolves a Twitch VOD and downloads it using preset.
func StartVODDownloadWithPresetAndDuration(ctx context.Context, id, vodURL, title, channel string, preset VODDownloadPreset, totalDuration time.Duration) error {
	return vodDownloadState.StartWithPresetAndDuration(ctx, id, vodURL, title, channel, preset, totalDuration)
}

// Start resolves a Twitch VOD and downloads it without transcoding.
func (d *VODDownloader) Start(ctx context.Context, id, vodURL, title, channel string) error {
	return d.StartWithPreset(ctx, id, vodURL, title, channel, VODDownloadPresetOriginal)
}

// StartWithPreset resolves a Twitch VOD and downloads it using preset.
func (d *VODDownloader) StartWithPreset(ctx context.Context, id, vodURL, title, channel string, preset VODDownloadPreset) error {
	return d.StartWithPresetAndDuration(ctx, id, vodURL, title, channel, preset, 0)
}

// StartWithPresetAndDuration resolves a Twitch VOD and downloads it using preset and known duration.
func (d *VODDownloader) StartWithPresetAndDuration(ctx context.Context, id, vodURL, title, channel string, preset VODDownloadPreset, totalDuration time.Duration) error {
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
	validatedPreset, err := ParseVODDownloadPreset(string(preset))
	if err != nil {
		return err
	}

	d.Lock()
	dir := d.dir
	if dir == "" {
		d.Unlock()
		return ErrVODDownloadsDisabled
	}
	if d.stopping {
		d.Unlock()
		return ErrVODDownloadsStopping
	}
	if d.active == nil {
		d.active = make(map[string]*vodDownload)
	}
	if _, ok := d.removing[id]; ok {
		d.Unlock()
		return ErrVODRemovalInProgress
	}
	if _, ok := d.active[id]; ok {
		d.Unlock()
		return ErrVODDownloadAlreadyStarted
	}
	outputPath := vodDownloadPath(dir, id)
	if info, err := os.Stat(outputPath); err == nil {
		if info.Mode().IsRegular() && info.Size() == 0 {
			if err := os.Remove(outputPath); err != nil {
				d.Unlock()
				return fmt.Errorf("failed to remove empty vod output file: %w", err)
			}
		} else {
			d.Unlock()
			return ErrVODDownloadAlreadyExists
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		d.Unlock()
		return fmt.Errorf("failed to check vod output file: %w", err)
	}
	downloadCtx, cancel := context.WithCancel(ctx)
	download := newVODDownload()
	download.progress.Preset = string(validatedPreset)
	download.progress.Operation = "download"
	download.totalDuration = totalDuration
	download.cancel = cancel
	d.active[id] = download
	d.Unlock()

	if err := os.MkdirAll(vodPartialDir(dir), 0755); err != nil {
		d.finishDownload(id, download, "", outputPath, time.Now(), fmt.Errorf("failed to create vods folder: %w", err))
		return fmt.Errorf("failed to create vods folder: %w", err)
	}

	inputURL, err := resolveVODDownloadURL(downloadCtx, vodURL)
	if err != nil {
		d.finishDownload(id, download, "", outputPath, time.Now(), err)
		return err
	}
	if !d.prepareStart(id, download) {
		err := ErrVODDownloadsStopping
		d.finishDownload(id, download, "", outputPath, time.Now(), err)
		return err
	}

	tempPath, err := d.createPartialFile(id, download, dir)
	if err != nil {
		d.finishDownload(id, download, "", outputPath, time.Now(), err)
		return err
	}

	cmd := exec.Command("ffmpeg", buildVODDownloadArgs(inputURL, tempPath, title, channel, validatedPreset)...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		d.finishDownload(id, download, tempPath, outputPath, time.Now(), err)
		return fmt.Errorf("failed to get ffmpeg progress pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		d.finishDownload(id, download, tempPath, outputPath, time.Now(), err)
		return fmt.Errorf("failed to get ffmpeg stderr pipe: %w", err)
	}

	startedAt := time.Now()
	d.Lock()
	if d.stopping || download.stopping || d.active == nil || d.active[id] != download {
		d.Unlock()
		d.finishDownload(id, download, tempPath, outputPath, startedAt, context.Canceled)
		return context.Canceled
	}
	download.cmd = cmd
	download.tempPath = tempPath
	download.outputPath = outputPath
	download.startedAt = startedAt
	if err := cmd.Start(); err != nil {
		d.Unlock()
		d.finishDownload(id, download, tempPath, outputPath, startedAt, err)
		return fmt.Errorf("failed to start ffmpeg: %w", err)
	}
	d.Unlock()

	d.setProgressState(id, download, "running")
	go d.readProgress(id, download, stdout)
	go logCommandOutput("ffmpeg-vod", id, stderr)
	go func() {
		d.finishDownload(id, download, tempPath, outputPath, startedAt, cmd.Wait())
	}()

	slog.Info("vod download started", "id", id, "output", outputPath, "ffmpeg_pid", cmd.Process.Pid)
	return nil
}

// ConvertVODDownload converts an original downloaded VOD to a compressed preset.

func ConvertVODDownload(ctx context.Context, id string, preset VODDownloadPreset) error {
	return vodDownloadState.Convert(ctx, id, preset)
}

// Convert converts an original downloaded VOD to a compressed preset.
func (d *VODDownloader) Convert(ctx context.Context, id string, preset VODDownloadPreset) error {
	id = strings.TrimSpace(id)
	if !vodDownloadIDRE.MatchString(id) {
		return fmt.Errorf("invalid vod id")
	}
	validatedPreset, err := ParseVODDownloadPreset(string(preset))
	if err != nil {
		return err
	}
	if validatedPreset == VODDownloadPresetOriginal {
		return ErrVODConversionTargetOriginal
	}

	progress, err := d.Progress(id)
	if err != nil {
		return err
	}
	if progress.Active {
		return ErrVODDownloadAlreadyStarted
	}
	if !progress.Downloaded {
		return ErrVODDownloadNotFound
	}
	if progress.Preset != string(VODDownloadPresetOriginal) {
		return ErrVODConversionRequiresOriginal
	}

	d.Lock()
	dir := d.dir
	if dir == "" {
		d.Unlock()
		return ErrVODDownloadsDisabled
	}
	if d.stopping {
		d.Unlock()
		return ErrVODDownloadsStopping
	}
	if d.active == nil {
		d.active = make(map[string]*vodDownload)
	}
	if _, ok := d.removing[id]; ok {
		d.Unlock()
		return ErrVODRemovalInProgress
	}
	if _, ok := d.active[id]; ok {
		d.Unlock()
		return ErrVODDownloadAlreadyStarted
	}
	outputPath := vodDownloadPath(dir, id)
	info, statErr := os.Stat(outputPath)
	if statErr != nil {
		d.Unlock()
		if errors.Is(statErr, os.ErrNotExist) {
			return ErrVODDownloadNotFound
		}
		return fmt.Errorf("failed to inspect vod download: %w", statErr)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		d.Unlock()
		return ErrVODDownloadNotFound
	}
	if info.Size() != progress.TotalSize {
		d.Unlock()
		return fmt.Errorf("vod download changed during conversion setup")
	}
	_, cancel := context.WithCancel(ctx)
	download := newVODDownload()
	download.progress.Preset = string(validatedPreset)
	download.progress.Operation = "convert"
	download.progress.Downloaded = true
	download.progress.OriginalSize = info.Size()
	download.totalDuration = time.Duration(progress.DurationSeconds * float64(time.Second))
	download.cancel = cancel
	d.active[id] = download
	d.Unlock()

	if err := os.MkdirAll(vodPartialDir(dir), 0755); err != nil {
		d.finishConversion(id, download, "", outputPath, time.Now(), err)
		return fmt.Errorf("failed to create vod partial folder: %w", err)
	}
	tempPath, err := d.createPartialFile(id, download, dir)
	if err != nil {
		d.finishConversion(id, download, "", outputPath, time.Now(), err)
		return err
	}
	cmd := exec.Command("ffmpeg", buildVODConversionArgs(outputPath, tempPath, validatedPreset, info.Size())...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		d.finishConversion(id, download, tempPath, outputPath, time.Now(), err)
		return fmt.Errorf("failed to get ffmpeg conversion progress pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		d.finishConversion(id, download, tempPath, outputPath, time.Now(), err)
		return fmt.Errorf("failed to get ffmpeg conversion stderr pipe: %w", err)
	}

	startedAt := time.Now()
	d.Lock()
	if d.stopping || download.stopping || d.active == nil || d.active[id] != download {
		d.Unlock()
		d.finishConversion(id, download, tempPath, outputPath, startedAt, context.Canceled)
		return context.Canceled
	}
	download.cmd = cmd
	download.tempPath = tempPath
	download.outputPath = outputPath
	download.startedAt = startedAt
	if err := cmd.Start(); err != nil {
		d.Unlock()
		d.finishConversion(id, download, tempPath, outputPath, startedAt, err)
		return fmt.Errorf("failed to start ffmpeg conversion: %w", err)
	}
	d.Unlock()

	d.setProgressState(id, download, "running")
	go d.readProgress(id, download, stdout)
	go logCommandOutput("ffmpeg-vod-convert", id, stderr)
	go func() {
		d.finishConversion(id, download, tempPath, outputPath, startedAt, cmd.Wait())
	}()

	slog.Info("vod conversion started", "id", id, "preset", validatedPreset, "ffmpeg_pid", cmd.Process.Pid)
	return nil
}

// StopVODDownloads attempts to gracefully stop any active VOD downloads.
func StopVODDownloads() error {
	return vodDownloadState.Stop()
}

// Stop attempts to gracefully stop any active VOD downloads.
func (d *VODDownloader) Stop() error {
	d.Lock()
	d.stopping = true
	active := make(map[string]*vodDownload, len(d.active))
	for id, download := range d.active {
		download.stopping = true
		if download.cancel != nil {
			download.cancel()
		}
		active[id] = download
	}
	d.Unlock()

	var stopErrs []error
	for id, download := range active {
		if err := d.stopDownload(id, download); err != nil {
			stopErrs = append(stopErrs, err)
		}
	}
	return errors.Join(stopErrs...)
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

func (d *VODDownloader) prepareStart(id string, download *vodDownload) bool {
	d.Lock()
	defer d.Unlock()
	return !d.stopping && !download.stopping && d.active != nil && d.active[id] == download
}

func newVODDownload() *vodDownload {
	now := time.Now()
	progress := VODDownloadProgress{
		Active:    true,
		Progress:  "resolving",
		StartedAt: now,
		UpdatedAt: now,
	}
	return &vodDownload{progress: progress, done: make(chan struct{})}
}

func (d *VODDownloader) createPartialFile(id string, download *vodDownload, dir string) (string, error) {
	d.Lock()
	defer d.Unlock()
	if d.stopping || download.stopping || d.active == nil || d.active[id] != download {
		return "", ErrVODDownloadsStopping
	}
	file, err := os.CreateTemp(vodPartialDir(dir), id+"-*.part")
	if err != nil {
		return "", fmt.Errorf("failed to create partial vod file: %w", err)
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("failed to close partial vod file: %w", err)
	}
	download.tempPath = path
	return path, nil
}

func (d *VODDownloader) finishDownload(id string, download *vodDownload, tempPath, outputPath string, startedAt time.Time, runErr error) {
	download.finishOnce.Do(func() {
		d.Lock()
		stopping := download.stopping
		d.Unlock()

		err := runErr
		if err == nil && stopping {
			err = context.Canceled
		}
		if err == nil {
			err = finalizeVODDownload(tempPath, outputPath)
		}
		if err != nil {
			if tempPath != "" {
				_ = os.Remove(tempPath)
			}
			if stopping && errors.Is(err, context.Canceled) {
				slog.Info("vod download canceled", "id", id, "output", outputPath, "elapsed", time.Since(startedAt))
			} else {
				slog.Error("vod download exited", "id", id, "output", outputPath, "elapsed", time.Since(startedAt), "error", err)
			}
		} else {
			completedAt := time.Now()
			if err := os.Chtimes(outputPath, completedAt, completedAt); err != nil {
				slog.Error("failed to record vod download completion time", "id", id, "output", outputPath, "error", err)
			}
			slog.Info("vod download finished", "id", id, "output", outputPath, "elapsed", time.Since(startedAt))
		}

		d.clear(id, download)
		if download.cancel != nil {
			download.cancel()
		}
		close(download.done)
	})
}

func (d *VODDownloader) finishConversion(id string, download *vodDownload, tempPath, outputPath string, startedAt time.Time, runErr error) {
	download.finishOnce.Do(func() {
		d.Lock()
		stopping := download.stopping
		d.Unlock()

		err := runErr
		if err == nil && stopping {
			err = context.Canceled
		}
		if err == nil {
			err = finalizeVODConversion(tempPath, outputPath, vodConversionBackupPath(filepath.Dir(outputPath), id))
		}
		if err != nil {
			if tempPath != "" {
				_ = os.Remove(tempPath)
			}
			if stopping && errors.Is(err, context.Canceled) {
				slog.Info("vod conversion canceled", "id", id, "output", outputPath, "elapsed", time.Since(startedAt))
			} else {
				slog.Error("vod conversion exited", "id", id, "output", outputPath, "elapsed", time.Since(startedAt), "error", err)
			}
		} else {
			completedAt := time.Now()
			if err := os.Chtimes(outputPath, completedAt, completedAt); err != nil {
				slog.Error("failed to record vod conversion completion time", "id", id, "output", outputPath, "error", err)
			}
			info, statErr := os.Stat(outputPath)
			d.Lock()
			if statErr == nil {
				if d.presets == nil {
					d.presets = make(map[string]vodPresetCacheEntry)
				}
				d.presets[id] = vodPresetCacheEntry{
					size: info.Size(), modTime: info.ModTime(),
					metadata: vodDownloadMetadata{Preset: VODDownloadPreset(download.progress.Preset), OriginalSize: download.progress.OriginalSize},
				}
			} else {
				delete(d.presets, id)
			}
			d.Unlock()
			slog.Info("vod conversion finished", "id", id, "output", outputPath, "elapsed", time.Since(startedAt))
		}

		d.clear(id, download)
		if download.cancel != nil {
			download.cancel()
		}
		close(download.done)
	})
}

func finalizeVODDownload(tempPath, outputPath string) error {
	if tempPath == "" {
		return fmt.Errorf("partial vod path is missing")
	}
	info, err := os.Stat(tempPath)
	if err != nil {
		return fmt.Errorf("failed to inspect partial vod file: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return fmt.Errorf("partial vod file is empty or not regular")
	}
	file, err := os.OpenFile(tempPath, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("failed to open partial vod file: %w", err)
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	if syncErr != nil {
		return fmt.Errorf("failed to sync partial vod file: %w", syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("failed to close partial vod file: %w", closeErr)
	}
	if _, err := os.Stat(outputPath); err == nil {
		return ErrVODDownloadAlreadyExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to check final vod file: %w", err)
	}
	if err := os.Rename(tempPath, outputPath); err != nil {
		return fmt.Errorf("failed to finalize vod download: %w", err)
	}
	if err := syncVODDirectory(outputPath); err != nil {
		slog.Warn("failed to sync vod directory after finalization", "output", outputPath, "error", err)
	}
	return nil
}

func syncVODDirectory(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (d *VODDownloader) stopDownload(id string, download *vodDownload) error {
	if download == nil || download.done == nil {
		return nil
	}

	d.Lock()
	download.stopping = true
	if download.cancel != nil {
		download.cancel()
	}
	cmd := download.cmd
	done := download.done
	outputPath := download.outputPath
	startedAt := download.startedAt
	d.Unlock()

	if cmd == nil || cmd.Process == nil {
		if waitForVODDownloadDone(done, vodDownloadStopTimeout) {
			return nil
		}
		return fmt.Errorf("%w: timed out stopping vod download startup %s", ErrStopTimeout, id)
	}

	signalErr := cmd.Process.Signal(os.Interrupt)
	if signalErr == nil || errors.Is(signalErr, os.ErrProcessDone) {
		if waitForVODDownloadDone(done, vodDownloadStopTimeout) {
			slog.Info("vod download stopped", "id", id, "output", outputPath, "elapsed", time.Since(startedAt))
			return nil
		}
	}
	if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("failed to kill vod download %s: %w", id, err)
	}
	if !waitForVODDownloadDone(done, vodDownloadStopTimeout) {
		return fmt.Errorf("%w: killed vod download %s did not exit", ErrStopTimeout, id)
	}
	slog.Warn("vod download required forced kill", "id", id, "output", outputPath)
	return nil
}

func waitForVODDownloadDone(done <-chan struct{}, timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
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
