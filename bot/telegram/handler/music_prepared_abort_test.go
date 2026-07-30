package handler

import (
	"context"
	"os"
	"testing"
	"time"

	botpkg "github.com/liuran001/MusicBot-Go/bot"
	"github.com/liuran001/MusicBot-Go/bot/download"
	"github.com/liuran001/MusicBot-Go/bot/platform"
)

// When the last waiter goes away — which is what /cancel does — the shared
// download must be aborted immediately instead of running to completion and
// only then discarding its output. Otherwise "cancel and free the cache" would
// keep writing to disk for as long as the transfer would have taken.
func TestReleasingLastWaiterAbortsSharedDownload(t *testing.T) {
	cacheDir := t.TempDir()
	downloadCtxCh := make(chan context.Context, 1)
	downloaderReturned := make(chan struct{})

	service := download.NewDownloadService(download.DownloadServiceOptions{Timeout: 10 * time.Second})
	info := &platform.DownloadInfo{
		URL:     "test://aborted-download",
		Size:    16,
		Format:  "mp3",
		Quality: platform.QualityHigh,
		Downloader: func(ctx context.Context, _ *platform.DownloadInfo, destPath string, _ func(written, total int64)) (int64, error) {
			select {
			case downloadCtxCh <- ctx:
			default:
			}
			// Never completes on its own: the only way out is cancellation.
			<-ctx.Done()
			close(downloaderReturned)
			_ = os.Remove(destPath)
			return 0, ctx.Err()
		},
	}
	track := &platform.Track{
		ID:       "track-abort-on-last-waiter",
		Title:    "Aborted Track",
		Duration: time.Second,
		Artists:  []platform.Artist{{ID: "artist-1", Name: "Abort Artist"}},
	}
	h := &MusicHandler{
		CacheDir:        cacheDir,
		DownloadService: service,
		ProcessTimeout:  30 * time.Second,
	}

	waiterCtx, cancelWaiter := context.WithCancel(context.Background())
	acquireReturned := make(chan struct{})
	go func() {
		defer close(acquireReturned)
		_, _, _, _ = h.acquirePreparedMedia(
			waiterCtx, "test", track.ID, "high",
			newStubPlatform("test"), track, info, nil, nil, nil,
			&botpkg.SongInfo{Platform: "test", TrackID: track.ID, SongName: track.Title, Duration: 1},
			nil,
		)
	}()

	var sharedCtx context.Context
	select {
	case sharedCtx = <-downloadCtxCh:
	case <-time.After(3 * time.Second):
		t.Fatal("shared download did not start")
	}
	if sharedCtx.Err() != nil {
		t.Fatal("shared download context was already cancelled before the waiter left")
	}

	cancelWaiter()

	select {
	case <-sharedCtx.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("shared download kept running after its last waiter cancelled")
	}
	select {
	case <-downloaderReturned:
	case <-time.After(3 * time.Second):
		t.Fatal("downloader did not unwind after cancellation")
	}
	select {
	case <-acquireReturned:
	case <-time.After(3 * time.Second):
		t.Fatal("acquirePreparedMedia did not return after cancellation")
	}

	// The aborted generation must not be left behind for the next requester to
	// attach to, or that request would inherit this cancellation.
	deadline := time.Now().Add(3 * time.Second)
	key := "prepared:test:" + track.ID + ":high"
	for {
		h.prepareMu.Lock()
		_, exists := h.preparedInFlight[key]
		h.prepareMu.Unlock()
		if !exists {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("aborted generation state was not removed")
		}
		time.Sleep(time.Millisecond)
	}
}
