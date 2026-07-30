package handler

import (
	"context"
	"sync"
)

// Job kinds tracked per user. They are reported separately because a user's
// mental model is "my download" vs "my upload", and because the two run on
// different contexts: an upload is deliberately detached from the request
// context so it survives the handler returning, so cancelling the download
// context alone would leave the upload running.
const (
	userJobDownload = "download"
	userJobUpload   = "upload"
)

// userJobRegistry tracks cancellable in-flight work per requesting user so
// /cancel can stop exactly that user's tasks and nothing else.
//
// The zero value is ready to use.
type userJobRegistry struct {
	mu   sync.Mutex
	seq  uint64
	jobs map[int64]map[uint64]userJob
}

type userJob struct {
	kind   string
	cancel context.CancelFunc
}

// add registers a cancellable job and returns an idempotent release func. A
// zero userID (no attributable requester, e.g. a channel post) is not tracked;
// the returned func is then a no-op.
func (r *userJobRegistry) add(userID int64, kind string, cancel context.CancelFunc) func() {
	if r == nil || userID == 0 || cancel == nil {
		return func() {}
	}
	r.mu.Lock()
	if r.jobs == nil {
		r.jobs = make(map[int64]map[uint64]userJob)
	}
	if r.jobs[userID] == nil {
		r.jobs[userID] = make(map[uint64]userJob)
	}
	r.seq++
	id := r.seq
	r.jobs[userID][id] = userJob{kind: kind, cancel: cancel}
	r.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			defer r.mu.Unlock()
			if bucket, ok := r.jobs[userID]; ok {
				delete(bucket, id)
				if len(bucket) == 0 {
					delete(r.jobs, userID)
				}
			}
		})
	}
}

// counts reports how many of the user's jobs are in flight, per kind.
func (r *userJobRegistry) counts(userID int64) (downloads, uploads int) {
	if r == nil || userID == 0 {
		return 0, 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, job := range r.jobs[userID] {
		switch job.kind {
		case userJobUpload:
			uploads++
		default:
			downloads++
		}
	}
	return downloads, uploads
}

// cancelUser cancels every job the user has in flight and reports how many of
// each kind were cancelled.
//
// The entries are dropped from the registry before any cancel func runs, so a
// cancellation that synchronously unwinds into its own release func cannot
// deadlock on the registry mutex, and a concurrent second /cancel cannot cancel
// the same job twice. Each job's own cleanup path — finishUploadTask for an
// upload, releasePreparedWaiter for a download — frees that task's scratch
// files as it unwinds.
func (r *userJobRegistry) cancelUser(userID int64) (downloads, uploads int) {
	if r == nil || userID == 0 {
		return 0, 0
	}
	r.mu.Lock()
	bucket := r.jobs[userID]
	delete(r.jobs, userID)
	r.mu.Unlock()

	cancels := make([]context.CancelFunc, 0, len(bucket))
	for _, job := range bucket {
		switch job.kind {
		case userJobUpload:
			uploads++
		default:
			downloads++
		}
		cancels = append(cancels, job.cancel)
	}
	for _, cancel := range cancels {
		cancel()
	}
	return downloads, uploads
}

// trackUserJob is the MusicHandler-side helper used at the hook points.
func (h *MusicHandler) trackUserJob(userID int64, kind string, cancel context.CancelFunc) func() {
	if h == nil {
		return func() {}
	}
	return h.userJobs.add(userID, kind, cancel)
}

// CancelUserJobs cancels all in-flight download and upload work for one user.
func (h *MusicHandler) CancelUserJobs(userID int64) (downloads, uploads int) {
	if h == nil {
		return 0, 0
	}
	return h.userJobs.cancelUser(userID)
}

// UserJobCounts reports one user's in-flight work without cancelling it.
func (h *MusicHandler) UserJobCounts(userID int64) (downloads, uploads int) {
	if h == nil {
		return 0, 0
	}
	return h.userJobs.counts(userID)
}
