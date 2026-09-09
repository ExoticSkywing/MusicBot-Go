package db

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/liuran001/MusicBot-Go/bot"
)

func TestLocalizedAudioPreservesStandaloneCoverWithoutLocalPath(t *testing.T) {
	for _, writePath := range []string{"create", "save_localized"} {
		t.Run(writePath, func(t *testing.T) {
			repo := newTempRepo(t)
			ctx := context.Background()
			write := repo.Create
			if writePath == "save_localized" {
				write = repo.SaveLocalizedAudio
			}

			variants := []*bot.SongInfo{
				appleAudio("535824738", "lossless", "zh", "audio-zh"),
				appleAudio("535824738", "lossless", "en", "audio-en"),
			}
			for _, song := range variants {
				song.CoverFileID = "cover-" + song.AudioLanguage
				song.CoverURL = "https://img.example/" + song.AudioLanguage + ".jpg"
				song.CoverLocalPath = "/tmp/music-cover-" + song.AudioLanguage + ".jpg"
				if err := write(ctx, song); err != nil {
					t.Fatalf("save %s audio: %v", song.AudioLanguage, err)
				}
				if song.CoverLocalPath == "" {
					t.Fatal("saving must not clear the active preparation's local cover")
				}
			}

			for _, want := range variants {
				// The first language must keep its cover after the primary cache
				// row is overwritten by the second language.
				got, err := repo.FindLocalizedAudio(ctx, want.Platform, want.TrackID, want.Quality, want.AudioLanguage)
				if err != nil {
					t.Fatal(err)
				}
				assertLocalizedCover(t, got, want)
				byFileID, err := repo.FindByFileID(ctx, want.FileID)
				if err != nil {
					t.Fatal(err)
				}
				assertLocalizedCover(t, byFileID, want)

				var model LocalizedAudioModel
				if err := repo.cacheDB.Where("file_id = ?", want.FileID).First(&model).Error; err != nil {
					t.Fatal(err)
				}
				var payload map[string]json.RawMessage
				if err := json.Unmarshal(model.SongInfoJSON, &payload); err != nil {
					t.Fatal(err)
				}
				if _, persisted := payload["CoverLocalPath"]; persisted {
					t.Fatal("localized audio JSON must not persist a temporary cover path")
				}
			}
		})
	}
}

func assertLocalizedCover(t *testing.T, got, want *bot.SongInfo) {
	t.Helper()
	if got == nil {
		t.Fatal("missing localized audio")
	}
	if got.FileID != want.FileID || got.CoverFileID != want.CoverFileID || got.CoverURL != want.CoverURL {
		t.Fatalf("localized cover = (%q, %q, %q), want (%q, %q, %q)",
			got.FileID, got.CoverFileID, got.CoverURL, want.FileID, want.CoverFileID, want.CoverURL)
	}
	if got.CoverLocalPath != "" {
		t.Fatalf("cached local cover path = %q, want empty", got.CoverLocalPath)
	}
}
