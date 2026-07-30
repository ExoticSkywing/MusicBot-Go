package kuwo

import (
	"context"
	"io"

	"github.com/liuran001/MusicBot-Go/bot/platform"
)

// KuwoPlatform adapts the Kuwo client to the shared music platform contract.
type KuwoPlatform struct {
	client *Client
}

// NewPlatform creates a Kuwo platform backed by client.
func NewPlatform(client *Client) *KuwoPlatform {
	return &KuwoPlatform{client: client}
}

func (p *KuwoPlatform) Name() string {
	return "kuwo"
}

func (p *KuwoPlatform) SupportsDownload() bool {
	return true
}

func (p *KuwoPlatform) SupportsSearch() bool {
	return true
}

func (p *KuwoPlatform) SupportsLyrics() bool {
	return true
}

func (p *KuwoPlatform) SupportsRecognition() bool {
	return false
}

func (p *KuwoPlatform) Capabilities() platform.Capabilities {
	return platform.Capabilities{
		Download:    true,
		Search:      true,
		Lyrics:      true,
		Recognition: false,
		HiRes:       true,
	}
}

func (p *KuwoPlatform) Metadata() platform.Meta {
	return platform.Meta{
		Name:          "kuwo",
		DisplayName:   "酷我",
		Emoji:         "🎧",
		Aliases:       []string{"kuwo", "kw", "酷我", "酷我音乐"},
		AllowGroupURL: true,
		GroupURLHosts: []string{"kuwo.cn", "www.kuwo.cn", "m.kuwo.cn"},
	}
}

func (p *KuwoPlatform) GetDownloadInfo(
	ctx context.Context,
	trackID string,
	quality platform.Quality,
) (*platform.DownloadInfo, error) {
	if p == nil || p.client == nil {
		return nil, platform.NewUnavailableError("kuwo", "track", trackID)
	}
	return p.client.GetDownloadInfo(ctx, trackID, quality)
}

func (p *KuwoPlatform) Search(ctx context.Context, query string, limit int) ([]platform.Track, error) {
	if p == nil || p.client == nil {
		return nil, platform.NewUnavailableError("kuwo", "search", "")
	}
	return p.client.Search(ctx, query, limit)
}

func (p *KuwoPlatform) GetLyrics(ctx context.Context, trackID string) (*platform.Lyrics, error) {
	if p == nil || p.client == nil {
		return nil, platform.NewUnavailableError("kuwo", "lyrics", trackID)
	}
	return p.client.GetLyrics(ctx, trackID)
}

func (p *KuwoPlatform) RecognizeAudio(context.Context, io.Reader) (*platform.Track, error) {
	return nil, platform.NewUnsupportedError("kuwo", "audio recognition")
}

func (p *KuwoPlatform) GetTrack(ctx context.Context, trackID string) (*platform.Track, error) {
	if p == nil || p.client == nil {
		return nil, platform.NewUnavailableError("kuwo", "track", trackID)
	}
	return p.client.GetTrack(ctx, trackID)
}

func (p *KuwoPlatform) GetArtist(context.Context, string) (*platform.Artist, error) {
	return nil, platform.NewUnsupportedError("kuwo", "get artist")
}

func (p *KuwoPlatform) GetAlbum(context.Context, string) (*platform.Album, error) {
	return nil, platform.NewUnsupportedError("kuwo", "get album")
}

func (p *KuwoPlatform) GetPlaylist(ctx context.Context, playlistID string) (*platform.Playlist, error) {
	if p == nil || p.client == nil {
		return nil, platform.NewUnavailableError("kuwo", "playlist", playlistID)
	}
	offset := platform.PlaylistOffsetFromContext(ctx)
	limit := platform.PlaylistLimitFromContext(ctx)
	return p.client.GetPlaylist(ctx, playlistID, offset, limit)
}

func (p *KuwoPlatform) MatchURL(rawURL string) (string, bool) {
	return NewURLMatcher().MatchURL(rawURL)
}

func (p *KuwoPlatform) MatchPlaylistURL(rawURL string) (string, bool) {
	return NewURLMatcher().MatchPlaylistURL(rawURL)
}

func (p *KuwoPlatform) MatchText(text string) (string, bool) {
	return NewTextMatcher().MatchText(text)
}
