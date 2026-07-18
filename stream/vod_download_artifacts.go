package stream

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Open opens a completed VOD download for playback.
func (d *VODDownloader) Open(id string) (*os.File, error) {
	id = strings.TrimSpace(id)
	if !vodDownloadIDRE.MatchString(id) {
		return nil, fmt.Errorf("invalid vod id")
	}

	d.Lock()
	defer d.Unlock()
	if d.dir == "" {
		return nil, ErrVODDownloadsDisabled
	}
	if d.active != nil {
		if _, ok := d.active[id]; ok {
			return nil, ErrVODDownloadNotFound
		}
	}

	file, err := os.Open(vodDownloadPath(d.dir, id))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrVODDownloadNotFound
		}
		return nil, fmt.Errorf("failed to open vod download: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("failed to inspect vod download: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		_ = file.Close()
		return nil, ErrVODDownloadNotFound
	}
	return file, nil
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
		download.cancelContexts()
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

func (d *VODDownloader) RemoveMetadataIfNoDownload(id string, removeMetadata func() error) error {
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
	if download := d.active[id]; download != nil {
		d.Unlock()
		return ErrVODDownloadProtected
	}
	if d.dir != "" {
		info, err := os.Stat(vodDownloadPath(d.dir, id))
		if err == nil && info.Mode().IsRegular() && info.Size() > 0 {
			d.Unlock()
			return ErrVODDownloadProtected
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			d.Unlock()
			return fmt.Errorf("failed to inspect vod download: %w", err)
		}
	}
	d.removing[id] = struct{}{}
	d.Unlock()
	defer func() {
		d.Lock()
		delete(d.removing, id)
		d.Unlock()
	}()

	return removeMetadata()
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
		metadata := d.completedFileMetadata(id, outputPath, info.Size(), info.ModTime())
		return true, info.Size(), metadata, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, 0, vodDownloadMetadata{}, nil
	}
	return false, 0, vodDownloadMetadata{}, fmt.Errorf("failed to check vod output file: %w", err)
}

func (d *VODDownloader) completedFileMetadata(id, outputPath string, size int64, modTime time.Time) vodDownloadMetadata {
	key := vodMetadataProbeKey{id: id, path: outputPath, size: size, modTime: modTime.UnixNano()}
	now := time.Now()

	d.Lock()
	if cached, ok := d.presets[id]; ok && cached.size == size && cached.modTime.Equal(modTime) {
		if cached.metadata.Preset != "" || now.Before(cached.retryAt) {
			d.Unlock()
			return cached.metadata
		}
	}
	if call := d.metadataProbes[key]; call != nil {
		done := call.done
		d.Unlock()
		<-done
		return call.metadata
	}
	if d.metadataProbes == nil {
		d.metadataProbes = make(map[vodMetadataProbeKey]*vodMetadataProbeCall)
	}
	call := &vodMetadataProbeCall{done: make(chan struct{})}
	d.metadataProbes[key] = call
	d.Unlock()

	metadata, probeErr := probeVODDownloadMetadata(outputPath)
	entry := vodPresetCacheEntry{size: size, modTime: modTime, metadata: metadata}
	if probeErr != nil {
		// Cache an unknown result briefly to avoid a process/log storm while
		// keeping failures retryable and never guessing the codec.
		slog.Warn("failed to read vod download preset", "id", id, "error", probeErr)
		metadata = vodDownloadMetadata{}
		entry.metadata = metadata
		entry.retryAt = time.Now().Add(vodMetadataProbeRetryDelay)
	} else if metadata.DurationSeconds > 0 {
		metadata.TotalBitrate = int64(float64(size) * 8 / metadata.DurationSeconds)
		entry.metadata = metadata
	}

	d.Lock()
	if d.presets == nil {
		d.presets = make(map[string]vodPresetCacheEntry)
	}
	d.presets[id] = entry
	call.metadata = metadata
	delete(d.metadataProbes, key)
	close(call.done)
	d.Unlock()
	return metadata
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
