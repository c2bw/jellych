package stream

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

var ErrVODDownloadsDisabled = errors.New("vod downloads folder not configured")
var ErrVODDownloadAlreadyStarted = errors.New("vod download already started")
var ErrVODDownloadAlreadyExists = errors.New("vod download already exists")
var ErrVODDownloadNotFound = errors.New("vod download not found")
var ErrVODDownloadsStopping = errors.New("vod downloads stopping")
var ErrVODRemovalInProgress = errors.New("vod removal in progress")
var ErrVODConversionRequiresOriginal = errors.New("vod conversion requires an original download")
var ErrVODConversionTargetOriginal = errors.New("vod conversion target must be compressed")

const vodURLResolutionTimeout = 20 * time.Second
const vodDownloadCleanupInterval = time.Hour
const defaultVODDownloadRetention = 30 * 24 * time.Hour
const vodDownloadStopTimeout = 5 * time.Second
const vodMetadataProbeTimeout = 5 * time.Second
const vodMetadataProbeRetryDelay = 30 * time.Second
const vodPartialDirName = ".jellych-partials"

var vodDownloadIDRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type vodDownload struct {
	progress            VODDownloadProgress
	lastTotalSize       int64
	lastTotalSizeUpdate time.Time
	cmd                 *exec.Cmd
	done                chan struct{}
	cancel              context.CancelFunc
	stopping            bool
	finishOnce          sync.Once
	tempPath            string
	outputPath          string
	startedAt           time.Time
	totalDuration       time.Duration
	processedDuration   time.Duration
}

// VODDownloadProgress is a snapshot of an active or completed VOD download.
type VODDownloadProgress struct {
	Active          bool      `json:"active"`
	Downloaded      bool      `json:"downloaded"`
	Progress        string    `json:"progress,omitempty"`
	TotalSize       int64     `json:"totalSize,omitempty"`
	BytesPerSecond  int64     `json:"bytesPerSecond,omitempty"`
	Speed           string    `json:"speed,omitempty"`
	Preset          string    `json:"preset,omitempty"`
	Operation       string    `json:"operation,omitempty"`
	OriginalSize    int64     `json:"originalSize,omitempty"`
	ETASeconds      int64     `json:"etaSeconds,omitempty"`
	DurationSeconds float64   `json:"-"`
	VideoCodec      string    `json:"videoCodec,omitempty"`
	VideoWidth      int       `json:"videoWidth,omitempty"`
	VideoHeight     int       `json:"videoHeight,omitempty"`
	TotalBitrate    int64     `json:"totalBitrate,omitempty"`
	StartedAt       time.Time `json:"startedAt,omitempty,omitzero"`
	UpdatedAt       time.Time `json:"updatedAt,omitempty,omitzero"`
}

type VODDownloader struct {
	sync.Mutex
	dir       string
	retention time.Duration
	active    map[string]*vodDownload
	removing  map[string]struct{}
	presets   map[string]vodPresetCacheEntry
	stopping  bool
}

type vodPresetCacheEntry struct {
	size     int64
	modTime  time.Time
	metadata vodDownloadMetadata
	retryAt  time.Time
}

type vodDownloadMetadata struct {
	Preset          VODDownloadPreset
	OriginalSize    int64
	DurationSeconds float64
	VideoCodec      string
	VideoWidth      int
	VideoHeight     int
	TotalBitrate    int64
}

var probeVODDownloadMetadata = func(path string) (vodDownloadMetadata, error) {
	ctx, cancel := context.WithTimeout(context.Background(), vodMetadataProbeTimeout)
	defer cancel()
	output, err := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-show_entries", "format=duration:format_tags=jellych_download_preset,jellych_original_size:stream=codec_type,codec_name,width,height", "-of", "json", path).Output()
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return vodDownloadMetadata{}, fmt.Errorf("timed out reading vod download metadata: %w", ctx.Err())
		}
		return vodDownloadMetadata{}, fmt.Errorf("failed to read vod download metadata: %w", err)
	}
	var result struct {
		Format struct {
			Duration string            `json:"duration"`
			Tags     map[string]string `json:"tags"`
		} `json:"format"`
		Streams []struct {
			CodecType string `json:"codec_type"`
			CodecName string `json:"codec_name"`
			Width     int    `json:"width"`
			Height    int    `json:"height"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		return vodDownloadMetadata{}, fmt.Errorf("failed to parse vod download metadata: %w", err)
	}
	preset, err := ParseVODDownloadPreset(metadataValue(result.Format.Tags, "jellych_download_preset"))
	if err != nil {
		return vodDownloadMetadata{}, fmt.Errorf("invalid vod download preset metadata: %w", err)
	}
	originalSize, _ := strconv.ParseInt(metadataValue(result.Format.Tags, "jellych_original_size"), 10, 64)
	durationSeconds, _ := strconv.ParseFloat(result.Format.Duration, 64)
	metadata := vodDownloadMetadata{Preset: preset, OriginalSize: originalSize, DurationSeconds: durationSeconds}
	for _, mediaStream := range result.Streams {
		if mediaStream.CodecType == "video" {
			metadata.VideoCodec = mediaStream.CodecName
			metadata.VideoWidth = mediaStream.Width
			metadata.VideoHeight = mediaStream.Height
			break
		}
	}
	return metadata, nil
}

func metadataValue(tags map[string]string, name string) string {
	for key, value := range tags {
		if strings.EqualFold(key, name) {
			return value
		}
	}
	return ""
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
	d.presets = nil
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
		if !info.Mode().IsRegular() || info.Size() == 0 {
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

	downloaded, size, metadata, err := d.completedFile(id)
	if err != nil {
		return VODDownloadProgress{}, err
	}
	return VODDownloadProgress{
		Downloaded: downloaded, TotalSize: size, Preset: string(metadata.Preset), OriginalSize: metadata.OriginalSize,
		DurationSeconds: metadata.DurationSeconds, VideoCodec: metadata.VideoCodec,
		VideoWidth: metadata.VideoWidth, VideoHeight: metadata.VideoHeight, TotalBitrate: metadata.TotalBitrate,
	}, nil
}

// StartVODDownload resolves a Twitch VOD and downloads it without transcoding.
func StartVODDownload(ctx context.Context, id, vodURL, title, channel string) error {
	return StartVODDownloadWithPreset(ctx, id, vodURL, title, channel, VODDownloadPresetOriginal)
}

// StartVODDownloadWithPreset resolves a Twitch VOD and downloads it using preset.
func StartVODDownloadWithPreset(ctx context.Context, id, vodURL, title, channel string, preset VODDownloadPreset) error {
	return vodDownloadState.StartWithPreset(ctx, id, vodURL, title, channel, preset)
}

// Start resolves a Twitch VOD and downloads it without transcoding.
func (d *VODDownloader) Start(ctx context.Context, id, vodURL, title, channel string) error {
	return d.StartWithPreset(ctx, id, vodURL, title, channel, VODDownloadPresetOriginal)
}

// StartWithPreset resolves a Twitch VOD and downloads it using preset.
func (d *VODDownloader) StartWithPreset(ctx context.Context, id, vodURL, title, channel string, preset VODDownloadPreset) error {
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
	if info, err := os.Stat(outputPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrVODDownloadNotFound
		}
		return fmt.Errorf("failed to inspect vod download: %w", err)
	} else if !info.Mode().IsRegular() || info.Size() == 0 {
		return ErrVODDownloadNotFound
	}
	if err := os.Remove(vodConversionBackupPath(dir, id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to delete vod conversion backup: %w", err)
	}
	if err := os.Remove(outputPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrVODDownloadNotFound
		}
		return fmt.Errorf("failed to delete vod download: %w", err)
	}
	delete(d.presets, id)
	slog.Info("vod download deleted", "id", id, "output", outputPath)
	return nil
}

// RemoveVODWithArtifacts serializes removal for id, stops any active
// download, deletes partial and completed artifacts, then removes metadata.
func RemoveVODWithArtifacts(id string, removeMetadata func() error) error {
	return vodDownloadState.RemoveWithArtifacts(id, removeMetadata)
}

func (d *VODDownloader) RemoveWithArtifacts(id string, removeMetadata func() error) error {
	id = strings.TrimSpace(id)
	if !vodDownloadIDRE.MatchString(id) {
		return fmt.Errorf("invalid vod id")
	}
	if removeMetadata == nil {
		return fmt.Errorf("remove metadata callback is required")
	}

	d.Lock()
	if d.removing == nil {
		d.removing = make(map[string]struct{})
	}
	if _, exists := d.removing[id]; exists {
		d.Unlock()
		return ErrVODRemovalInProgress
	}
	d.removing[id] = struct{}{}
	download := d.active[id]
	if download != nil {
		download.stopping = true
		if download.cancel != nil {
			download.cancel()
		}
	}
	dir := d.dir
	d.Unlock()
	defer func() {
		d.Lock()
		delete(d.removing, id)
		d.Unlock()
	}()

	if download != nil {
		if err := d.stopDownload(id, download); err != nil {
			return err
		}
	}
	if dir != "" {
		if err := d.removeArtifacts(dir, id); err != nil {
			return err
		}
	}
	return removeMetadata()
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
		updateVODConversionETA(download)
	case "out_time_us":
		if micros, err := strconv.ParseInt(value, 10, 64); err == nil && micros >= 0 {
			download.processedDuration = time.Duration(micros) * time.Microsecond
			updateVODConversionETA(download)
		}
	case "progress":
		progress.Progress = value
	}
}

func updateVODConversionETA(download *vodDownload) {
	if download.progress.Operation != "convert" || download.totalDuration <= 0 || download.processedDuration <= 0 {
		return
	}
	speed, err := strconv.ParseFloat(strings.TrimSuffix(download.progress.Speed, "x"), 64)
	if err != nil || speed <= 0 {
		return
	}
	remaining := download.totalDuration - download.processedDuration
	if remaining <= 0 {
		download.progress.ETASeconds = 0
		return
	}
	download.progress.ETASeconds = int64(math.Ceil(remaining.Seconds() / speed))
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

func vodPartialDir(dir string) string {
	return filepath.Join(dir, vodPartialDirName)
}

func vodConversionBackupPath(dir, id string) string {
	return filepath.Join(vodPartialDir(dir), id+".original")
}

func (d *VODDownloader) removeArtifacts(dir, id string) error {
	var removeErrs []error
	outputPath := vodDownloadPath(dir, id)
	if err := os.Remove(outputPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		removeErrs = append(removeErrs, fmt.Errorf("failed to delete completed vod: %w", err))
	}
	if err := os.Remove(vodConversionBackupPath(dir, id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		removeErrs = append(removeErrs, fmt.Errorf("failed to delete conversion backup: %w", err))
	}

	partialDir := vodPartialDir(dir)
	entries, err := os.ReadDir(partialDir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			removeErrs = append(removeErrs, fmt.Errorf("failed to read partial vod folder: %w", err))
		}
		return errors.Join(removeErrs...)
	}
	for _, entry := range entries {
		if entry.IsDir() || vodPartialID(entry.Name()) != id {
			continue
		}
		path := filepath.Join(partialDir, entry.Name())
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			removeErrs = append(removeErrs, fmt.Errorf("failed to delete partial vod %q: %w", entry.Name(), err))
		}
	}
	return errors.Join(removeErrs...)
}

func vodPartialID(name string) string {
	if filepath.Ext(name) != ".part" {
		return ""
	}
	base := strings.TrimSuffix(name, ".part")
	separator := strings.LastIndexByte(base, '-')
	if separator <= 0 || separator == len(base)-1 {
		return ""
	}
	return base[:separator]
}

func (d *VODDownloader) completedFile(id string) (bool, int64, vodDownloadMetadata, error) {
	id = strings.TrimSpace(id)
	if !vodDownloadIDRE.MatchString(id) {
		return false, 0, vodDownloadMetadata{}, fmt.Errorf("invalid vod id")
	}

	d.Lock()
	dir := d.dir
	if dir == "" {
		d.Unlock()
		return false, 0, vodDownloadMetadata{}, ErrVODDownloadsDisabled
	}
	if d.active != nil {
		if _, ok := d.active[id]; ok {
			d.Unlock()
			return false, 0, vodDownloadMetadata{}, nil
		}
	}
	outputPath := vodDownloadPath(dir, id)
	d.Unlock()

	info, err := os.Stat(outputPath)
	if err == nil {
		if !info.Mode().IsRegular() || info.Size() == 0 {
			return false, 0, vodDownloadMetadata{}, nil
		}
		d.Lock()
		cached, ok := d.presets[id]
		d.Unlock()
		if ok && cached.size == info.Size() && cached.modTime.Equal(info.ModTime()) {
			if cached.metadata.Preset != "" || time.Now().Before(cached.retryAt) {
				return true, info.Size(), cached.metadata, nil
			}
		}
		preset, probeErr := probeVODDownloadMetadata(outputPath)
		if probeErr != nil {
			// Cache an unknown result briefly to avoid a process/log storm while
			// keeping failures retryable and never guessing the codec.
			slog.Warn("failed to read vod download preset", "id", id, "error", probeErr)
			d.Lock()
			if d.presets == nil {
				d.presets = make(map[string]vodPresetCacheEntry)
			}
			d.presets[id] = vodPresetCacheEntry{
				size: info.Size(), modTime: info.ModTime(), retryAt: time.Now().Add(vodMetadataProbeRetryDelay),
			}
			d.Unlock()
			return true, info.Size(), vodDownloadMetadata{}, nil
		}
		if preset.DurationSeconds > 0 {
			preset.TotalBitrate = int64(float64(info.Size()) * 8 / preset.DurationSeconds)
		}
		d.Lock()
		if d.presets == nil {
			d.presets = make(map[string]vodPresetCacheEntry)
		}
		d.presets[id] = vodPresetCacheEntry{size: info.Size(), modTime: info.ModTime(), metadata: preset}
		d.Unlock()
		return true, info.Size(), preset, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, 0, vodDownloadMetadata{}, nil
	}
	return false, 0, vodDownloadMetadata{}, fmt.Errorf("failed to check vod output file: %w", err)
}

func finalizeVODConversion(tempPath, outputPath, backupPath string) error {
	if err := syncCompletedPartial(tempPath); err != nil {
		return err
	}
	if info, err := os.Stat(outputPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrVODDownloadNotFound
		}
		return fmt.Errorf("failed to inspect original vod: %w", err)
	} else if !info.Mode().IsRegular() || info.Size() == 0 {
		return ErrVODDownloadNotFound
	}
	if err := os.Remove(backupPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to remove stale conversion backup: %w", err)
	}
	if err := os.Rename(outputPath, backupPath); err != nil {
		return fmt.Errorf("failed to back up original vod: %w", err)
	}
	if err := os.Rename(tempPath, outputPath); err != nil {
		rollbackErr := os.Rename(backupPath, outputPath)
		return errors.Join(fmt.Errorf("failed to install converted vod: %w", err), rollbackErr)
	}
	if err := syncVODDirectory(outputPath); err != nil {
		slog.Warn("failed to sync vod directory after conversion", "output", outputPath, "error", err)
	}
	if err := os.Remove(backupPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Warn("failed to remove original vod conversion backup", "backup", backupPath, "error", err)
	}
	return nil
}

func syncCompletedPartial(tempPath string) error {
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
	return nil
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

	partialDeleted, partialErr := d.cleanupInterruptedPartials(dir)

	entries, err := os.ReadDir(dir)
	if err != nil {
		return partialDeleted, errors.Join(partialErr, fmt.Errorf("failed to read vod downloads folder: %w", err))
	}

	cutoff := now.Add(-retention)
	deleted := partialDeleted
	var cleanupErrs []error
	if partialErr != nil {
		cleanupErrs = append(cleanupErrs, partialErr)
	}
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
		if info.Size() > 0 && !info.ModTime().Before(cutoff) {
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

func (d *VODDownloader) cleanupInterruptedPartials(dir string) (int, error) {
	partialDir := vodPartialDir(dir)
	entries, err := os.ReadDir(partialDir)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("failed to read interrupted vod downloads: %w", err)
	}

	deleted := 0
	var cleanupErrs []error
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".original" {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".original")
		if !vodDownloadIDRE.MatchString(id) {
			continue
		}
		changed, err := d.recoverInterruptedConversion(dir, id)
		if err != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("failed to recover interrupted conversion %q: %w", entry.Name(), err))
			continue
		}
		if changed {
			deleted++
		}
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".part" {
			continue
		}
		path := filepath.Join(partialDir, entry.Name())
		removed, err := d.removeInterruptedPartial(dir, path)
		if err != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("failed to delete interrupted download %q: %w", entry.Name(), err))
			continue
		}
		if removed {
			deleted++
			slog.Info("interrupted vod download deleted", "output", path)
		}
	}
	return deleted, errors.Join(cleanupErrs...)
}

func (d *VODDownloader) recoverInterruptedConversion(dir, id string) (bool, error) {
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
	backupPath := vodConversionBackupPath(dir, id)
	outputPath := vodDownloadPath(dir, id)
	if info, err := os.Stat(outputPath); err == nil && info.Mode().IsRegular() && info.Size() > 0 {
		if err := os.Remove(backupPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
		slog.Info("removed completed vod conversion backup", "id", id, "backup", backupPath)
		return true, nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if err := os.Rename(backupPath, outputPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	slog.Warn("restored original vod after interrupted conversion", "id", id, "output", outputPath)
	return true, nil
}

func (d *VODDownloader) removeInterruptedPartial(dir, path string) (bool, error) {
	d.Lock()
	defer d.Unlock()
	if d.dir != dir {
		return false, nil
	}
	for _, download := range d.active {
		if download != nil && download.tempPath == path {
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
	if err := os.Remove(vodConversionBackupPath(dir, id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
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
