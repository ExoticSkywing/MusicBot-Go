package handler

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCleanupStaleCacheEntriesRemovesOnlyOldGeneratedEntries(t *testing.T) {
	t.Parallel()

	cacheDir := t.TempDir()
	now := time.Date(2026, time.July, 29, 12, 0, 0, 0, time.UTC)
	oldTime := now.Add(-2 * time.Hour)
	recentTime := now.Add(-30 * time.Minute)

	oldFiles := []string{
		"1774808140730337-song.flac",
		"1774808140730337-cover.jpg.resize.jpg",
		"recognize-1774808140730337000.ogg",
		"recognize-1774808140730337000.ogg.mp3",
	}
	for _, name := range oldFiles {
		writeJanitorTestFile(t, filepath.Join(cacheDir, name), oldTime)
	}
	oldDir := filepath.Join(cacheDir, "1774808140730337")
	if err := os.Mkdir(oldDir, 0o755); err != nil {
		t.Fatalf("create old timestamp directory: %v", err)
	}
	writeJanitorTestFile(t, filepath.Join(oldDir, "song.flac"), oldTime)
	setJanitorTestModTime(t, oldDir, oldTime)
	oldPartsDir := filepath.Join(cacheDir, "1774808140730337-song.flac.parts")
	if err := os.Mkdir(oldPartsDir, 0o755); err != nil {
		t.Fatalf("create old multipart directory: %v", err)
	}
	writeJanitorTestFile(t, filepath.Join(oldPartsDir, "part.0"), oldTime)
	setJanitorTestModTime(t, oldPartsDir, oldTime)

	recentFile := filepath.Join(cacheDir, "1774808140730338-recent.flac")
	writeJanitorTestFile(t, recentFile, recentTime)
	recentDir := filepath.Join(cacheDir, "1774808140730338")
	if err := os.Mkdir(recentDir, 0o755); err != nil {
		t.Fatalf("create recent timestamp directory: %v", err)
	}
	writeJanitorTestFile(t, filepath.Join(recentDir, "song.flac"), recentTime)
	setJanitorTestModTime(t, recentDir, recentTime)

	preservedFiles := []string{
		"spotify-credentials.json",
		"spotify-credentials.json.oauthtoken",
		"spotify-credentials.json.verifier",
		"notes.txt",
		"123-notes.txt",
		"177480814073033-song.flac",
		"17748081407303370-song.flac",
		"1774808140730339",
		"recognize-not-a-timestamp.ogg",
		"recognize-1774808140730337000.wav",
	}
	for _, name := range preservedFiles {
		writeJanitorTestFile(t, filepath.Join(cacheDir, name), oldTime)
	}
	preservedDirs := []string{
		"bilibili",
		"123",
		"1774808140730337000",
		"177480814073033-song.flac.parts",
		"1774808140730339-not-a-timestamp-directory",
	}
	for _, name := range preservedDirs {
		path := filepath.Join(cacheDir, name)
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatalf("create preserved directory %q: %v", name, err)
		}
		setJanitorTestModTime(t, path, oldTime)
	}

	stats, err := cleanupStaleCacheEntriesAt(cacheDir, time.Hour, now)
	if err != nil {
		t.Fatalf("cleanup cache: %v", err)
	}
	if stats.ScannedEntries != 23 {
		t.Fatalf("scanned entries = %d, want 23", stats.ScannedEntries)
	}
	if stats.EligibleEntries != 6 {
		t.Fatalf("eligible entries = %d, want 6", stats.EligibleEntries)
	}
	if stats.RemovedFiles != 4 {
		t.Fatalf("removed files = %d, want 4", stats.RemovedFiles)
	}
	if stats.RemovedDirectories != 2 {
		t.Fatalf("removed directories = %d, want 2", stats.RemovedDirectories)
	}
	if stats.FailedEntries != 0 {
		t.Fatalf("failed entries = %d, want 0", stats.FailedEntries)
	}

	for _, name := range append(oldFiles, filepath.Base(oldDir), filepath.Base(oldPartsDir)) {
		assertJanitorTestMissing(t, filepath.Join(cacheDir, name))
	}
	assertJanitorTestExists(t, recentFile)
	assertJanitorTestExists(t, recentDir)
	for _, name := range preservedFiles {
		assertJanitorTestExists(t, filepath.Join(cacheDir, name))
	}
	for _, name := range preservedDirs {
		assertJanitorTestExists(t, filepath.Join(cacheDir, name))
	}
}

func TestCleanupStaleCacheEntriesDoesNotFollowSymlinks(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	cacheDir := filepath.Join(root, "cache")
	outsideDir := filepath.Join(root, "outside")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatalf("create cache directory: %v", err)
	}
	if err := os.MkdirAll(outsideDir, 0o755); err != nil {
		t.Fatalf("create outside directory: %v", err)
	}
	outsideFile := filepath.Join(outsideDir, "keep.txt")
	if err := os.WriteFile(outsideFile, []byte("keep"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}

	linkPath := filepath.Join(cacheDir, "1774808140730337")
	if err := os.Symlink(outsideDir, linkPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	now := time.Now()
	stats, err := cleanupStaleCacheEntriesAt(cacheDir, time.Hour, now)
	if err != nil {
		t.Fatalf("cleanup cache: %v", err)
	}
	if stats.ScannedEntries != 1 || stats.EligibleEntries != 0 ||
		stats.RemovedFiles != 0 || stats.RemovedDirectories != 0 || stats.FailedEntries != 0 {
		t.Fatalf("unexpected stats for symlink: %+v", stats)
	}
	assertJanitorTestExists(t, linkPath)
	assertJanitorTestExists(t, outsideFile)
}

func TestCleanupStaleCacheEntriesMissingDirectoryAndAgeValidation(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "missing")
	stats, err := cleanupStaleCacheEntriesAt(missing, time.Hour, time.Now())
	if err != nil {
		t.Fatalf("missing cache directory should be a no-op: %v", err)
	}
	if stats != (CacheJanitorStats{}) {
		t.Fatalf("missing cache stats = %+v, want zero", stats)
	}

	deleteAllDir := t.TempDir()
	deleteAllPath := filepath.Join(deleteAllDir, "1774808140730337-song.flac")
	deleteAllNow := time.Now()
	writeJanitorTestFile(t, deleteAllPath, deleteAllNow.Add(time.Hour))
	deleteAllStats, err := cleanupStaleCacheEntriesAt(deleteAllDir, 0, deleteAllNow)
	if err != nil {
		t.Fatalf("zero cleanup age: %v", err)
	}
	if deleteAllStats.RemovedFiles != 1 {
		t.Fatalf("zero-age removed files = %d, want 1", deleteAllStats.RemovedFiles)
	}
	assertJanitorTestMissing(t, deleteAllPath)

	if _, err := cleanupStaleCacheEntriesAt(t.TempDir(), -time.Second, time.Now()); err == nil {
		t.Fatal("expected negative cleanup age to fail")
	}
}

func writeJanitorTestFile(t *testing.T, path string, modTime time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte("test"), 0o644); err != nil {
		t.Fatalf("write %q: %v", path, err)
	}
	setJanitorTestModTime(t, path, modTime)
}

func setJanitorTestModTime(t *testing.T, path string, modTime time.Time) {
	t.Helper()
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatalf("set modtime for %q: %v", path, err)
	}
}

func assertJanitorTestExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("expected %q to exist: %v", path, err)
	}
}

func assertJanitorTestMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("expected %q to be removed, stat err = %v", path, err)
	}
}
