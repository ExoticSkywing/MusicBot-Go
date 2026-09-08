// Source adapted from github.com/guohuiyuan/music-lib at commit 3b22e851f4fa2f55ceab943fa846a71536fed4f9.
// Licensed under GNU AGPL v3.0; see the bundled upstream license.

package qianqian

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/liuran001/MusicBot-Go/plugins/musiclib/internal/model"
	utils "github.com/liuran001/MusicBot-Go/plugins/musiclib/internal/request"
	"net/url"
	"regexp"
)

// Search 搜索歌曲
func (q *Qianqian) Search(keyword string) ([]model.Song, error) {
	params := url.Values{}
	params.Set("word", keyword)
	params.Set("type", "1")
	params.Set("pageNo", "1")
	params.Set("pageSize", "10")
	params.Set("appid", AppID)
	signParams(params)
	apiURL := "https://music.91q.com/v1/search?" + params.Encode()

	body, err := utils.Get(q.ctx, q.client, apiURL,
		utils.WithHeader("User-Agent", UserAgent),
		utils.WithHeader("Referer", Referer),
		utils.WithHeader("Cookie", q.cookie),
	)
	if err != nil {
		return nil, err
	}

	var resp struct {
		Data struct {
			TypeTrack []struct {
				TSID         string                          `json:"TSID"`
				Title        string                          `json:"title"`
				AlbumTitle   string                          `json:"albumTitle"`
				AlbumAssetID string                          `json:"albumAssetCode"`
				Pic          string                          `json:"pic"`
				Duration     int                             `json:"duration"`
				Lyric        string                          `json:"lyric"`
				Artist       []qianqianArtist                `json:"artist"`
				RateFileInfo map[string]qianqianRateFileInfo `json:"rateFileInfo"`
				IsVip        int                             `json:"isVip"`
			} `json:"typeTrack"`
		} `json:"data"`
	}

	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("qianqian json parse error: %w", err)
	}

	var songs []model.Song
	for _, item := range resp.Data.TypeTrack {
		if item.IsVip != 0 {
			continue
		}
		size, bitrate := qianqianRateStats(item.RateFileInfo, item.Duration)

		songs = append(songs, model.Song{
			Source:   "qianqian",
			ID:       item.TSID,
			Name:     item.Title,
			Artist:   joinQianqianArtists(item.Artist),
			Album:    item.AlbumTitle,
			AlbumID:  normalizeQianqianAlbumAssetCode(item.AlbumAssetID),
			Duration: item.Duration,
			Size:     size,
			Bitrate:  bitrate,
			Cover:    item.Pic,
			Link:     fmt.Sprintf("https://music.91q.com/song/%s", item.TSID),
			Extra: map[string]string{
				"tsid":     item.TSID,
				"album_id": normalizeQianqianAlbumAssetCode(item.AlbumAssetID),
			},
		})
	}
	return songs, nil
}

// Parse 解析链接并获取完整信息
func (q *Qianqian) Parse(link string) (*model.Song, error) {
	// 1. 提取 TSID
	re := regexp.MustCompile(`music\.91q\.com/song/(\w+)`)
	matches := re.FindStringSubmatch(link)
	if len(matches) < 2 {
		return nil, errors.New("invalid qianqian link")
	}
	tsid := matches[1]

	// MusicBot resolves the media separately in GetDownloadInfo. Metadata and
	// lyrics must remain available even when the audio requires a subscription.
	return q.fetchSongInfo(tsid)
}
