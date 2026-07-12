package stream

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

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
