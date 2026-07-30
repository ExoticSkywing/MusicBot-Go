package handler

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestUserJobRegistryCancelsOnlyTheCallersJobs(t *testing.T) {
	var registry userJobRegistry
	const alice, bob = int64(1), int64(2)

	aliceDownload, aliceDownloadCancelled := trackedCancel()
	aliceUpload, aliceUploadCancelled := trackedCancel()
	bobDownload, bobDownloadCancelled := trackedCancel()

	registry.add(alice, userJobDownload, aliceDownload)
	registry.add(alice, userJobUpload, aliceUpload)
	registry.add(bob, userJobDownload, bobDownload)

	if downloads, uploads := registry.counts(alice); downloads != 1 || uploads != 1 {
		t.Fatalf("counts(alice) = %d/%d, want 1/1", downloads, uploads)
	}

	downloads, uploads := registry.cancelUser(alice)
	if downloads != 1 || uploads != 1 {
		t.Fatalf("cancelUser(alice) = %d/%d, want 1/1", downloads, uploads)
	}
	if !*aliceDownloadCancelled || !*aliceUploadCancelled {
		t.Fatal("alice's jobs were not cancelled")
	}
	// The whole point of keying on the sender: a /cancel in a shared chat must
	// not abort somebody else's download.
	if *bobDownloadCancelled {
		t.Fatal("bob's download was cancelled by alice's /cancel")
	}
	if downloads, uploads := registry.counts(alice); downloads != 0 || uploads != 0 {
		t.Fatalf("counts(alice) after cancel = %d/%d, want 0/0", downloads, uploads)
	}
	if downloads, _ := registry.counts(bob); downloads != 1 {
		t.Fatalf("bob should still have 1 download, got %d", downloads)
	}
}

// A finished job must leave the registry, otherwise /cancel would report work
// that is long gone.
func TestUserJobRegistryReleaseRemovesEntry(t *testing.T) {
	var registry userJobRegistry
	cancel, cancelled := trackedCancel()
	release := registry.add(7, userJobDownload, cancel)

	release()
	release() // idempotent

	if downloads, uploads := registry.counts(7); downloads != 0 || uploads != 0 {
		t.Fatalf("counts after release = %d/%d, want 0/0", downloads, uploads)
	}
	if d, u := registry.cancelUser(7); d != 0 || u != 0 {
		t.Fatalf("cancelUser after release = %d/%d, want 0/0", d, u)
	}
	if *cancelled {
		t.Fatal("releasing a finished job must not cancel it")
	}
}

// A cancel func that unwinds into its own release func must not deadlock on the
// registry mutex — that is the real shape of an upload cancel, whose worker
// calls finishUploadTask, which calls releaseJob.
func TestUserJobRegistryCancelReentrantRelease(t *testing.T) {
	var registry userJobRegistry
	var release func()
	cancelled := false
	release = registry.add(9, userJobUpload, func() {
		cancelled = true
		release()
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		registry.cancelUser(9)
	}()
	select {
	case <-done:
	case <-timeoutAfterSeconds(5):
		t.Fatal("cancelUser deadlocked on a re-entrant release")
	}
	if !cancelled {
		t.Fatal("job was not cancelled")
	}
}

// Two concurrent /cancel calls must cancel each job exactly once.
func TestUserJobRegistryConcurrentCancelCountsOnce(t *testing.T) {
	var registry userJobRegistry
	var mu sync.Mutex
	calls := 0
	registry.add(11, userJobDownload, func() {
		mu.Lock()
		calls++
		mu.Unlock()
	})

	var wg sync.WaitGroup
	totals := make([]int, 2)
	for i := range totals {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			downloads, uploads := registry.cancelUser(11)
			totals[idx] = downloads + uploads
		}(i)
	}
	wg.Wait()

	if totals[0]+totals[1] != 1 {
		t.Fatalf("job reported %d times across two /cancel calls, want 1", totals[0]+totals[1])
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("cancel func ran %d times, want 1", calls)
	}
}

// Messages with no attributable sender (e.g. channel posts) cannot be cancelled
// by user, so they must not be tracked at all rather than pooled under id 0.
func TestUserJobRegistryIgnoresZeroUser(t *testing.T) {
	var registry userJobRegistry
	cancel, cancelled := trackedCancel()
	release := registry.add(0, userJobDownload, cancel)
	release()

	if d, u := registry.counts(0); d != 0 || u != 0 {
		t.Fatalf("counts(0) = %d/%d, want 0/0", d, u)
	}
	if d, u := registry.cancelUser(0); d != 0 || u != 0 {
		t.Fatalf("cancelUser(0) = %d/%d, want 0/0", d, u)
	}
	if *cancelled {
		t.Fatal("untracked job must not be cancelled")
	}
}

func TestMusicHandlerCancelUserJobsNilSafe(t *testing.T) {
	var h *MusicHandler
	if d, u := h.CancelUserJobs(5); d != 0 || u != 0 {
		t.Fatalf("nil handler CancelUserJobs = %d/%d, want 0/0", d, u)
	}
	if d, u := h.UserJobCounts(5); d != 0 || u != 0 {
		t.Fatalf("nil handler UserJobCounts = %d/%d, want 0/0", d, u)
	}
	if release := h.trackUserJob(5, userJobDownload, func() {}); release == nil {
		t.Fatal("trackUserJob must return a usable release func even on a nil handler")
	} else {
		release()
	}
}

func TestMusicHandlerTracksAndCancelsJobs(t *testing.T) {
	h := &MusicHandler{}
	ctx, cancel := context.WithCancel(context.Background())
	release := h.trackUserJob(42, userJobDownload, cancel)
	defer release()

	if d, u := h.UserJobCounts(42); d != 1 || u != 0 {
		t.Fatalf("UserJobCounts = %d/%d, want 1/0", d, u)
	}
	if d, u := h.CancelUserJobs(42); d != 1 || u != 0 {
		t.Fatalf("CancelUserJobs = %d/%d, want 1/0", d, u)
	}
	select {
	case <-ctx.Done():
	case <-timeoutAfterSeconds(5):
		t.Fatal("cancelling the job did not cancel its context")
	}
}

func timeoutAfterSeconds(seconds int) <-chan time.Time {
	return time.After(time.Duration(seconds) * time.Second)
}

func trackedCancel() (func(), *bool) {
	called := false
	return func() { called = true }, &called
}
