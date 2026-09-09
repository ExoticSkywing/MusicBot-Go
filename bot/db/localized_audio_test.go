package db

import (
	"context"
	"testing"

	"github.com/liuran001/MusicBot-Go/bot"
)

func appleAudio(trackID, quality, language, fileID string) *bot.SongInfo {
	return &bot.SongInfo{
		Platform:       "applemusic",
		TrackID:        trackID,
		Quality:        quality,
		SongName:       "Song " + language,
		SongArtists:    "Artist " + language,
		SongAlbum:      "Album " + language,
		FileID:         fileID,
		AudioValidated: true,
		AudioLanguage:  language,
	}
}

func TestLocalizedAudioSurvivesPrimaryLanguageOverwrite(t *testing.T) {
	repo := newTempRepo(t)
	ctx := context.Background()

	zh := appleAudio("535824738", "lossless", "zh", "file-zh")
	if err := repo.Create(ctx, zh); err != nil {
		t.Fatalf("create Chinese audio: %v", err)
	}
	en := appleAudio(zh.TrackID, zh.Quality, "en", "file-en")
	if err := repo.Create(ctx, en); err != nil {
		t.Fatalf("overwrite primary with English audio: %v", err)
	}

	for _, want := range []*bot.SongInfo{zh, en} {
		got, err := repo.FindLocalizedAudio(ctx, want.Platform, want.TrackID, want.Quality, want.AudioLanguage)
		if err != nil {
			t.Fatalf("find %s audio: %v", want.AudioLanguage, err)
		}
		if got == nil || got.FileID != want.FileID || got.AudioLanguage != want.AudioLanguage || got.ID != 0 {
			t.Fatalf("unexpected %s audio: %+v", want.AudioLanguage, got)
		}
	}
	primary, err := repo.FindByPlatformTrackID(ctx, en.Platform, en.TrackID, en.Quality)
	if err != nil {
		t.Fatalf("find primary audio: %v", err)
	}
	if primary.FileID != en.FileID || primary.AudioLanguage != "en" {
		t.Fatalf("primary audio = %+v", primary)
	}
	byFileID, err := repo.FindByFileID(ctx, zh.FileID)
	if err != nil {
		t.Fatalf("find sidecar by file ID: %v", err)
	}
	if byFileID.FileID != zh.FileID || byFileID.ID != 0 {
		t.Fatalf("sidecar file lookup = %+v", byFileID)
	}
}

func TestLocalizedAudioPreservesUnknownLegacyLanguage(t *testing.T) {
	repo := newTempRepo(t)
	ctx := context.Background()
	legacy := appleAudio("1721464906", "high", "", "legacy-file")
	legacy.SongName = "Legacy"
	if err := repo.Create(ctx, legacy); err != nil {
		t.Fatalf("create legacy audio: %v", err)
	}
	localized := appleAudio(legacy.TrackID, legacy.Quality, "zh", "file-zh")
	if err := repo.Create(ctx, localized); err != nil {
		t.Fatalf("overwrite legacy primary: %v", err)
	}

	got, err := repo.FindLocalizedAudio(ctx, legacy.Platform, legacy.TrackID, legacy.Quality, "")
	if err != nil {
		t.Fatalf("find legacy audio: %v", err)
	}
	if got == nil || got.FileID != legacy.FileID || got.AudioLanguage != "" {
		t.Fatalf("legacy audio not preserved: %+v", got)
	}
}

func TestLocalizedAudioSameLanguageReplacesInvalidFile(t *testing.T) {
	repo := newTempRepo(t)
	ctx := context.Background()
	old := appleAudio("535824738", "high", "zh", "invalid-file")
	if err := repo.SaveLocalizedAudio(ctx, old); err != nil {
		t.Fatalf("save old audio: %v", err)
	}
	replacement := appleAudio(old.TrackID, old.Quality, old.AudioLanguage, "replacement-file")
	if err := repo.SaveLocalizedAudio(ctx, replacement); err != nil {
		t.Fatalf("save replacement audio: %v", err)
	}
	got, err := repo.FindLocalizedAudio(ctx, old.Platform, old.TrackID, old.Quality, old.AudioLanguage)
	if err != nil {
		t.Fatalf("find replacement: %v", err)
	}
	if got == nil || got.FileID != replacement.FileID {
		t.Fatalf("replacement not stored: %+v", got)
	}
}

func TestDeleteLocalizedAudioOnlyClearsSelectedVariant(t *testing.T) {
	repo := newTempRepo(t)
	ctx := context.Background()
	zh := appleAudio("535824738", "high", "zh", "file-zh")
	en := appleAudio(zh.TrackID, zh.Quality, "en", "file-en")
	if err := repo.Create(ctx, en); err != nil {
		t.Fatalf("create primary English audio: %v", err)
	}
	if err := repo.SaveLocalizedAudio(ctx, zh); err != nil {
		t.Fatalf("save Chinese audio: %v", err)
	}
	if err := repo.DeleteLocalizedAudio(ctx, zh.Platform, zh.TrackID, zh.Quality, zh.AudioLanguage); err != nil {
		t.Fatalf("delete Chinese audio: %v", err)
	}
	if got, err := repo.FindLocalizedAudio(ctx, zh.Platform, zh.TrackID, zh.Quality, zh.AudioLanguage); err != nil || got != nil {
		t.Fatalf("deleted variant = %+v, %v", got, err)
	}
	if got, err := repo.FindLocalizedAudio(ctx, en.Platform, en.TrackID, en.Quality, en.AudioLanguage); err != nil || got == nil || got.FileID != en.FileID {
		t.Fatalf("English variant changed: %+v, %v", got, err)
	}
	if primary, err := repo.FindByPlatformTrackID(ctx, en.Platform, en.TrackID, en.Quality); err != nil || primary == nil || primary.FileID != en.FileID {
		t.Fatalf("non-selected primary changed: %+v, %v", primary, err)
	}
	if err := repo.DeleteLocalizedAudio(ctx, en.Platform, en.TrackID, en.Quality, en.AudioLanguage); err != nil {
		t.Fatalf("delete English audio: %v", err)
	}
	if primary, err := repo.FindByPlatformTrackID(ctx, en.Platform, en.TrackID, en.Quality); err == nil || primary != nil {
		t.Fatalf("selected primary still present: %+v, %v", primary, err)
	}
}

func TestLocalizedAudioIsolatedByQuality(t *testing.T) {
	repo := newTempRepo(t)
	ctx := context.Background()
	high := appleAudio("535824738", "high", "zh", "file-high")
	lossless := appleAudio(high.TrackID, "lossless", high.AudioLanguage, "file-lossless")
	for _, song := range []*bot.SongInfo{high, lossless} {
		if err := repo.SaveLocalizedAudio(ctx, song); err != nil {
			t.Fatalf("save %s audio: %v", song.Quality, err)
		}
	}
	for _, want := range []*bot.SongInfo{high, lossless} {
		got, err := repo.FindLocalizedAudio(ctx, want.Platform, want.TrackID, want.Quality, want.AudioLanguage)
		if err != nil || got == nil || got.FileID != want.FileID {
			t.Fatalf("%s audio = %+v, %v", want.Quality, got, err)
		}
	}
}

func TestFindCachedAudioSourceUsesRemainingVariantAfterPrimaryInvalidation(t *testing.T) {
	repo := newTempRepo(t)
	ctx := context.Background()
	zh := appleAudio("535824738", "high", "zh", "file-zh")
	en := appleAudio(zh.TrackID, zh.Quality, "en", "file-en")
	if err := repo.Create(ctx, zh); err != nil {
		t.Fatalf("create primary Chinese audio: %v", err)
	}
	if err := repo.SaveLocalizedAudio(ctx, en); err != nil {
		t.Fatalf("save English variant: %v", err)
	}
	if err := repo.DeleteLocalizedAudio(ctx, zh.Platform, zh.TrackID, zh.Quality, zh.AudioLanguage); err != nil {
		t.Fatalf("invalidate primary Chinese audio: %v", err)
	}

	got, err := repo.FindCachedAudioSource(ctx, zh.Platform, zh.TrackID, zh.Quality)
	if err != nil {
		t.Fatalf("find remaining source: %v", err)
	}
	if got == nil || got.FileID != en.FileID || got.AudioLanguage != en.AudioLanguage || got.ID != 0 {
		t.Fatalf("remaining source = %+v", got)
	}
}
