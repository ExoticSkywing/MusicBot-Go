// Source adapted from github.com/guohuiyuan/music-lib at commit 3b22e851f4fa2f55ceab943fa846a71536fed4f9.
// Licensed under GNU AGPL v3.0; see the bundled upstream license.

package migu

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/liuran001/MusicBot-Go/plugins/musiclib/internal/model"
	utils "github.com/liuran001/MusicBot-Go/plugins/musiclib/internal/request"
	"net/url"
	"strings"
)

func (m *Migu) GetLyrics(s *model.Song) (string, error) {
	if s.Source != "migu" {
		return "", errors.New("source mismatch")
	}

	contentID := ""
	if s.Extra != nil && s.Extra["content_id"] != "" {
		contentID = s.Extra["content_id"]
	} else {
		parts := strings.Split(s.ID, "|")
		if len(parts) >= 1 {
			contentID = parts[0]
		}
	}

	if contentID == "" {
		return "", errors.New("invalid migu song id")
	}

	params := url.Values{}
	params.Set("resourceId", contentID)
	params.Set("resourceType", "2")

	apiURL := "http://c.musicapp.migu.cn/MIGUM2.0/v1.0/content/resourceinfo.do?" + params.Encode()

	body, err := utils.Get(m.ctx, m.client, apiURL,
		utils.WithHeader("User-Agent", UserAgent),
		utils.WithHeader("Referer", Referer),
		utils.WithHeader("Cookie", m.cookie),
	)
	if err != nil {
		return "", err
	}

	var resp struct {
		Resource []struct {
			LrcUrl   string `json:"lrcUrl"`
			LyricUrl string `json:"lyricUrl"`
		} `json:"resource"`
	}

	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("migu resource info parse error: %w", err)
	}

	if len(resp.Resource) == 0 {
		return "", errors.New("resource info not found")
	}

	lyricUrl := resp.Resource[0].LrcUrl
	if lyricUrl == "" {
		lyricUrl = resp.Resource[0].LyricUrl
	}

	if lyricUrl == "" {
		return "", errors.New("lyric url not found")
	}

	lyricUrl = strings.Replace(lyricUrl, "http://", "https://", 1)

	lrcBody, err := utils.Get(m.ctx, m.client, lyricUrl,
		utils.WithHeader("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/143.0.0.0 Safari/537.36"),
		utils.WithHeader("Referer", "https://y.migu.cn/"),
		utils.WithHeader("Cookie", m.cookie),
	)
	if err != nil {
		return "", fmt.Errorf("download lyric failed: %w", err)
	}

	return string(lrcBody), nil
}
