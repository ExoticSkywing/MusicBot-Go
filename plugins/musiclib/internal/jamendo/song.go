// Adapted from guohuiyuan/music-lib, commit 3b22e851f4fa2f55ceab943fa846a71536fed4f9.
// SPDX-License-Identifier: AGPL-3.0-only

package jamendo

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/liuran001/MusicBot-Go/plugins/musiclib/internal/model"
	"regexp"
)

func (j *Jamendo) Search(keyword string) ([]model.Song, error) {
	body, err := j.searchByType(keyword, "track")
	if err != nil {
		return nil, err
	}

	var results []jamendoTrackItem
	if err := json.Unmarshal(body, &results); err != nil {
		return nil, fmt.Errorf("jamendo json parse error: %w", err)
	}

	songs := make([]model.Song, 0, len(results))
	for _, item := range results {
		song := buildSong(item, jamendoTrackMeta{})
		if song == nil {
			continue
		}
		songs = append(songs, *song)
	}
	return songs, nil
}

func (j *Jamendo) Parse(link string) (*model.Song, error) {
	re := regexp.MustCompile(`jamendo\.com/track/(\d+)`)
	matches := re.FindStringSubmatch(link)
	if len(matches) < 2 {
		return nil, errors.New("invalid jamendo link")
	}

	return j.getTrackByID(matches[1], jamendoTrackMeta{})
}
