package douyin

import (
	"context"
	"io"

	"github.com/liuran001/MusicBot-Go/bot/platform"
)

// DouyinPlatform 提供抖音原声（www.douyin.com/music/<id>）的解析与下载。
// 抖音没有可匿名调用的免签名搜索接口，因此只支持链接解析。
type DouyinPlatform struct {
	client *Client
}

func NewPlatform(client *Client) *DouyinPlatform {
	return &DouyinPlatform{client: client}
}

func (p *DouyinPlatform) Name() string { return platformName }

func (p *DouyinPlatform) SupportsDownload() bool { return true }

func (p *DouyinPlatform) SupportsSearch() bool { return false }

func (p *DouyinPlatform) SupportsLyrics() bool { return false }

func (p *DouyinPlatform) SupportsRecognition() bool { return false }

func (p *DouyinPlatform) Capabilities() platform.Capabilities {
	return platform.Capabilities{Download: true}
}

func (p *DouyinPlatform) Metadata() platform.Meta {
	return platform.Meta{
		Name:          platformName,
		DisplayName:   "抖音",
		Emoji:         "🎼",
		Aliases:       []string{"douyin", "dy", "抖音"},
		AllowGroupURL: true,
		GroupURLHosts: []string{"douyin.com", "iesdouyin.com"},
	}
}

func (p *DouyinPlatform) GetDownloadInfo(ctx context.Context, trackID string, quality platform.Quality) (*platform.DownloadInfo, error) {
	if p == nil || p.client == nil {
		return nil, platform.NewUnavailableError(platformName, "track", trackID)
	}
	return p.client.FetchDownloadInfo(ctx, trackID, quality)
}

func (p *DouyinPlatform) GetTrack(ctx context.Context, trackID string) (*platform.Track, error) {
	if p == nil || p.client == nil {
		return nil, platform.NewUnavailableError(platformName, "track", trackID)
	}
	return p.client.GetTrack(ctx, trackID)
}

func (p *DouyinPlatform) Search(context.Context, string, int) ([]platform.Track, error) {
	return nil, platform.NewUnsupportedError(platformName, "search")
}

func (p *DouyinPlatform) GetLyrics(context.Context, string) (*platform.Lyrics, error) {
	return nil, platform.NewUnsupportedError(platformName, "lyrics")
}

func (p *DouyinPlatform) RecognizeAudio(context.Context, io.Reader) (*platform.Track, error) {
	return nil, platform.NewUnsupportedError(platformName, "audio recognition")
}

func (p *DouyinPlatform) GetArtist(context.Context, string) (*platform.Artist, error) {
	return nil, platform.NewUnsupportedError(platformName, "artist")
}

func (p *DouyinPlatform) GetAlbum(context.Context, string) (*platform.Album, error) {
	return nil, platform.NewUnsupportedError(platformName, "album")
}

func (p *DouyinPlatform) GetPlaylist(context.Context, string) (*platform.Playlist, error) {
	return nil, platform.NewUnsupportedError(platformName, "playlist")
}

func (p *DouyinPlatform) MatchURL(rawURL string) (string, bool) {
	return NewURLMatcher().MatchURL(rawURL)
}

func (p *DouyinPlatform) MatchText(text string) (string, bool) {
	return NewTextMatcher().MatchText(text)
}

// ShortLinkHosts 声明 App 分享出来的 v.douyin.com 短链，解析后再走 MatchURL。
func (p *DouyinPlatform) ShortLinkHosts() []string {
	return []string{"v.douyin.com"}
}

func (p *DouyinPlatform) Close() error {
	if p != nil && p.client != nil {
		p.client.Close()
	}
	return nil
}
