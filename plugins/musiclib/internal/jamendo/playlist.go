// Adapted from guohuiyuan/music-lib, commit 3b22e851f4fa2f55ceab943fa846a71536fed4f9.
// SPDX-License-Identifier: AGPL-3.0-only

package jamendo

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/liuran001/MusicBot-Go/plugins/musiclib/internal/model"
	"regexp"
	"strconv"
	"strings"
)

func (j *Jamendo) SearchPlaylist(keyword string) ([]model.Playlist, error) {
	body, err := j.searchByType(keyword, "playlist")
	if err != nil {
		return nil, err
	}

	var results []jamendoPlaylistItem

	if err := json.Unmarshal(body, &results); err != nil {
		return nil, fmt.Errorf("jamendo playlist json parse error: %w", err)
	}

	playlists := make([]model.Playlist, 0, len(results))
	for _, item := range results {
		if item.ID == 0 {
			continue
		}

		playlists = append(playlists, model.Playlist{
			Source:  "jamendo",
			ID:      strconv.Itoa(item.ID),
			Name:    item.Name,
			Creator: item.UserName,
			Cover:   item.Image,
			Link:    fmt.Sprintf("https://www.jamendo.com/playlist/%d", item.ID),
		})
	}
	return playlists, nil
}

func (j *Jamendo) GetPlaylistSongs(id string) ([]model.Song, error) {
	playlistItem, err := j.getPlaylistByID(id)
	if err != nil {
		return nil, err
	}
	return j.fetchPlaylistTracks(playlistItem)
}

func (j *Jamendo) ParsePlaylist(link string) (*model.Playlist, []model.Song, error) {
	re := regexp.MustCompile(`jamendo\.com/playlist/(\d+)`)
	matches := re.FindStringSubmatch(link)
	if len(matches) >= 2 {
		return j.fetchPlaylistDetail(matches[1])
	}

	if len(link) > 0 && !strings.Contains(link, "/") {
		return j.fetchPlaylistDetail(link)
	}

	return nil, nil, errors.New("invalid jamendo playlist link")
}
