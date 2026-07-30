package qqmusic

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/liuran001/MusicBot-Go/bot/platform"
)

// buildSingerAvatarURL derives the portrait URL from the singer's photo mid.
// The SingerInfoInter payload ships an empty pic block, so the URL has to be
// composed from singer_pmid (falling back to the plain mid).
func buildSingerAvatarURL(singerPMid, singerMid string) string {
	mid := strings.TrimSpace(singerPMid)
	if mid == "" {
		mid = strings.TrimSpace(singerMid)
	}
	if mid == "" || !qqSingerMidPattern.MatchString(strings.ReplaceAll(mid, "_", "")) {
		return ""
	}
	return "https://y.qq.com/music/photo_new/T001R300x300M000" + mid + ".jpg"
}

// GetSingerDetail fetches artist metadata by singer mid. QQ Music does not
// report a song count here, so the caller receives 0 and the artist card omits
// that line rather than showing a wrong number.
func (c *Client) GetSingerDetail(ctx context.Context, artistID string) (*platform.Artist, int, error) {
	artistID = strings.TrimSpace(artistID)
	if artistID == "" || !qqSingerMidPattern.MatchString(artistID) {
		return nil, 0, platform.NewNotFoundError("qqmusic", "artist", artistID)
	}
	if c == nil {
		return nil, 0, platform.NewUnavailableError("qqmusic", "artist", artistID)
	}

	payload := map[string]interface{}{
		"comm": map[string]interface{}{"ct": 24, "cv": 10000},
		"singer": map[string]interface{}{
			"module": "music.musichallSinger.SingerInfoInter",
			"method": "GetSingerDetail",
			"param": map[string]interface{}{
				"singer_mids": []string{artistID},
				"ex":          1,
			},
		},
	}

	body, err := c.postJSON(ctx, musicuEndpoint+"?format=json&inCharset=utf8&outCharset=utf8", payload)
	if err != nil {
		return nil, 0, platform.NewUnavailableError("qqmusic", "artist", artistID)
	}

	var response struct {
		Code   int `json:"code"`
		Singer struct {
			Code int `json:"code"`
			Data struct {
				SingerList []struct {
					BasicInfo struct {
						SingerMid  string      `json:"singer_mid"`
						SingerPMid string      `json:"singer_pmid"`
						Name       string      `json:"name"`
						SingerID   json.Number `json:"singer_id"`
					} `json:"basic_info"`
				} `json:"singer_list"`
			} `json:"data"`
		} `json:"singer"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, 0, platform.NewUnavailableError("qqmusic", "artist", artistID)
	}
	if response.Code != 0 || response.Singer.Code != 0 {
		return nil, 0, platform.NewUnavailableError("qqmusic", "artist", artistID)
	}
	if len(response.Singer.Data.SingerList) == 0 {
		return nil, 0, platform.NewNotFoundError("qqmusic", "artist", artistID)
	}

	basic := response.Singer.Data.SingerList[0].BasicInfo
	name := strings.TrimSpace(basic.Name)
	if name == "" {
		return nil, 0, platform.NewNotFoundError("qqmusic", "artist", artistID)
	}
	// Identity guard: never present a different singer under the requested mid.
	if responseMid := strings.TrimSpace(basic.SingerMid); responseMid != "" && responseMid != artistID {
		return nil, 0, platform.NewUnavailableError("qqmusic", "artist", artistID)
	}

	return &platform.Artist{
		ID:        artistID,
		Platform:  "qqmusic",
		Name:      name,
		AvatarURL: buildSingerAvatarURL(basic.SingerPMid, basic.SingerMid),
		URL:       buildArtistURL(artistID),
	}, 0, nil
}
