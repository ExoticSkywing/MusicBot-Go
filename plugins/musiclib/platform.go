// Package musiclib adapts the five additional music-lib sources to MusicBot.
package musiclib

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/platform"
	"github.com/liuran001/MusicBot-Go/plugins/musiclib/internal/fivesing"
	"github.com/liuran001/MusicBot-Go/plugins/musiclib/internal/jamendo"
	"github.com/liuran001/MusicBot-Go/plugins/musiclib/internal/joox"
	"github.com/liuran001/MusicBot-Go/plugins/musiclib/internal/migu"
	"github.com/liuran001/MusicBot-Go/plugins/musiclib/internal/model"
	"github.com/liuran001/MusicBot-Go/plugins/musiclib/internal/qianqian"
)

type source interface {
	Search(string) ([]model.Song, error)
	Parse(string) (*model.Song, error)
	GetDownloadURL(*model.Song) (string, error)
	GetLyrics(*model.Song) (string, error)
	ParsePlaylist(string) (*model.Playlist, []model.Song, error)
}

type albumSource interface {
	ParseAlbum(string) (*model.Playlist, []model.Song, error)
}

// Platform shares a transport, but creates a source per operation so request
// contexts and cookies cannot be overwritten by concurrent users.
type Platform struct {
	name    string
	cookie  string
	client  *http.Client
	timeout time.Duration
}

func NewPlatform(name, cookie string, client *http.Client, timeout time.Duration) *Platform {
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	return &Platform{name: name, cookie: strings.TrimSpace(cookie), client: client, timeout: timeout}
}

func (p *Platform) Name() string              { return p.name }
func (p *Platform) SupportsDownload() bool    { return true }
func (p *Platform) SupportsSearch() bool      { return true }
func (p *Platform) SupportsLyrics() bool      { return p.name != "jamendo" }
func (p *Platform) SupportsRecognition() bool { return false }
func (p *Platform) Capabilities() platform.Capabilities {
	return platform.Capabilities{Download: true, Search: true, Lyrics: p.SupportsLyrics()}
}

func (p *Platform) Close() error {
	p.client.CloseIdleConnections()
	return nil
}

func (p *Platform) newSource(ctx context.Context) source {
	switch p.name {
	case "migu":
		return migu.New(ctx, p.cookie, p.client)
	case "qianqian":
		return qianqian.New(ctx, p.cookie, p.client)
	case "fivesing":
		return fivesing.New(ctx, p.cookie, p.client)
	case "jamendo":
		return jamendo.New(ctx, p.cookie, p.client)
	case "joox":
		return joox.New(ctx, p.cookie, p.client)
	default:
		return nil
	}
}

func (p *Platform) wrapError(ctx context.Context, resource, id string, err error) error {
	if ctx.Err() != nil {
		err = ctx.Err()
	} else if errors.Is(err, joox.ErrFullAudioUnavailable) || errors.Is(err, model.ErrPreviewOnly) {
		err = errors.Join(platform.ErrIncompleteAudio, err)
	}
	return &platform.PlatformError{Platform: p.name, Resource: resource, ID: id, Err: err}
}

func (p *Platform) Search(ctx context.Context, query string, limit int) ([]platform.Track, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	api := p.newSource(ctx)
	if api == nil {
		return nil, platform.NewUnsupportedError(p.name, "search")
	}
	songs, err := api.Search(query)
	if err != nil || ctx.Err() != nil {
		return nil, p.wrapError(ctx, "search", "", err)
	}
	tracks := p.convertSongs(songs)
	if limit > 0 && len(tracks) > limit {
		tracks = tracks[:limit]
	}
	return tracks, nil
}

func (p *Platform) parseSong(ctx context.Context, api source, id string) (*model.Song, error) {
	link := p.trackURL(strings.TrimSpace(id))
	if link == "" {
		return nil, platform.NewNotFoundError(p.name, "track", id)
	}
	if api == nil {
		return nil, platform.NewUnsupportedError(p.name, "track")
	}
	song, err := api.Parse(link)
	if err != nil || ctx.Err() != nil {
		return nil, p.wrapError(ctx, "track", id, err)
	}
	if song == nil || song.IsInvalid || strings.TrimSpace(song.Name) == "" {
		return nil, platform.NewNotFoundError(p.name, "track", id)
	}
	return song, nil
}

func (p *Platform) GetTrack(ctx context.Context, trackID string) (*platform.Track, error) {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	song, err := p.parseSong(ctx, p.newSource(ctx), trackID)
	if err != nil {
		return nil, err
	}
	track := p.convertSong(*song)
	if track.ID == "" {
		return nil, platform.NewNotFoundError(p.name, "track", trackID)
	}
	return &track, nil
}

func (p *Platform) GetDownloadInfo(ctx context.Context, trackID string, quality platform.Quality) (*platform.DownloadInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	api := p.newSource(ctx)
	song, err := p.parseSong(ctx, api, trackID)
	if err != nil {
		return nil, err
	}
	// The upstream APIs select the best available rendition. Report the format
	// actually returned; requesting lossless must not relabel an MP3 as lossless.
	downloadURL, err := api.GetDownloadURL(song)
	if err != nil || ctx.Err() != nil {
		return nil, p.wrapError(ctx, "download", trackID, err)
	}
	parsed, err := url.Parse(strings.TrimSpace(downloadURL))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return nil, platform.NewUnavailableError(p.name, "download", trackID)
	}
	format := strings.TrimPrefix(strings.ToLower(path.Ext(parsed.Path)), ".")
	if !audioFormat(format) {
		format = strings.ToLower(strings.TrimPrefix(song.Ext, "."))
	}
	if !audioFormat(format) {
		format = "mp3"
	}
	bitrate := song.Bitrate
	if sourceFormat := strings.ToLower(strings.TrimPrefix(song.Ext, ".")); sourceFormat != "" && sourceFormat != format {
		// A CDN can redirect a requested lossless rendition to an MP3. The
		// source's size-derived bitrate then describes a different file.
		bitrate = 0
	}
	actualQuality := platform.QualityStandard
	if format == "flac" || format == "wav" || format == "alac" {
		actualQuality = platform.QualityLossless
	} else if bitrate >= 192 {
		actualQuality = platform.QualityHigh
	}
	// Some upstream search results estimate size from a different rendition.
	// Let the downloader obtain the length from the selected media response.
	return &platform.DownloadInfo{
		URL: parsed.String(), Format: format, Bitrate: bitrate, Quality: actualQuality,
		Headers: map[string]string{"User-Agent": "Mozilla/5.0", "Referer": p.trackURL(trackID)},
	}, nil
}

func audioFormat(format string) bool {
	switch format {
	case "mp3", "m4a", "aac", "flac", "alac", "wav", "ogg", "opus":
		return true
	}
	return false
}

func (p *Platform) GetLyrics(ctx context.Context, trackID string) (*platform.Lyrics, error) {
	if !p.SupportsLyrics() {
		return nil, platform.NewUnsupportedError(p.name, "lyrics")
	}
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	api := p.newSource(ctx)
	song, err := p.parseSong(ctx, api, trackID)
	if err != nil {
		return nil, err
	}
	raw, err := api.GetLyrics(song)
	if err != nil || ctx.Err() != nil {
		return nil, p.wrapError(ctx, "lyrics", trackID, err)
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, platform.NewUnavailableError(p.name, "lyrics", trackID)
	}
	raw = platform.NormalizeLRCTimestamps(raw)
	return &platform.Lyrics{Plain: raw, Timestamped: platform.ParseLRCTimestampedLines(raw)}, nil
}

func (p *Platform) GetPlaylist(ctx context.Context, playlistID string) (*platform.Playlist, error) {
	isAlbum, id := platform.ParseAlbumCollectionID(playlistID)
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	collection, songs, err := p.parseCollection(ctx, id, isAlbum)
	if err != nil {
		return nil, err
	}
	tracks := p.convertSongs(songs)
	trackCount := max(collection.TrackCount, len(tracks))
	offset := min(platform.PlaylistOffsetFromContext(ctx), len(tracks))
	tracks = tracks[offset:]
	if limit := platform.PlaylistLimitFromContext(ctx); limit > 0 && len(tracks) > limit {
		tracks = tracks[:limit]
	}
	return &platform.Playlist{
		ID: playlistID, Platform: p.name, Title: collection.Name,
		CoverURL: collection.Cover, Description: collection.Description,
		Creator: collection.Creator, TrackCount: trackCount,
		Tracks: tracks, URL: p.collectionURL(id, isAlbum),
	}, nil
}

func (p *Platform) GetAlbum(ctx context.Context, albumID string) (*platform.Album, error) {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	collection, songs, err := p.parseCollection(ctx, albumID, true)
	if err != nil {
		return nil, err
	}
	album := &platform.Album{
		ID: albumID, Platform: p.name, Title: collection.Name,
		CoverURL: collection.Cover, Description: collection.Description,
		TrackCount: max(collection.TrackCount, len(songs)), URL: p.collectionURL(albumID, true),
	}
	if collection.Creator != "" {
		album.Artists = []platform.Artist{{Platform: p.name, Name: collection.Creator}}
	}
	return album, nil
}

func (p *Platform) parseCollection(ctx context.Context, id string, isAlbum bool) (*model.Playlist, []model.Song, error) {
	resource := "playlist"
	if isAlbum {
		resource = "album"
	}
	api := p.newSource(ctx)
	if api == nil || (isAlbum && p.name == "fivesing") {
		return nil, nil, platform.NewUnsupportedError(p.name, resource)
	}
	link := p.collectionURL(strings.TrimSpace(id), isAlbum)
	if link == "" {
		return nil, nil, platform.NewNotFoundError(p.name, resource, id)
	}
	var collection *model.Playlist
	var songs []model.Song
	var err error
	if isAlbum {
		albums, ok := api.(albumSource)
		if !ok {
			return nil, nil, platform.NewUnsupportedError(p.name, resource)
		}
		collection, songs, err = albums.ParseAlbum(link)
	} else {
		collection, songs, err = api.ParsePlaylist(link)
	}
	if err != nil || ctx.Err() != nil {
		return nil, nil, p.wrapError(ctx, resource, id, err)
	}
	if collection == nil {
		return nil, nil, platform.NewNotFoundError(p.name, resource, id)
	}
	return collection, songs, nil
}

func (p *Platform) convertSongs(songs []model.Song) []platform.Track {
	tracks := make([]platform.Track, 0, len(songs))
	for _, song := range songs {
		if song.IsInvalid {
			continue
		}
		track := p.convertSong(song)
		if track.ID != "" && track.Title != "" {
			tracks = append(tracks, track)
		}
	}
	return tracks
}

func (p *Platform) convertSong(song model.Song) platform.Track {
	id, ok := p.MatchURL(song.Link)
	if !ok {
		// Validate the source ID through the same URL rules used by callbacks.
		id, _ = p.MatchURL(p.trackURL(song.ID))
	}
	track := platform.Track{
		ID: id, Platform: p.name, Title: strings.TrimSpace(song.Name),
		Duration: time.Duration(max(song.Duration, 0)) * time.Second,
		CoverURL: song.Cover, URL: p.trackURL(id),
	}
	if strings.TrimSpace(song.Artist) != "" {
		track.Artists = []platform.Artist{{Platform: p.name, Name: song.Artist}}
	}
	if song.Album != "" || song.AlbumID != "" {
		track.Album = &platform.Album{
			ID: song.AlbumID, Platform: p.name, Title: song.Album,
			CoverURL: song.Cover, URL: p.collectionURL(song.AlbumID, true),
		}
	}
	return track
}

func (p *Platform) GetArtist(context.Context, string) (*platform.Artist, error) {
	return nil, platform.NewUnsupportedError(p.name, "artist")
}

func (p *Platform) RecognizeAudio(context.Context, io.Reader) (*platform.Track, error) {
	return nil, platform.NewUnsupportedError(p.name, "audio recognition")
}

var _ platform.Platform = (*Platform)(nil)
var _ platform.PlaylistURLMatcher = (*Platform)(nil)
var _ platform.MetadataProvider = (*Platform)(nil)
