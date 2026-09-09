package handler

import (
	"context"
	"strings"
	"time"

	botpkg "github.com/liuran001/MusicBot-Go/bot"
	"github.com/liuran001/MusicBot-Go/bot/i18n"
	"github.com/liuran001/MusicBot-Go/bot/platform"
)

type localizedMetadataRepository interface {
	FindLocalizedSongMetadata(context.Context, string, string, string) (*botpkg.LocalizedSongMetadata, error)
	SaveLocalizedSongMetadata(context.Context, *botpkg.LocalizedSongMetadata) error
}

// metadataRequestKey keeps names and embedded tags isolated between bot languages.
func metadataRequestKey(ctx context.Context, platformName, key string) string {
	if isAppleMusicPlatform(platformName) {
		return key + ":lang:" + i18n.From(ctx).Lang()
	}
	return key
}

// localizeCachedSong overlays display names only. The cached audio, playback IDs,
// links and lyrics remain untouched; old cache entries are upgraded on demand.
func localizeCachedSong(ctx context.Context, manager platform.Manager, repo botpkg.SongRepository, song *botpkg.SongInfo) {
	if song == nil || !isAppleMusicPlatform(song.Platform) || song.TrackID == "" {
		return
	}
	lang := i18n.From(ctx).Lang()
	// Compare with the persisted file snapshot, not a display-only overlay from
	// an earlier call. Existing downloads wrote these cached names into the file.
	defer func() {
		storage, ok := repo.(localizedAudioRepository)
		if !ok || song.MetadataLanguage != lang || song.AudioLanguage == lang || song.FileID == "" || !isReusableCachedSong(song, song.Platform, song.Quality) {
			return
		}
		original, err := repo.FindByFileID(ctx, song.FileID)
		if err != nil || original == nil || original.SongName != song.SongName || original.SongArtists != song.SongArtists || original.SongAlbum != song.SongAlbum {
			return
		}
		marked := *song
		marked.AudioLanguage = lang
		if storage.SaveLocalizedAudio(ctx, &marked) == nil {
			song.AudioLanguage = lang
		}
	}()
	if song.MetadataLanguage == lang {
		return
	}
	storage, _ := repo.(localizedMetadataRepository)
	if storage != nil {
		if meta, err := storage.FindLocalizedSongMetadata(ctx, song.Platform, song.TrackID, lang); err == nil && meta != nil {
			applyLocalizedSongMetadata(song, meta)
			return
		}
	}
	if manager == nil {
		return
	}
	localizer, ok := manager.Get(song.Platform).(interface {
		LocalizeTrack(context.Context, *platform.Track) (*platform.Track, error)
	})
	if !ok {
		return
	}
	track := &platform.Track{Platform: song.Platform, ID: song.TrackID, Title: song.SongName, MetadataLanguage: song.MetadataLanguage,
		Album: &platform.Album{Title: song.SongAlbum}, Duration: time.Duration(song.Duration) * time.Second}
	artistIDs := strings.Split(song.SongArtistsIDs, ",")
	for i, name := range strings.Split(song.SongArtists, "/") {
		artist := platform.Artist{Name: name}
		if i < len(artistIDs) {
			artist.ID = artistIDs[i]
		}
		track.Artists = append(track.Artists, artist)
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	localized, err := localizer.LocalizeTrack(lookupCtx, track)
	if err != nil || localized == nil || localized.MetadataLanguage != lang {
		return
	}
	meta := &botpkg.LocalizedSongMetadata{Platform: song.Platform, TrackID: song.TrackID, Language: lang, SongName: localized.Title}
	names := make([]string, 0, len(localized.Artists))
	for _, artist := range localized.Artists {
		names = append(names, artist.Name)
	}
	meta.SongArtists = strings.Join(names, "/")
	if localized.Album != nil {
		meta.SongAlbum = localized.Album.Title
	}
	if storage != nil {
		_ = storage.SaveLocalizedSongMetadata(ctx, meta)
		// A concurrent request may already have saved this language. Preserve it.
		if saved, err := storage.FindLocalizedSongMetadata(ctx, song.Platform, song.TrackID, lang); err == nil && saved != nil {
			meta = saved
		}
	}
	applyLocalizedSongMetadata(song, meta)
}

func applyLocalizedSongMetadata(song *botpkg.SongInfo, meta *botpkg.LocalizedSongMetadata) {
	if meta.SongName != "" {
		song.SongName = meta.SongName
	}
	if meta.SongArtists != "" {
		song.SongArtists = meta.SongArtists
	}
	if meta.SongAlbum != "" {
		song.SongAlbum = meta.SongAlbum
	}
	song.MetadataLanguage = meta.Language
}
