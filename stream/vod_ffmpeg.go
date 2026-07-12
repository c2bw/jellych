package stream

import (
	"fmt"
	"strings"
)

// VODDownloadPreset selects the fixed encoding profile used for a VOD download.
type VODDownloadPreset string

const (
	VODDownloadPresetOriginal VODDownloadPreset = "original"
	VODDownloadPresetH264     VODDownloadPreset = "h264"
	VODDownloadPresetHEVC     VODDownloadPreset = "hevc"
	VODDownloadPresetVP9      VODDownloadPreset = "vp9"
)

// ParseVODDownloadPreset validates a preset name. An empty name selects the
// original stream-copy profile for backwards compatibility.
func ParseVODDownloadPreset(value string) (VODDownloadPreset, error) {
	preset := VODDownloadPreset(strings.ToLower(strings.TrimSpace(value)))
	if preset == "" {
		return VODDownloadPresetOriginal, nil
	}
	switch preset {
	case VODDownloadPresetOriginal, VODDownloadPresetH264, VODDownloadPresetHEVC, VODDownloadPresetVP9:
		return preset, nil
	default:
		return "", fmt.Errorf("invalid vod download preset %q", value)
	}
}

// buildVODDownloadArgs owns the ffmpeg output contract separately from VOD
// lifecycle, retention, and filesystem coordination.
func buildVODDownloadArgs(inputURL, outputPath, title, channel string, preset VODDownloadPreset) []string {
	args := []string{
		"-hide_banner",
		"-loglevel", "warning",
		"-y",
		"-nostdin",
		"-nostats",
		"-progress", "pipe:1",
		"-i", inputURL,
		"-map", "0:v?",
		"-map", "0:a?",
		"-map", "0:s?",
	}
	args = appendVODPresetCodecArgs(args, preset)
	if title != "" {
		args = append(args, "-metadata", "title="+title)
	}
	if channel != "" {
		args = append(args, "-metadata", "artist="+channel)
	}
	args = append(args, "-metadata", "jellych_download_preset="+string(preset))
	return append(args, "-f", "matroska", outputPath)
}

func buildVODConversionArgs(inputPath, outputPath string, preset VODDownloadPreset, originalSize int64) []string {
	args := []string{
		"-hide_banner",
		"-loglevel", "warning",
		"-y",
		"-nostdin",
		"-nostats",
		"-progress", "pipe:1",
		"-i", inputPath,
		"-map", "0:v?",
		"-map", "0:a?",
		"-map", "0:s?",
		"-map_metadata", "0",
	}
	args = appendVODPresetCodecArgs(args, preset)
	args = append(args,
		"-metadata", "jellych_download_preset="+string(preset),
		"-metadata", fmt.Sprintf("jellych_original_size=%d", originalSize),
	)
	return append(args, "-f", "matroska", outputPath)
}

func appendVODPresetCodecArgs(args []string, preset VODDownloadPreset) []string {
	switch preset {
	case VODDownloadPresetH264:
		return append(args, "-c:v", "libx264", "-preset", "medium", "-crf", "23", "-c:a", "aac", "-b:a", "128k", "-c:s", "copy")
	case VODDownloadPresetHEVC:
		return append(args, "-c:v", "libx265", "-preset", "medium", "-crf", "25", "-c:a", "aac", "-b:a", "128k", "-c:s", "copy")
	case VODDownloadPresetVP9:
		return append(args, "-c:v", "libvpx-vp9", "-crf", "32", "-b:v", "0", "-deadline", "good", "-cpu-used", "2", "-c:a", "libopus", "-b:a", "128k", "-c:s", "copy")
	default:
		return append(args, "-c", "copy")
	}
}
