package handler

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CacheJanitorStats summarizes one top-level cache cleanup pass.
//
// Directory counts refer to top-level entries only. A matching directory is
// removed recursively, but its children are neither scanned nor counted.
type CacheJanitorStats struct {
	ScannedEntries     int
	EligibleEntries    int
	RemovedFiles       int
	RemovedDirectories int
	FailedEntries      int
}

// CleanupStaleCacheEntries removes stale, process-generated temporary entries
// from cacheDir.
//
// Only cacheDir's direct children are considered. The recognized names are:
//
//   - directories named with the 16-digit Unix-microsecond media timestamp;
//   - multipart directories beginning with that timestamp and ending in .parts;
//   - regular files beginning with that timestamp and a hyphen;
//   - .musicbot-download-<digits> staging files left behind by a crash;
//   - recognize-<19-digit Unix-nanosecond>.ogg and its .mp3 conversion, which
//     older releases produced before recognition stopped transcoding.
//
// Symlinks and special files are always preserved. This keeps the cleanup from
// following a cache entry to a path outside cacheDir. Unrecognized entries,
// including Spotify credentials/tokens and the bilibili directory, are also
// preserved. An olderThan value of zero removes every matching entry; negative
// values are rejected.
func CleanupStaleCacheEntries(cacheDir string, olderThan time.Duration) (CacheJanitorStats, error) {
	return cleanupStaleCacheEntriesAt(cacheDir, olderThan, time.Now())
}

func cleanupStaleCacheEntriesAt(cacheDir string, olderThan time.Duration, now time.Time) (CacheJanitorStats, error) {
	var stats CacheJanitorStats
	cacheDir = strings.TrimSpace(cacheDir)
	if cacheDir == "" {
		return stats, errors.New("cache directory is empty")
	}
	if olderThan < 0 {
		return stats, fmt.Errorf("cache cleanup age must not be negative: %s", olderThan)
	}

	root, err := filepath.Abs(cacheDir)
	if err != nil {
		return stats, fmt.Errorf("resolve cache directory %q: %w", cacheDir, err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return stats, nil
		}
		return stats, fmt.Errorf("read cache directory %q: %w", root, err)
	}

	cutoff := now.Add(-olderThan)
	var cleanupErrs []error
	for _, entry := range entries {
		stats.ScannedEntries++

		entryPath, pathErr := cacheTopLevelPath(root, entry.Name())
		if pathErr != nil {
			stats.FailedEntries++
			cleanupErrs = append(cleanupErrs, pathErr)
			continue
		}

		info, infoErr := os.Lstat(entryPath)
		if infoErr != nil {
			stats.FailedEntries++
			cleanupErrs = append(cleanupErrs, fmt.Errorf("inspect cache entry %q: %w", entryPath, infoErr))
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if !isCacheJanitorEntry(entry.Name(), info) ||
			(olderThan > 0 && info.ModTime().After(cutoff)) {
			continue
		}

		stats.EligibleEntries++
		if info.IsDir() {
			if removeErr := os.RemoveAll(entryPath); removeErr != nil {
				stats.FailedEntries++
				cleanupErrs = append(cleanupErrs, fmt.Errorf("remove cache directory %q: %w", entryPath, removeErr))
				continue
			}
			stats.RemovedDirectories++
			continue
		}
		if removeErr := os.Remove(entryPath); removeErr != nil {
			stats.FailedEntries++
			cleanupErrs = append(cleanupErrs, fmt.Errorf("remove cache file %q: %w", entryPath, removeErr))
			continue
		}
		stats.RemovedFiles++
	}

	return stats, errors.Join(cleanupErrs...)
}

func cacheTopLevelPath(root, name string) (string, error) {
	if name == "" || name != filepath.Base(name) {
		return "", fmt.Errorf("unsafe cache entry name %q", name)
	}
	candidate := filepath.Join(root, name)
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return "", fmt.Errorf("resolve cache entry %q: %w", name, err)
	}
	if relative == ".." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("cache entry %q escapes cache directory", name)
	}
	return candidate, nil
}

func isCacheJanitorEntry(name string, info os.FileInfo) bool {
	if info.IsDir() {
		return isMusicCacheTimestamp(name) ||
			(strings.HasSuffix(name, ".parts") && hasMusicTimestampPrefix(name))
	}
	if !info.Mode().IsRegular() {
		return false
	}
	return hasMusicTimestampPrefix(name) || isRecognizeTempFile(name) || isDownloadStagingFile(name)
}

// isDownloadStagingFile matches the partial files the download service stages
// alongside their destination. They are removed on the normal path; this only
// reaps the ones a crash left behind.
func isDownloadStagingFile(name string) bool {
	const prefix = ".musicbot-download-"
	if !strings.HasPrefix(name, prefix) {
		return false
	}
	remainder := strings.TrimPrefix(name, prefix)
	if remainder == "" {
		return false
	}
	for i := 0; i < len(remainder); i++ {
		if remainder[i] < '0' || remainder[i] > '9' {
			return false
		}
	}
	return true
}

func hasMusicTimestampPrefix(name string) bool {
	hyphen := strings.IndexByte(name, '-')
	return hyphen > 0 && hyphen < len(name)-1 && isMusicCacheTimestamp(name[:hyphen])
}

func isRecognizeTempFile(name string) bool {
	const prefix = "recognize-"
	if !strings.HasPrefix(name, prefix) {
		return false
	}
	remainder := strings.TrimPrefix(name, prefix)
	switch {
	case strings.HasSuffix(remainder, ".ogg.mp3"):
		remainder = strings.TrimSuffix(remainder, ".ogg.mp3")
	case strings.HasSuffix(remainder, ".ogg"):
		remainder = strings.TrimSuffix(remainder, ".ogg")
	default:
		return false
	}
	return isFixedWidthDigits(remainder, 19)
}

func isMusicCacheTimestamp(value string) bool {
	return isFixedWidthDigits(value, 16)
}

func isFixedWidthDigits(value string, width int) bool {
	if len(value) != width {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	return true
}
