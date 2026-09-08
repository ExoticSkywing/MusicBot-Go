// Adapted from guohuiyuan/music-lib, commit 3b22e851f4fa2f55ceab943fa846a71536fed4f9.
// SPDX-License-Identifier: AGPL-3.0-only

package joox

import (
	"errors"
	"github.com/liuran001/MusicBot-Go/plugins/musiclib/internal/model"
)

// ErrFullAudioUnavailable means JOOX exposed metadata or a preview, but no
// explicitly verified full-track stream for the current anonymous session.
var ErrFullAudioUnavailable = errors.New("joox full audio unavailable")

// GetDownloadURL 获取下载链接
func (j *Joox) GetDownloadURL(s *model.Song) (string, error) {
	if s.Source != "joox" {
		return "", errors.New("source mismatch")
	}
	if s.URL != "" {
		return s.URL, nil
	}

	songID := s.ID
	if s.Extra != nil && s.Extra["songid"] != "" {
		songID = s.Extra["songid"]
	}

	// 复用核心逻辑
	info, err := j.fetchSongInfo(songID)
	if err != nil {
		return "", err
	}
	if info.URL != "" {
		return info.URL, nil
	}

	return "", ErrFullAudioUnavailable
}
