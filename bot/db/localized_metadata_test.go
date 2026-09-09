package db

import (
	"context"
	"testing"

	"github.com/liuran001/MusicBot-Go/bot"
)

func TestLocalizedSongMetadataKeepsLanguageVariants(t *testing.T) {
	repo := newTempRepo(t)
	ctx := context.Background()

	variants := []*bot.LocalizedSongMetadata{
		{Platform: "applemusic", TrackID: "535824738", Language: "zh", SongName: "晴天", SongArtists: "周杰伦", SongAlbum: "叶惠美"},
		{Platform: "applemusic", TrackID: "535824738", Language: "en", SongName: "Sunny Day", SongArtists: "Jay Chou", SongAlbum: "Yeh, Hwei-Mei"},
	}
	for _, variant := range variants {
		if err := repo.SaveLocalizedSongMetadata(ctx, variant); err != nil {
			t.Fatalf("save %s metadata: %v", variant.Language, err)
		}
	}
	for _, want := range variants {
		got, err := repo.FindLocalizedSongMetadata(ctx, want.Platform, want.TrackID, want.Language)
		if err != nil {
			t.Fatalf("find %s metadata: %v", want.Language, err)
		}
		if got == nil || got.SongName != want.SongName || got.SongArtists != want.SongArtists || got.SongAlbum != want.SongAlbum {
			t.Fatalf("unexpected %s metadata: %+v", want.Language, got)
		}
	}
}

func TestLocalizedSongMetadataDoesNotOverwriteSameLanguage(t *testing.T) {
	repo := newTempRepo(t)
	ctx := context.Background()
	original := &bot.LocalizedSongMetadata{
		Platform: "applemusic", TrackID: "535824738", Language: "zh",
		SongName: "晴天", SongArtists: "周杰伦", SongAlbum: "叶惠美",
	}
	if err := repo.SaveLocalizedSongMetadata(ctx, original); err != nil {
		t.Fatalf("save original metadata: %v", err)
	}
	changed := *original
	changed.SongName = "不应覆盖"
	if err := repo.SaveLocalizedSongMetadata(ctx, &changed); err != nil {
		t.Fatalf("save duplicate metadata: %v", err)
	}

	got, err := repo.FindLocalizedSongMetadata(ctx, original.Platform, original.TrackID, original.Language)
	if err != nil {
		t.Fatalf("find metadata: %v", err)
	}
	if got == nil || got.SongName != original.SongName {
		t.Fatalf("same-language metadata was overwritten: %+v", got)
	}
}

func TestSongInfoMetadataLanguagePersistsAndSnapshotsOnCreateAndUpdate(t *testing.T) {
	repo := newTempRepo(t)
	ctx := context.Background()
	song := &bot.SongInfo{
		Platform: "applemusic", TrackID: "535824738", Quality: "high",
		SongName: "晴天", SongArtists: "周杰伦", SongAlbum: "叶惠美",
		MetadataLanguage: "zh", FileID: "telegram-file", AudioValidated: true,
	}
	if err := repo.Create(ctx, song); err != nil {
		t.Fatalf("create song: %v", err)
	}
	loaded, err := repo.FindByPlatformTrackID(ctx, song.Platform, song.TrackID, song.Quality)
	if err != nil {
		t.Fatalf("find created song: %v", err)
	}
	if loaded.MetadataLanguage != "zh" {
		t.Fatalf("metadata language = %q, want zh", loaded.MetadataLanguage)
	}
	zh, err := repo.FindLocalizedSongMetadata(ctx, song.Platform, song.TrackID, "zh")
	if err != nil || zh == nil || zh.SongName != "晴天" {
		t.Fatalf("create snapshot = %+v, %v", zh, err)
	}

	loaded.MetadataLanguage = "en"
	loaded.SongName = "Sunny Day"
	loaded.SongArtists = "Jay Chou"
	loaded.SongAlbum = "Yeh, Hwei-Mei"
	if err := repo.Update(ctx, loaded); err != nil {
		t.Fatalf("update song: %v", err)
	}
	en, err := repo.FindLocalizedSongMetadata(ctx, song.Platform, song.TrackID, "en")
	if err != nil || en == nil || en.SongName != "Sunny Day" {
		t.Fatalf("update snapshot = %+v, %v", en, err)
	}
	zh, err = repo.FindLocalizedSongMetadata(ctx, song.Platform, song.TrackID, "zh")
	if err != nil || zh == nil || zh.SongName != "晴天" {
		t.Fatalf("prior language snapshot changed: %+v, %v", zh, err)
	}
}

func TestLocalizedSongMetadataCanBeRecreatedAfterCacheCleanup(t *testing.T) {
	repo := newTempRepo(t)
	ctx := context.Background()
	original := &bot.LocalizedSongMetadata{
		Platform: "applemusic", TrackID: "535824738", Language: "zh", SongName: "晴天",
	}
	if err := repo.SaveLocalizedSongMetadata(ctx, original); err != nil {
		t.Fatalf("save original metadata: %v", err)
	}
	if err := repo.DeleteAllQualitiesByPlatformTrackID(ctx, original.Platform, original.TrackID); err != nil {
		t.Fatalf("delete track cache: %v", err)
	}

	recreated := *original
	recreated.SongName = "重新缓存"
	if err := repo.SaveLocalizedSongMetadata(ctx, &recreated); err != nil {
		t.Fatalf("recreate metadata: %v", err)
	}
	got, err := repo.FindLocalizedSongMetadata(ctx, recreated.Platform, recreated.TrackID, recreated.Language)
	if err != nil {
		t.Fatalf("find recreated metadata: %v", err)
	}
	if got == nil || got.SongName != recreated.SongName {
		t.Fatalf("recreated metadata = %+v", got)
	}
}
