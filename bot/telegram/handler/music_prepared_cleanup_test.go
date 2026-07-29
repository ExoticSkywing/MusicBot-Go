package handler

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	botpkg "github.com/liuran001/MusicBot-Go/bot"
	"github.com/liuran001/MusicBot-Go/bot/download"
	"github.com/liuran001/MusicBot-Go/bot/platform"
)

func TestAcquirePreparedMediaSharesReadyArtifactUntilAllWaitersRelease(t *testing.T) {
	cacheDir := t.TempDir()
	payload := []byte("prepared audio")
	var downloadCalls atomic.Int32

	service := download.NewDownloadService(download.DownloadServiceOptions{
		Timeout: time.Second,
	})
	info := &platform.DownloadInfo{
		URL:     "test://prepared-artifact",
		Size:    int64(len(payload)),
		Format:  "mp3",
		Quality: platform.QualityHigh,
		Downloader: func(_ context.Context, _ *platform.DownloadInfo, destPath string, progress func(written, total int64)) (int64, error) {
			downloadCalls.Add(1)
			if err := os.WriteFile(destPath, payload, 0o644); err != nil {
				return 0, err
			}
			if progress != nil {
				progress(int64(len(payload)), int64(len(payload)))
			}
			return int64(len(payload)), nil
		},
	}
	track := &platform.Track{
		ID:       "track-1",
		Title:    "Prepared Track",
		Duration: time.Second,
		Artists:  []platform.Artist{{ID: "artist-1", Name: "Prepared Artist"}},
	}
	h := &MusicHandler{
		CacheDir:        cacheDir,
		DownloadService: service,
	}

	firstInfo := &botpkg.SongInfo{
		Platform:    "test",
		TrackID:     track.ID,
		SongName:    track.Title,
		SongArtists: track.Artists[0].Name,
		Duration:    1,
	}
	firstMusicPath, firstPicPath, releaseFirst, err := h.acquirePreparedMedia(
		context.Background(),
		"test",
		track.ID,
		"high",
		newStubPlatform("test"),
		track,
		info,
		nil,
		nil,
		nil,
		firstInfo,
		nil,
	)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if releaseFirst == nil {
		t.Fatal("first acquire returned nil release function")
	}
	if got := downloadCalls.Load(); got != 1 {
		t.Fatalf("downloader calls after first acquire = %d, want 1", got)
	}
	if _, err := os.Stat(firstMusicPath); err != nil {
		t.Fatalf("prepared music missing after first acquire: %v", err)
	}

	secondInfo := &botpkg.SongInfo{
		Platform:    "test",
		TrackID:     track.ID,
		SongName:    track.Title,
		SongArtists: track.Artists[0].Name,
		Duration:    1,
	}
	secondMusicPath, secondPicPath, releaseSecond, err := h.acquirePreparedMedia(
		context.Background(),
		"test",
		track.ID,
		"high",
		newStubPlatform("test"),
		track,
		info,
		nil,
		nil,
		nil,
		secondInfo,
		nil,
	)
	if err != nil {
		t.Fatalf("second acquire: %v", err)
	}
	if releaseSecond == nil {
		t.Fatal("second acquire returned nil release function")
	}
	if got := downloadCalls.Load(); got != 1 {
		t.Fatalf("ready artifact started another download: calls = %d, want 1", got)
	}
	if secondMusicPath != firstMusicPath {
		t.Fatalf("second acquire music path = %q, want shared path %q", secondMusicPath, firstMusicPath)
	}
	if secondPicPath != firstPicPath {
		t.Fatalf("second acquire picture path = %q, want shared path %q", secondPicPath, firstPicPath)
	}

	releaseSecond()
	if _, err := os.Stat(firstMusicPath); err != nil {
		t.Fatalf("artifact cleaned while first waiter still held it: %v", err)
	}

	releaseFirst()
	if _, err := os.Stat(firstMusicPath); !os.IsNotExist(err) {
		t.Fatalf("artifact still exists after both waiters released, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Dir(firstMusicPath)); !os.IsNotExist(err) {
		t.Fatalf("artifact directory still exists after both waiters released, stat err = %v", err)
	}

	h.prepareMu.Lock()
	remainingStates := len(h.preparedInFlight)
	h.prepareMu.Unlock()
	if remainingStates != 0 {
		t.Fatalf("prepared artifact states after final release = %d, want 0", remainingStates)
	}

	// A release function is idempotent. Recreate the same path after the final
	// cleanup and call both releases again; a second cleanup would remove it.
	if err := os.MkdirAll(filepath.Dir(firstMusicPath), 0o755); err != nil {
		t.Fatalf("recreate artifact directory: %v", err)
	}
	if err := os.WriteFile(firstMusicPath, []byte("sentinel"), 0o644); err != nil {
		t.Fatalf("write cleanup sentinel: %v", err)
	}
	releaseFirst()
	releaseSecond()
	if _, err := os.Stat(firstMusicPath); err != nil {
		t.Fatalf("duplicate release cleaned artifact more than once: %v", err)
	}
}

func TestAcquirePreparedMediaCanceledLastWaiterStartsFreshGeneration(t *testing.T) {
	cacheDir := t.TempDir()
	payload := []byte("prepared audio")
	firstDownloadStarted := make(chan struct{})
	allowFirstDownload := make(chan struct{})
	firstDownloaderReturned := make(chan struct{})
	var downloadCalls atomic.Int32

	service := download.NewDownloadService(download.DownloadServiceOptions{
		Timeout: time.Second,
	})
	info := &platform.DownloadInfo{
		URL:     "test://prepared-artifact-canceled-generation",
		Size:    int64(len(payload)),
		Format:  "mp3",
		Quality: platform.QualityHigh,
		Downloader: func(_ context.Context, _ *platform.DownloadInfo, destPath string, progress func(written, total int64)) (int64, error) {
			call := downloadCalls.Add(1)
			if call == 1 {
				// Make the old generation's final cleanup long enough for the
				// test to deterministically observe the state-transition window.
				stamp := strings.SplitN(filepath.Base(destPath), "-", 2)[0]
				finalDir := filepath.Join(filepath.Dir(destPath), stamp)
				if err := os.MkdirAll(finalDir, 0o755); err != nil {
					return 0, err
				}
				for i := 0; i < 8_000; i++ {
					name := filepath.Join(finalDir, fmt.Sprintf("cleanup-%05d", i))
					if err := os.WriteFile(name, []byte("x"), 0o644); err != nil {
						return 0, err
					}
				}
				close(firstDownloadStarted)
				<-allowFirstDownload
			}
			if err := os.WriteFile(destPath, payload, 0o644); err != nil {
				return 0, err
			}
			if progress != nil {
				progress(int64(len(payload)), int64(len(payload)))
			}
			if call == 1 {
				close(firstDownloaderReturned)
			}
			return int64(len(payload)), nil
		},
	}
	track := &platform.Track{
		ID:       "track-canceled-generation",
		Title:    "Canceled Prepared Track",
		Duration: time.Second,
		Artists:  []platform.Artist{{ID: "artist-1", Name: "Prepared Artist"}},
	}
	h := &MusicHandler{
		CacheDir:        cacheDir,
		DownloadService: service,
		ProcessTimeout:  5 * time.Second,
	}
	newSongInfo := func() *botpkg.SongInfo {
		return &botpkg.SongInfo{
			Platform:    "test",
			TrackID:     track.ID,
			SongName:    track.Title,
			SongArtists: track.Artists[0].Name,
			Duration:    1,
		}
	}
	acquire := func(ctx context.Context, songInfo *botpkg.SongInfo) (string, func(), error) {
		musicPath, _, release, err := h.acquirePreparedMedia(
			ctx,
			"test",
			track.ID,
			"high",
			newStubPlatform("test"),
			track,
			info,
			nil,
			nil,
			nil,
			songInfo,
			nil,
		)
		return musicPath, release, err
	}

	firstCtx, cancelFirst := context.WithCancel(context.Background())
	firstResult := make(chan error, 1)
	go func() {
		_, _, err := acquire(firstCtx, newSongInfo())
		firstResult <- err
	}()

	select {
	case <-firstDownloadStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("first download did not start")
	}
	cancelFirst()
	select {
	case err := <-firstResult:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("first acquire error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled first acquire did not return")
	}

	close(allowFirstDownload)
	select {
	case <-firstDownloaderReturned:
	case <-time.After(time.Second):
		t.Fatal("first downloader did not finish")
	}

	key := "prepared:test:" + track.ID + ":high"
	deadline := time.Now().Add(3 * time.Second)
	for {
		h.prepareMu.Lock()
		_, exists := h.preparedInFlight[key]
		h.prepareMu.Unlock()
		if !exists {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("canceled generation state was not removed")
		}
		time.Sleep(100 * time.Microsecond)
	}

	secondMusicPath, releaseSecond, err := acquire(context.Background(), newSongInfo())
	if err != nil {
		t.Fatalf("second acquire: %v", err)
	}
	if releaseSecond == nil {
		t.Fatal("second acquire returned nil release function")
	}
	if got := downloadCalls.Load(); got != 2 {
		t.Fatalf("second acquire reused canceled generation: downloader calls = %d, want 2", got)
	}
	if _, err := os.Stat(secondMusicPath); err != nil {
		t.Fatalf("second acquire returned missing artifact %q: %v", secondMusicPath, err)
	}

	releaseSecond()
	h.prepareMu.Lock()
	remainingStates := len(h.preparedInFlight)
	h.prepareMu.Unlock()
	if remainingStates != 0 {
		t.Fatalf("prepared artifact states after second release = %d, want 0", remainingStates)
	}
}
