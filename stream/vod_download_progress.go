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
	"strconv"
	"strings"
	"time"
)

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
			updateVODEstimatedSize(download)
		}
	case "speed":
		progress.Speed = value
		updateVODETA(download)
	case "out_time_us":
		if micros, err := strconv.ParseInt(value, 10, 64); err == nil && micros >= 0 {
			download.processedDuration = time.Duration(micros) * time.Microsecond
			updateVODETA(download)
			updateVODEstimatedSize(download)
		}
	case "progress":
		progress.Progress = value
	}
}

func updateVODEstimatedSize(download *vodDownload) {
	progress := &download.progress
	if progress.Operation != "convert" || progress.TotalSize <= 0 || download.totalDuration <= 0 || download.processedDuration <= 0 {
		return
	}
	estimated := float64(progress.TotalSize) * float64(download.totalDuration) / float64(download.processedDuration)
	if estimated >= float64(math.MaxInt64) {
		progress.EstimatedSize = math.MaxInt64
		return
	}
	progress.EstimatedSize = int64(math.Ceil(estimated))
}

func updateVODETA(download *vodDownload) {
	if download.totalDuration <= 0 || download.processedDuration <= 0 {
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
