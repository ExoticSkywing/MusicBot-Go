package douyin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/platform"
	"github.com/liuran001/MusicBot-Go/bot/util"
)

const (
	platformName = "douyin"

	douyinUserAgent      = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36"
	douyinMusicDetailURL = "https://www.douyin.com/aweme/v1/web/music/detail/"
	douyinWebAid         = "6383"
	defaultTimeout       = 15 * time.Second

	// 原声的 play_url 是不带签名的 CDN 对象地址，短时缓存可以让同一次下载里
	// GetTrack 与 GetDownloadInfo 共用一次详情请求。
	douyinMusicCacheTTL  = 10 * time.Minute
	douyinMusicCacheSize = 1024
	douyinMaxBodyBytes   = 4 << 20

	// 抖音原声 CDN 目前统一转码为 128kbps MP3，实际码率由下载后探测覆盖。
	douyinNominalBitrate = 128
)

type Client struct {
	httpClient *http.Client
	detailURL  string
	cache      *util.TTLCache[*douyinMusic]
}

type douyinMusicDetailResponse struct {
	StatusCode int          `json:"status_code"`
	StatusMsg  string       `json:"status_msg"`
	MusicInfo  *douyinMusic `json:"music_info"`
}

type douyinURLList struct {
	URI     string   `json:"uri"`
	URLList []string `json:"url_list"`
}

type douyinMusic struct {
	IDStr           string        `json:"id_str"`
	MID             string        `json:"mid"`
	Title           string        `json:"title"`
	Author          string        `json:"author"`
	OwnerNickname   string        `json:"owner_nickname"`
	SecUID          string        `json:"sec_uid"`
	Duration        int           `json:"duration"`
	IsOriginalSound bool          `json:"is_original_sound"`
	IsPGC           bool          `json:"is_pgc"`
	PreventDownload bool          `json:"prevent_download"`
	PlayURL         douyinURLList `json:"play_url"`
	CoverHD         douyinURLList `json:"cover_hd"`
	CoverLarge      douyinURLList `json:"cover_large"`
	CoverMedium     douyinURLList `json:"cover_medium"`
}

func NewClient(httpClient *http.Client, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: timeout}
	}
	return &Client{
		httpClient: httpClient,
		detailURL:  douyinMusicDetailURL,
		cache:      util.NewTTLCache[*douyinMusic](douyinMusicCacheTTL, douyinMusicCacheSize),
	}
}

func (c *Client) Close() {
	if c != nil && c.httpClient != nil {
		c.httpClient.CloseIdleConnections()
	}
}

func (c *Client) GetTrack(ctx context.Context, musicID string) (*platform.Track, error) {
	music, err := c.fetchMusic(ctx, musicID)
	if err != nil {
		return nil, err
	}
	track := convertDouyinMusic(music)
	return &track, nil
}

func (c *Client) FetchDownloadInfo(ctx context.Context, musicID string, _ platform.Quality) (*platform.DownloadInfo, error) {
	music, err := c.fetchMusic(ctx, musicID)
	if err != nil {
		return nil, err
	}
	// 版权曲库（PGC）条目在抖音侧通常只给拍摄用的片段，无法确认是完整音频，
	// 按项目统一策略拒绝，不把片段当完整歌曲发送。
	if music.IsPGC {
		return nil, &platform.PlatformError{Platform: platformName, Resource: "track", ID: musicID, Err: platform.ErrIncompleteAudio}
	}
	// 创作者关闭了下载的原声同样不提供。
	if music.PreventDownload {
		return nil, platform.NewUnavailableError(platformName, "track", musicID)
	}
	urls := playableURLs(music.PlayURL)
	if len(urls) == 0 {
		return nil, platform.NewUnavailableError(platformName, "track", musicID)
	}
	return &platform.DownloadInfo{
		URL:           urls[0],
		CandidateURLs: urls[1:],
		Format:        audioFormatFromURL(urls[0]),
		Bitrate:       douyinNominalBitrate,
		Quality:       platform.QualityStandard,
	}, nil
}

func (c *Client) fetchMusic(ctx context.Context, musicID string) (*douyinMusic, error) {
	musicID = strings.TrimSpace(musicID)
	if !isDouyinMusicID(musicID) {
		return nil, platform.NewNotFoundError(platformName, "track", musicID)
	}
	return c.cache.Do(musicID, func() (*douyinMusic, error) {
		return c.fetchMusicUncached(ctx, musicID)
	})
}

func (c *Client) fetchMusicUncached(ctx context.Context, musicID string) (*douyinMusic, error) {
	params := url.Values{}
	params.Set("device_platform", "webapp")
	params.Set("aid", douyinWebAid)
	params.Set("music_id", musicID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.detailURL+"?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", douyinUserAgent)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	req.Header.Set("Referer", "https://www.douyin.com/music/"+musicID)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("douyin: music detail request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, platform.NewRateLimitedError(platformName)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("douyin: music detail status=%d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, douyinMaxBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("douyin: read music detail: %w", err)
	}

	var payload douyinMusicDetailResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("douyin: decode music detail: %w", err)
	}
	if payload.StatusCode != 0 {
		return nil, fmt.Errorf("douyin: music detail status_code=%d msg=%s", payload.StatusCode, strings.TrimSpace(payload.StatusMsg))
	}
	music := payload.MusicInfo
	if music == nil || firstNonEmpty(music.IDStr, music.MID) == "" {
		return nil, platform.NewNotFoundError(platformName, "track", musicID)
	}
	return music, nil
}

func convertDouyinMusic(music *douyinMusic) platform.Track {
	id := firstNonEmpty(music.IDStr, music.MID)
	track := platform.Track{
		ID:       id,
		Platform: platformName,
		Title:    firstNonEmpty(music.Title, id),
		Duration: time.Duration(music.Duration) * time.Second,
		CoverURL: firstNonEmpty(firstURL(music.CoverHD), firstURL(music.CoverLarge), firstURL(music.CoverMedium)),
		URL:      buildMusicURL(id),
	}
	if name := firstNonEmpty(music.Author, music.OwnerNickname); name != "" {
		artist := platform.Artist{ID: music.SecUID, Platform: platformName, Name: name}
		if music.SecUID != "" {
			artist.URL = "https://www.douyin.com/user/" + music.SecUID
		}
		track.Artists = []platform.Artist{artist}
	}
	return track
}

func playableURLs(list douyinURLList) []string {
	candidates := append(append([]string(nil), list.URLList...), list.URI)
	seen := make(map[string]struct{}, len(candidates))
	urls := make([]string, 0, len(candidates))
	for _, raw := range candidates {
		raw = strings.TrimSpace(raw)
		if !strings.HasPrefix(raw, "https://") && !strings.HasPrefix(raw, "http://") {
			continue
		}
		if _, ok := seen[raw]; ok {
			continue
		}
		seen[raw] = struct{}{}
		urls = append(urls, raw)
	}
	return urls
}

func audioFormatFromURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "mp3"
	}
	switch ext := strings.ToLower(strings.TrimPrefix(path.Ext(parsed.Path), ".")); ext {
	case "mp3", "m4a", "aac":
		return ext
	default:
		return "mp3"
	}
}

func firstURL(list douyinURLList) string {
	for _, raw := range list.URLList {
		if raw = strings.TrimSpace(raw); raw != "" {
			return raw
		}
	}
	return ""
}

func buildMusicURL(musicID string) string {
	return "https://www.douyin.com/music/" + musicID
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
