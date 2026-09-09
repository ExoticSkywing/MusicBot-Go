package handler

import (
	"context"
	"errors"
	"reflect"
	"testing"

	botpkg "github.com/liuran001/MusicBot-Go/bot"
	"github.com/liuran001/MusicBot-Go/bot/i18n"
	"github.com/liuran001/MusicBot-Go/bot/platform"
)

type metadataTestRepo struct {
	*stubSongRepository
	variants map[string]botpkg.LocalizedSongMetadata
}

func (r *metadataTestRepo) FindLocalizedSongMetadata(_ context.Context, p, id, l string) (*botpkg.LocalizedSongMetadata, error) {
	m, ok := r.variants[p+":"+id+":"+l]
	if !ok {
		return nil, nil
	}
	return &m, nil
}
func (r *metadataTestRepo) SaveLocalizedSongMetadata(_ context.Context, m *botpkg.LocalizedSongMetadata) error {
	k := m.Platform + ":" + m.TrackID + ":" + m.Language
	if _, ok := r.variants[k]; !ok {
		r.variants[k] = *m
	}
	return nil
}

type metadataTestPlatform struct {
	*stubPlatform
	calls int
	fail  bool
}

func (p *metadataTestPlatform) LocalizeTrack(ctx context.Context, track *platform.Track) (*platform.Track, error) {
	p.calls++
	if p.fail {
		return nil, errors.New("unavailable")
	}
	out := *track
	out.Title = "晴天"
	out.MetadataLanguage = i18n.From(ctx).Lang()
	out.Artists = []platform.Artist{{Name: "周杰伦"}}
	out.Album = &platform.Album{Title: "叶惠美"}
	return &out, nil
}
func TestLocalizeOldCachedSongPreservesMediaAndReusesLanguage(t *testing.T) {
	repo := &metadataTestRepo{variants: map[string]botpkg.LocalizedSongMetadata{}}
	p := &metadataTestPlatform{stubPlatform: newStubPlatform("applemusic")}
	manager := newStubManager()
	manager.Register(p)
	yes := true
	old := botpkg.SongInfo{Platform: "applemusic", TrackID: "1721464906", SongName: "Sunny Day", SongArtists: "Jay Chou", SongAlbum: "Yeh, Hwei-Mei", FileID: "audio", ThumbFileID: "thumb", Quality: "lossless", AudioValidated: true, TrackURL: "tr-url", SongArtistsIDs: "old-artist", AlbumID: 123, LyricsAvailable: &yes}
	song := old
	localizeCachedSong(zhCtx(), manager, repo, &song)
	if song.SongName != "晴天" || song.MetadataLanguage != "zh" || p.calls != 1 {
		t.Fatalf("song=%+v calls=%d", song, p.calls)
	}
	expected := old
	expected.SongName = "晴天"
	expected.SongArtists = "周杰伦"
	expected.SongAlbum = "叶惠美"
	expected.MetadataLanguage = "zh"
	if !reflect.DeepEqual(song, expected) {
		t.Fatalf("non-name metadata changed: %+v", song)
	}
	localizeCachedSong(zhCtx(), manager, repo, &song)
	again := old
	localizeCachedSong(zhCtx(), manager, repo, &again)
	if p.calls != 1 || !reflect.DeepEqual(again, expected) {
		t.Fatalf("localized cache not reused: %+v calls=%d", again, p.calls)
	}
	// A saved language is preferred even when a different language is in the audio row.
	repo.variants["applemusic:1721464906:en"] = botpkg.LocalizedSongMetadata{Platform: old.Platform, TrackID: old.TrackID, Language: "en", SongName: "Saved English", SongArtists: "Saved Artist", SongAlbum: "Saved Album"}
	en := i18n.WithLocalizer(context.Background(), i18n.For("en"))
	localizeCachedSong(en, manager, repo, &song)
	if song.SongName != "Saved English" || p.calls != 1 {
		t.Fatal("existing language overwritten")
	}
}
func TestLocalizeCachedSongFailureKeepsOriginal(t *testing.T) {
	p := &metadataTestPlatform{stubPlatform: newStubPlatform("applemusic"), fail: true}
	manager := newStubManager()
	manager.Register(p)
	song := botpkg.SongInfo{Platform: "applemusic", TrackID: "1", SongName: "Original", FileID: "audio"}
	before := song
	localizeCachedSong(zhCtx(), manager, nil, &song)
	if !reflect.DeepEqual(before, song) {
		t.Fatal("failure changed cache")
	}
}
func TestMetadataRequestKeyLanguageIsolation(t *testing.T) {
	en := i18n.WithLocalizer(context.Background(), i18n.For("en"))
	if metadataRequestKey(en, "applemusic", "x") == metadataRequestKey(zhCtx(), "applemusic", "x") {
		t.Fatal("Apple languages share key")
	}
	if metadataRequestKey(en, "netease", "x") != "x" {
		t.Fatal("other platform changed")
	}
}
