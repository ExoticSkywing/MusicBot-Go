package handler

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	botpkg "github.com/liuran001/MusicBot-Go/bot"
	"github.com/liuran001/MusicBot-Go/bot/download"
	"github.com/liuran001/MusicBot-Go/bot/platform"
)

func TestAcquirePreparedMediaSharesReadyArtifactUntilAllWaitersRelease(t *testing.T) {
	cacheDir := t.TempDir()
	payload := preparedAudioWAV(t)
	var downloadCalls atomic.Int32

	service := download.NewDownloadService(download.DownloadServiceOptions{
		Timeout: time.Second,
	})
	info := &platform.DownloadInfo{
		URL:     "test://prepared-artifact",
		Size:    int64(len(payload)),
		Format:  "wav",
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
	if !firstInfo.AudioValidated || !secondInfo.AudioValidated || secondInfo.Duration != 1 {
		t.Fatal("full audio validation was not propagated to all waiters")
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
	payload := preparedAudioWAV(t)
	firstDownloadStarted := make(chan struct{})
	allowFirstDownload := make(chan struct{})
	unblockFirstDownload := sync.OnceFunc(func() { close(allowFirstDownload) })
	var downloadCalls atomic.Int32

	service := download.NewDownloadService(download.DownloadServiceOptions{
		Timeout: time.Second,
	})
	info := &platform.DownloadInfo{
		URL:     "test://prepared-artifact-canceled-generation",
		Size:    int64(len(payload)),
		Format:  "wav",
		Quality: platform.QualityHigh,
		Downloader: func(_ context.Context, _ *platform.DownloadInfo, destPath string, progress func(written, total int64)) (int64, error) {
			call := downloadCalls.Add(1)
			if call == 1 {
				// Keep the canceled generation alive until its replacement is
				// ready. A channel makes this independent of filesystem speed.
				if err := os.WriteFile(destPath, payload, 0o644); err != nil {
					return 0, err
				}
				close(firstDownloadStarted)
				// Deliberately model a downloader slow to honor cancellation.
				// Test cleanup always opens this gate, even after t.Fatal.
				<-allowFirstDownload
				return int64(len(payload)), nil
			}
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
		ID:       "track-canceled-generation",
		Title:    "Canceled Prepared Track",
		Duration: time.Second,
		Artists:  []platform.Artist{{ID: "artist-1", Name: "Prepared Artist"}},
	}
	h := &MusicHandler{
		CacheDir:        cacheDir,
		DownloadService: service,
		ProcessTimeout:  30 * time.Second,
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
	firstAcquireDone := make(chan struct{})
	t.Cleanup(func() {
		cancelFirst()
		unblockFirstDownload()
		// Join acquire before waiting on prepareWG, so no worker can be
		// added after ShutdownUploads starts waiting. TempDir cleans up last.
		<-firstAcquireDone
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := h.ShutdownUploads(ctx); err != nil {
			t.Errorf("stop prepared-media workers: %v", err)
		}
	})
	go func() {
		defer close(firstAcquireDone)
		_, release, err := acquire(firstCtx, newSongInfo())
		if release != nil {
			release()
		}
		firstResult <- err
	}()

	select {
	case <-firstDownloadStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("first download did not start")
	}
	key := "prepared:test:" + track.ID + ":high"
	h.prepareMu.Lock()
	firstState := h.preparedInFlight[key]
	h.prepareMu.Unlock()
	if firstState == nil {
		t.Fatal("first generation state missing")
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

	h.prepareMu.Lock()
	_, exists := h.preparedInFlight[key]
	h.prepareMu.Unlock()
	if exists {
		t.Fatal("canceled generation state was not removed before downloader returned")
	}

	secondCtx, cancelSecond := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelSecond()
	secondMusicPath, releaseSecond, err := acquire(secondCtx, newSongInfo())
	if err != nil {
		t.Fatalf("second acquire: %v", err)
	}
	if releaseSecond == nil {
		t.Fatal("second acquire returned nil release function")
	}
	defer releaseSecond()
	if got := downloadCalls.Load(); got != 2 {
		t.Fatalf("second acquire reused canceled generation: downloader calls = %d, want 2", got)
	}
	if _, err := os.Stat(secondMusicPath); err != nil {
		t.Fatalf("second acquire returned missing artifact %q: %v", secondMusicPath, err)
	}
	h.prepareMu.Lock()
	secondState := h.preparedInFlight[key]
	h.prepareMu.Unlock()
	if secondState == nil || secondState == firstState {
		t.Fatal("second acquire did not create a fresh generation")
	}

	unblockFirstDownload()
	select {
	case <-firstState.done:
	case <-time.After(3 * time.Second):
		t.Fatal("canceled generation did not finish")
	}
	h.prepareMu.Lock()
	currentState := h.preparedInFlight[key]
	h.prepareMu.Unlock()
	if currentState != secondState {
		t.Fatal("old generation completion removed its replacement")
	}
	if _, err := os.Stat(secondMusicPath); err != nil {
		t.Fatalf("old generation cleanup removed replacement artifact: %v", err)
	}

	releaseSecond()
	h.prepareMu.Lock()
	remainingStates := len(h.preparedInFlight)
	h.prepareMu.Unlock()
	if remainingStates != 0 {
		t.Fatalf("prepared artifact states after second release = %d, want 0", remainingStates)
	}
}
