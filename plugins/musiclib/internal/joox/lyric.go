// Adapted from guohuiyuan/music-lib, commit 3b22e851f4fa2f55ceab943fa846a71536fed4f9.
// SPDX-License-Identifier: AGPL-3.0-only

package joox

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/liuran001/MusicBot-Go/plugins/musiclib/internal/model"
	utils "github.com/liuran001/MusicBot-Go/plugins/musiclib/internal/request"
	"net/url"
	"strings"
)

// GetLyrics 获取歌词
func (j *Joox) GetLyrics(s *model.Song) (string, error) {
	if s.Source != "joox" {
		return "", errors.New("source mismatch")
	}

	songID := s.ID
	if s.Extra != nil && s.Extra["songid"] != "" {
		songID = s.Extra["songid"]
	}

	pageSingle, pageErr := j.fetchPageSingle(songID)
	if pageErr == nil && pageSingle.LyricExists != 0 && pageSingle.Lyric != "" {
		lyric, err := decodeJooxLyric(pageSingle.Lyric)
		if err == nil {
			return lyric, nil
		}
		pageErr = err
	}

	params := url.Values{}
	params.Set("musicid", songID)
	params.Set("country", "sg")
	params.Set("lang", "zh_cn")
	apiURL := "https://api.joox.com/web-fcgi-bin/web_lyric?" + params.Encode()

	body, err := utils.Get(j.ctx, j.client, apiURL,
		utils.WithHeader("User-Agent", UserAgent),
		utils.WithHeader("Cookie", j.cookie),
	)
	if err != nil {
		if pageErr != nil {
			return "", fmt.Errorf("joox lyrics unavailable: current page API: %v; legacy API: %w", pageErr, err)
		}
		return "", err
	}

	bodyStr := string(body)
	if idx := strings.Index(bodyStr, "MusicJsonCallback("); idx >= 0 {
		bodyStr = strings.TrimPrefix(bodyStr[idx:], "MusicJsonCallback(")
		bodyStr = strings.TrimSuffix(bodyStr, ")")
	}

	var resp struct {
		Lyric string `json:"lyric"`
	}
	if err := json.Unmarshal([]byte(bodyStr), &resp); err != nil {
		return "", fmt.Errorf("joox lyric json parse error: %w", err)
	}
	if resp.Lyric == "" {
		return "", errors.New("lyric not found or empty")
	}

	return decodeJooxLyric(resp.Lyric)
}

func decodeJooxLyric(encoded string) (string, error) {
	decodedBytes, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("base64 decode error: %w", err)
	}

	return string(decodedBytes), nil
}
