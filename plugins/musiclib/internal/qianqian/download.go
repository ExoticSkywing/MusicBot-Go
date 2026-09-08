// Source adapted from github.com/guohuiyuan/music-lib at commit 3b22e851f4fa2f55ceab943fa846a71536fed4f9.
// Licensed under GNU AGPL v3.0; see the bundled upstream license.

package qianqian

import (
	"errors"
	"github.com/liuran001/MusicBot-Go/plugins/musiclib/internal/model"
)

// GetDownloadURL 获取下载链接
func (q *Qianqian) GetDownloadURL(s *model.Song) (string, error) {
	if s.Source != "qianqian" {
		return "", errors.New("source mismatch")
	}
	if s.URL != "" {
		return s.URL, nil
	}

	tsid := s.ID
	if s.Extra != nil && s.Extra["tsid"] != "" {
		tsid = s.Extra["tsid"]
	}

	return q.fetchDownloadURL(tsid, s)
}
