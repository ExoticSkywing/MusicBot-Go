// Adapted from guohuiyuan/music-lib, commit 3b22e851f4fa2f55ceab943fa846a71536fed4f9.
// SPDX-License-Identifier: AGPL-3.0-only

package jamendo

import (
	"errors"
	"github.com/liuran001/MusicBot-Go/plugins/musiclib/internal/model"
)

func (j *Jamendo) GetLyrics(s *model.Song) (string, error) {
	if s.Source != "jamendo" {
		return "", errors.New("source mismatch")
	}
	return "", nil
}
