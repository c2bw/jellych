package stream

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
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
		return append(args, "-c:v", "libx265", "-preset", "medium", "-crf", "25", "-x265-params", x265ThreadParams(availableCPUCount()), "-c:a", "aac", "-b:a", "128k", "-c:s", "copy")
	case VODDownloadPresetVP9:
		return append(args, "-c:v", "libvpx-vp9", "-crf", "32", "-b:v", "0", "-deadline", "good", "-cpu-used", "2", "-threads", fmt.Sprintf("%d", vpxThreadCount(availableCPUCount())), "-row-mt", "1", "-c:a", "libopus", "-b:a", "128k", "-c:s", "copy")
	default:
		return append(args, "-c", "copy")
	}
}

// VODPresetCommands returns the resolved codec arguments shown by the web UI.
// Dynamic CPU settings are evaluated on the server so the preview matches the
// command that FFmpeg will actually receive in this deployment.
func VODPresetCommands() map[string]string {
	commands := make(map[string]string, 4)
	for _, preset := range []VODDownloadPreset{
		VODDownloadPresetOriginal,
		VODDownloadPresetH264,
		VODDownloadPresetHEVC,
		VODDownloadPresetVP9,
	} {
		commands[string(preset)] = strings.Join(appendVODPresetCodecArgs(nil, preset), " ")
	}
	return commands
}

func availableCPUCount() int {
	logical := runtime.NumCPU()
	if data, err := os.ReadFile("/sys/fs/cgroup/cpu.max"); err == nil {
		return effectiveCPUCount(logical, string(data))
	}
	quota, quotaErr := os.ReadFile("/sys/fs/cgroup/cpu/cpu.cfs_quota_us")
	period, periodErr := os.ReadFile("/sys/fs/cgroup/cpu/cpu.cfs_period_us")
	if quotaErr == nil && periodErr == nil {
		return effectiveCPUCount(logical, strings.TrimSpace(string(quota))+" "+strings.TrimSpace(string(period)))
	}
	return logical
}

// effectiveCPUCount combines scheduler affinity, reflected by logical, with a
// cgroup v2 cpu.max value (or an equivalent v1 quota/period pair).
func effectiveCPUCount(logical int, cpuMax string) int {
	if logical < 1 {
		logical = 1
	}
	fields := strings.Fields(cpuMax)
	if len(fields) != 2 || fields[0] == "max" {
		return logical
	}
	quota, quotaErr := strconv.Atoi(fields[0])
	period, periodErr := strconv.Atoi(fields[1])
	if quotaErr != nil || periodErr != nil || quota <= 0 || period <= 0 {
		return logical
	}
	quotaCPUs := (quota + period - 1) / period
	if quotaCPUs < logical {
		return quotaCPUs
	}
	return logical
}

// x265ThreadParams explicitly creates an encoder pool. Some virtualized and
// containerized hosts expose all CPUs to the process while x265's NUMA
// discovery still fails to allocate its automatic pool, disabling WPP and
// leaving the encode limited to a few frame threads.
func x265ThreadParams(cpuCount int) string {
	if cpuCount < 1 {
		cpuCount = 1
	}
	frameThreads := (cpuCount + 3) / 4
	if frameThreads > 6 {
		frameThreads = 6
	}
	return fmt.Sprintf("pools=%d:frame-threads=%d", cpuCount, frameThreads)
}

// vpxThreadCount uses the CPUs available to the process without exceeding
// libvpx's recommended maximum thread count.
func vpxThreadCount(cpuCount int) int {
	if cpuCount < 1 {
		return 1
	}
	if cpuCount > 16 {
		return 16
	}
	return cpuCount
}
