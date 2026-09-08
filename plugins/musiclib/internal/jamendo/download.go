// Adapted from guohuiyuan/music-lib, commit 3b22e851f4fa2f55ceab943fa846a71536fed4f9.
// SPDX-License-Identifier: AGPL-3.0-only

package jamendo

import (
	"errors"
	"github.com/liuran001/MusicBot-Go/plugins/musiclib/internal/model"
)

func (j *Jamendo) GetDownloadURL(s *model.Song) (string, error) {
	if s.Source != "jamendo" {
		return "", errors.New("source mismatch")
	}
	if s.URL != "" {
		return s.URL, nil
	}

	trackID := s.ID
	if s.Extra != nil && s.Extra["track_id"] != "" {
		trackID = s.Extra["track_id"]
	}
	if trackID == "" {
		return "", errors.New("id missing")
	}

	info, err := j.getTrackByID(trackID, jamendoTrackMeta{
		ArtistName: s.Artist,
		AlbumName:  s.Album,
		AlbumID:    s.AlbumID,
	})
	if err != nil {
		return "", err
	}
	return info.URL, nil
}
