package netease

import (
	"context"
	"errors"
	"fmt"

	"github.com/liuran001/MusicBot-Go/bot/recognize"
)

// Recognizer adapts the NetEase recognition service to the core interface.
type Recognizer struct {
	service *RecognizeService
}

func NewRecognizer(service *RecognizeService) *Recognizer {
	return &Recognizer{service: service}
}

func (r *Recognizer) Start(ctx context.Context) error {
	if r == nil || r.service == nil {
		return errors.New("recognizer not configured")
	}
	return r.service.Start(ctx)
}

func (r *Recognizer) Stop() error {
	if r == nil || r.service == nil {
		return nil
	}
	return r.service.Stop()
}

func (r *Recognizer) Recognize(ctx context.Context, audioData []byte) (*recognize.Result, error) {
	if r == nil || r.service == nil {
		return nil, errors.New("recognizer not configured")
	}
	result, err := r.service.Recognize(ctx, audioData)
	return mapRecognizeResult(result, err)
}

// RecognizeFile implements recognize.FileService for local Telegram Bot API
// paths, keeping large uploads out of Go memory.
func (r *Recognizer) RecognizeFile(ctx context.Context, filePath string) (*recognize.Result, error) {
	if r == nil || r.service == nil {
		return nil, errors.New("recognizer not configured")
	}
	result, err := r.service.RecognizeFile(ctx, filePath)
	return mapRecognizeResult(result, err)
}

func mapRecognizeResult(result *RecognizeResult, err error) (*recognize.Result, error) {
	if err != nil {
		return nil, err
	}
	if result == nil || result.Data == nil || len(result.Data.Result) == 0 {
		return nil, errors.New("recognition returned no results")
	}
	songID := result.Data.Result[0].Song.ID
	trackID := fmt.Sprintf("%d", songID)
	return &recognize.Result{
		Platform: "netease",
		TrackID:  trackID,
		URL:      fmt.Sprintf("https://music.163.com/song/%d", songID),
	}, nil
}
