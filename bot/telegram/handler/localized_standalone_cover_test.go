package handler

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/db"
	"gorm.io/gorm/logger"
)

func TestSendMusicPersistsLocalizedStandaloneCover(t *testing.T) {
	bot, recorder := newStandaloneCoverTestBot(t, false)
	dir := t.TempDir()
	repo, err := db.NewSQLiteRepository(filepath.Join(dir, "cache.db"), filepath.Join(dir, "data.db"), logger.Default.LogMode(logger.Silent))
	if err != nil {
		t.Fatal(err)
	}
	ctx := zhCtx()
	source := standaloneCoverTestSong()
	source.Platform = "applemusic"
	source.MetadataLanguage = "en"
	source.AudioLanguage = "en"
	if err := repo.Create(ctx, source); err != nil {
		t.Fatal(err)
	}
	song := *source
	song.ID = 0
	song.FileID = "audio-zh"
	song.MetadataLanguage = "zh"
	song.AudioLanguage = "zh"

	workerCtx, cancelWorker := context.WithCancel(context.Background())
	h := &MusicHandler{Repo: repo, CacheDir: dir, EnableStandaloneCover: true, UploadWorkerCount: 1, UploadQueueSize: 2}
	h.StartWorker(workerCtx)
	t.Cleanup(func() {
		cancelWorker()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = h.ShutdownUploads(shutdownCtx)
	})
	if err := h.sendMusic(ctx, bot, nil, standaloneCoverTestMessage(), &song, "", "", nil, nil, song.Platform, song.TrackID); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		stored, err := repo.FindLocalizedAudio(ctx, song.Platform, song.TrackID, song.Quality, "zh")
		if err != nil {
			t.Fatal(err)
		}
		if stored != nil && stored.FileID == "audio-new" {
			if stored.CoverFileID != "cover-large" || stored.CoverURL != song.CoverURL {
				t.Fatalf("localized upload lost standalone cover: %+v", stored)
			}
			original, err := repo.FindLocalizedAudio(ctx, source.Platform, source.TrackID, source.Quality, "en")
			if err != nil || original == nil || original.FileID != source.FileID || original.CoverFileID != source.CoverFileID {
				t.Fatalf("original language audio or cover changed: %+v, %v", original, err)
			}
			methods, _ := recorder.snapshot()
			if got := strings.Join(methods, ","); got != "sendPhoto,sendAudio" {
				t.Fatalf("Telegram methods = %q, want cover before audio", got)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("localized audio and standalone cover were not persisted")
}
