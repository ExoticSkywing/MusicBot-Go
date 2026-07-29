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

func TestUploadShutdownBeginDrainsPendingExactlyOnce(t *testing.T) {
	cacheFile := filepath.Join(t.TempDir(), "pending.flac")
	if err := os.WriteFile(cacheFile, []byte("pending upload"), 0o644); err != nil {
		t.Fatalf("write pending artifact: %v", err)
	}

	var cleanupDoneCalls int32
	resultCh := make(chan uploadResult, 1)
	h := &MusicHandler{
		UploadQueue:     make(chan uploadTask, 1),
		uploadAccepting: true,
		activeUploads:   make(map[uint64]uploadTask),
	}
	h.UploadQueue <- uploadTask{
		cleanup:     []string{cacheFile},
		cleanupDone: func() { atomic.AddInt32(&cleanupDoneCalls, 1) },
		finishOnce:  &sync.Once{},
		resultCh:    resultCh,
	}

	h.BeginUploadShutdown()
	h.BeginUploadShutdown()

	if _, err := os.Stat(cacheFile); !os.IsNotExist(err) {
		t.Fatalf("pending artifact was not removed, stat err = %v", err)
	}
	if got := atomic.LoadInt32(&cleanupDoneCalls); got != 1 {
		t.Fatalf("cleanupDone calls = %d, want 1", got)
	}
	if got := len(h.UploadQueue); got != 0 {
		t.Fatalf("upload queue length = %d, want 0", got)
	}
	select {
	case result := <-resultCh:
		if !errors.Is(result.err, errUploadShuttingDown) {
			t.Fatalf("pending result error = %v, want %v", result.err, errUploadShuttingDown)
		}
	default:
		t.Fatal("pending task did not receive a shutdown result")
	}
}

func TestUploadShutdownCancelsActiveAndWaits(t *testing.T) {
	workerCtx, workerCancel := context.WithCancel(context.Background())
	taskCtx, taskCancel := context.WithCancel(context.Background())
	cleanupEntered := make(chan struct{})
	cleanupRelease := make(chan struct{})
	var releaseOnce sync.Once
	releaseCleanup := func() {
		releaseOnce.Do(func() {
			close(cleanupRelease)
		})
	}
	t.Cleanup(releaseCleanup)
	t.Cleanup(workerCancel)
	t.Cleanup(taskCancel)

	var cleanupDoneCalls int32
	h := &MusicHandler{
		UploadQueue:     make(chan uploadTask, 1),
		UploadLimiter:   make(chan struct{}, 1),
		uploadAccepting: true,
		uploadCancel:    workerCancel,
		activeUploads:   make(map[uint64]uploadTask),
	}
	// Keep the worker inside processUploadTask until shutdown cancels taskCtx.
	h.UploadLimiter <- struct{}{}
	h.uploadWG.Add(1)
	go func() {
		defer h.uploadWG.Done()
		h.runUploadWorker(workerCtx)
	}()

	h.UploadQueue <- uploadTask{
		id:         1,
		ctx:        taskCtx,
		cancel:     taskCancel,
		finishOnce: &sync.Once{},
		resultCh:   make(chan uploadResult, 1),
		cleanupDone: func() {
			atomic.AddInt32(&cleanupDoneCalls, 1)
			close(cleanupEntered)
			<-cleanupRelease
		},
	}
	waitForActiveUpload(t, h, 1)

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer shutdownCancel()
	shutdownResult := make(chan error, 1)
	go func() {
		shutdownResult <- h.ShutdownUploads(shutdownCtx)
	}()

	select {
	case <-taskCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("active upload context was not canceled")
	}
	select {
	case <-cleanupEntered:
	case <-time.After(time.Second):
		t.Fatal("active upload did not enter cleanup")
	}
	select {
	case err := <-shutdownResult:
		t.Fatalf("ShutdownUploads returned before the active task exited: %v", err)
	case <-time.After(30 * time.Millisecond):
	}

	releaseCleanup()
	select {
	case err := <-shutdownResult:
		if err != nil {
			t.Fatalf("ShutdownUploads returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("ShutdownUploads did not return after the active task exited")
	}
	if got := atomic.LoadInt32(&cleanupDoneCalls); got != 1 {
		t.Fatalf("cleanupDone calls = %d, want 1", got)
	}
}

func TestUploadShutdownCancelsAndWaitsForPreparedDownload(t *testing.T) {
	cacheDir := t.TempDir()
	downloadStarted := make(chan struct{})
	downloadCanceled := make(chan struct{})
	downloadRelease := make(chan struct{})
	var releaseOnce sync.Once
	releaseDownload := func() {
		releaseOnce.Do(func() {
			close(downloadRelease)
		})
	}
	t.Cleanup(releaseDownload)

	var downloadCalls atomic.Int32
	service := download.NewDownloadService(download.DownloadServiceOptions{
		Timeout: time.Second,
	})
	info := &platform.DownloadInfo{
		URL:     "test://prepared-shutdown",
		Format:  "mp3",
		Quality: platform.QualityHigh,
		Downloader: func(ctx context.Context, _ *platform.DownloadInfo, destPath string, _ func(written, total int64)) (int64, error) {
			downloadCalls.Add(1)
			if err := os.WriteFile(destPath, []byte("partial prepared download"), 0o644); err != nil {
				return 0, err
			}
			close(downloadStarted)
			<-ctx.Done()
			close(downloadCanceled)
			<-downloadRelease
			return 0, ctx.Err()
		},
	}
	track := &platform.Track{
		ID:       "prepared-shutdown",
		Title:    "Prepared Shutdown",
		Duration: time.Second,
	}
	h := &MusicHandler{
		CacheDir:        cacheDir,
		DownloadService: service,
	}

	acquireResult := make(chan error, 1)
	go func() {
		_, _, _, err := h.acquirePreparedMedia(
			context.Background(),
			"test",
			track.ID,
			"high",
			nil,
			track,
			info,
			nil,
			nil,
			nil,
			&botpkg.SongInfo{},
			nil,
		)
		acquireResult <- err
	}()

	select {
	case <-downloadStarted:
	case <-time.After(time.Second):
		t.Fatal("prepared download did not start")
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer shutdownCancel()
	shutdownResult := make(chan error, 1)
	go func() {
		shutdownResult <- h.ShutdownUploads(shutdownCtx)
	}()

	select {
	case <-downloadCanceled:
	case <-time.After(time.Second):
		t.Fatal("prepared download context was not canceled")
	}
	select {
	case err := <-shutdownResult:
		t.Fatalf("ShutdownUploads returned before the prepared download exited: %v", err)
	case <-time.After(30 * time.Millisecond):
	}

	releaseDownload()
	select {
	case err := <-shutdownResult:
		if err != nil {
			t.Fatalf("ShutdownUploads returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("ShutdownUploads did not return after the prepared download exited")
	}
	select {
	case err := <-acquireResult:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("prepared acquire error = %v, want %v", err, context.Canceled)
		}
	case <-time.After(time.Second):
		t.Fatal("prepared acquire did not return after shutdown")
	}

	if got := downloadCalls.Load(); got != 1 {
		t.Fatalf("prepared downloader calls = %d, want 1", got)
	}
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		t.Fatalf("read cache directory: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("prepared cache entries after shutdown = %d, want 0", len(entries))
	}
	h.prepareMu.Lock()
	remainingStates := len(h.preparedInFlight)
	h.prepareMu.Unlock()
	if remainingStates != 0 {
		t.Fatalf("prepared states after shutdown = %d, want 0", remainingStates)
	}
}

func TestUploadShutdownRejectsLateSendMusic(t *testing.T) {
	cacheFile := filepath.Join(t.TempDir(), "late.flac")
	if err := os.WriteFile(cacheFile, []byte("late upload"), 0o644); err != nil {
		t.Fatalf("write late artifact: %v", err)
	}

	var cleanupDoneCalls int32
	h := &MusicHandler{
		UploadQueue:     make(chan uploadTask, 1),
		uploadAccepting: true,
		activeUploads:   make(map[uint64]uploadTask),
	}
	h.BeginUploadShutdown()

	err := h.sendMusic(
		context.Background(),
		nil,
		nil,
		nil,
		&botpkg.SongInfo{},
		cacheFile,
		"",
		[]string{cacheFile},
		func() { atomic.AddInt32(&cleanupDoneCalls, 1) },
		"test",
		"late",
	)
	if !errors.Is(err, errUploadShuttingDown) {
		t.Fatalf("sendMusic error = %v, want %v", err, errUploadShuttingDown)
	}
	if got := atomic.LoadInt32(&cleanupDoneCalls); got != 1 {
		t.Fatalf("cleanupDone calls before sendMusic returned = %d, want 1", got)
	}
	if _, statErr := os.Stat(cacheFile); !os.IsNotExist(statErr) {
		t.Fatalf("late artifact was not removed, stat err = %v", statErr)
	}
	if got := len(h.UploadQueue); got != 0 {
		t.Fatalf("late task was enqueued after shutdown; queue length = %d", got)
	}
}

func TestUploadShutdownFinishConcurrentExactlyOnce(t *testing.T) {
	cacheFile := filepath.Join(t.TempDir(), "concurrent.flac")
	if err := os.WriteFile(cacheFile, []byte("concurrent finish"), 0o644); err != nil {
		t.Fatalf("write concurrent artifact: %v", err)
	}

	var cancelCalls int32
	var cleanupDoneCalls int32
	var onDoneCalls int32
	resultCh := make(chan uploadResult, 64)
	task := uploadTask{
		cancel:      func() { atomic.AddInt32(&cancelCalls, 1) },
		cleanup:     []string{cacheFile},
		cleanupDone: func() { atomic.AddInt32(&cleanupDoneCalls, 1) },
		finishOnce:  &sync.Once{},
		resultCh:    resultCh,
		onDone:      func(uploadResult) { atomic.AddInt32(&onDoneCalls, 1) },
	}
	h := &MusicHandler{}

	const callers = 64
	var wg sync.WaitGroup
	wg.Add(callers)
	for i := 0; i < callers; i++ {
		go func() {
			defer wg.Done()
			h.finishUploadTask(task, uploadResult{err: errUploadShuttingDown}, true)
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt32(&cancelCalls); got != 1 {
		t.Fatalf("cancel calls = %d, want 1", got)
	}
	if got := atomic.LoadInt32(&cleanupDoneCalls); got != 1 {
		t.Fatalf("cleanupDone calls = %d, want 1", got)
	}
	if got := atomic.LoadInt32(&onDoneCalls); got != 1 {
		t.Fatalf("onDone calls = %d, want 1", got)
	}
	if got := len(resultCh); got != 1 {
		t.Fatalf("result count = %d, want 1", got)
	}
	if _, err := os.Stat(cacheFile); !os.IsNotExist(err) {
		t.Fatalf("concurrent artifact was not removed, stat err = %v", err)
	}
}

func waitForActiveUpload(t *testing.T, h *MusicHandler, taskID uint64) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		h.uploadLifecycleMu.Lock()
		_, active := h.activeUploads[taskID]
		h.uploadLifecycleMu.Unlock()
		if active {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("upload task %d did not become active", taskID)
		}
		time.Sleep(time.Millisecond)
	}
}
