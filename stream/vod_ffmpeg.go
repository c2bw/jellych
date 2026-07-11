package stream

// buildVODDownloadArgs owns the ffmpeg output contract separately from VOD
// lifecycle, retention, and filesystem coordination.
func buildVODDownloadArgs(inputURL, outputPath, title, channel string) []string {
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
