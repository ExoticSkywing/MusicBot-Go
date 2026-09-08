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
)

// GetLyrics 获取歌词
func (q *Qianqian) GetLyrics(s *model.Song) (string, error) {
	if s.Source != "qianqian" {
		return "", errors.New("source mismatch")
	}

	tsid := s.ID
	if s.Extra != nil && s.Extra["tsid"] != "" {
		tsid = s.Extra["tsid"]
	}

	params := url.Values{}
	params.Set("TSID", tsid)
	params.Set("appid", AppID)
	signParams(params)
	apiURL := "https://music.91q.com/v1/song/info?" + params.Encode()

	body, err := utils.Get(q.ctx, q.client, apiURL,
		utils.WithHeader("User-Agent", UserAgent),
		utils.WithHeader("Referer", Referer),
		utils.WithHeader("Cookie", q.cookie),
	)
	if err != nil {
		return "", err
	}

	var resp struct {
		Data []struct {
			Lyric string `json:"lyric"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("qianqian song info parse error: %w", err)
	}
	if len(resp.Data) == 0 || resp.Data[0].Lyric == "" {
		return "", errors.New("lyric url not found")
	}

	lyricURL := resp.Data[0].Lyric
	lrcBody, err := utils.Get(q.ctx, q.client, lyricURL,
		utils.WithHeader("User-Agent", UserAgent),
		utils.WithHeader("Cookie", q.cookie),
	)
	if err != nil {
		return "", fmt.Errorf("download lyric failed: %w", err)
	}
	return string(lrcBody), nil
}
