// Source adapted from github.com/guohuiyuan/music-lib at commit 3b22e851f4fa2f55ceab943fa846a71536fed4f9.
// Licensed under GNU AGPL v3.0; see the bundled upstream license.

package migu

import (
	"errors"
	"fmt"
	"github.com/liuran001/MusicBot-Go/plugins/musiclib/internal/model"
	"net/http"
	"net/url"
	"strings"
)

// GetDownloadURL 获取下载链接
func (m *Migu) GetDownloadURL(s *model.Song) (string, error) {
	if s.Source != "migu" {
		return "", errors.New("source mismatch")
	}
	if s.Extra != nil && s.Extra["preview_only"] == "true" {
		return "", fmt.Errorf("%w: migu chargeAuditions requires an audition", model.ErrPreviewOnly)
	}
	if s.URL != "" {
		if model.ExplicitPreviewURL(s.URL) {
			return "", fmt.Errorf("%w: migu media URL is marked as an audition", model.ErrPreviewOnly)
		}
		return s.URL, nil
	}

	var contentID, resourceType, formatType string
	if s.Extra != nil {
		contentID = s.Extra["content_id"]
		resourceType = s.Extra["resource_type"]
		formatType = s.Extra["format_type"]
	}

	if contentID == "" || resourceType == "" || formatType == "" {
		parts := strings.Split(s.ID, "|")
		if len(parts) == 3 {
			contentID = parts[0]
			resourceType = parts[1]
			formatType = parts[2]
		} else {
			return "", errors.New("invalid id structure and missing extra data")
		}
	}

	params := url.Values{}
	params.Set("toneFlag", formatType)
	params.Set("netType", "00")
	params.Set("userId", MagicUserID)
	params.Set("ua", "Android_migu")
	params.Set("version", "5.1")
	params.Set("copyrightId", "0")
	params.Set("contentId", contentID)
	params.Set("resourceType", resourceType)
	params.Set("channel", "0")

	apiURL := "http://app.pd.nf.migu.cn/MIGUM2.0/v1.0/content/sub/listenSong.do?" + params.Encode()

	client := *m.client
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}

	req, err := http.NewRequestWithContext(m.ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Referer", Referer)
	req.Header.Set("Cookie", m.cookie)

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusMultipleChoices && resp.StatusCode < http.StatusBadRequest {
		location, err := resp.Location()
		if err != nil {
			return "", fmt.Errorf("migu download redirect is invalid: %w", err)
		}
		if model.ExplicitPreviewURL(location.String()) {
			return "", fmt.Errorf("%w: migu redirect is marked as an audition", model.ErrPreviewOnly)
		}
		return location.String(), nil
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("migu download endpoint returned status %d", resp.StatusCode)
	}

	return "", fmt.Errorf("migu download endpoint returned no redirect (status %d)", resp.StatusCode)
}
