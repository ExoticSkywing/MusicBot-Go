// Adapted from guohuiyuan/music-lib, commit 3b22e851f4fa2f55ceab943fa846a71536fed4f9.
// SPDX-License-Identifier: AGPL-3.0-only

package fivesing

import (
	"encoding/json"
	"errors"
	"github.com/liuran001/MusicBot-Go/plugins/musiclib/internal/model"
	utils "github.com/liuran001/MusicBot-Go/plugins/musiclib/internal/request"
	"net/url"
	"strings"
)

func (f *Fivesing) GetLyrics(s *model.Song) (string, error) {
	if s.Source != "fivesing" {
		return "", errors.New("source mismatch")
	}

	var songID, songType string
	if s.Extra != nil {
		songID = s.Extra["songid"]
		songType = s.Extra["songtype"]
	} else {
		parts := strings.Split(s.ID, "|")
		if len(parts) == 2 {
			songID = parts[0]
			songType = parts[1]
		}
	}

	if songID == "" {
		return "", errors.New("invalid id")
	}

	params := url.Values{}
	params.Set("songid", songID)
	params.Set("songtype", songType)
	apiURL := "http://mobileapi.5sing.kugou.com/song/newget?" + params.Encode()

	body, err := utils.Get(f.ctx, f.client, apiURL, utils.WithHeader("User-Agent", UserAgent), utils.WithHeader("Cookie", f.cookie))
	if err != nil {
		return "", err
	}

	var resp struct {
		Data struct {
			DynamicWords string `json:"dynamicWords"`
		} `json:"data"`
	}
	json.Unmarshal(body, &resp)
	if resp.Data.DynamicWords == "" {
		return "", errors.New("lyrics not found")
	}
	return resp.Data.DynamicWords, nil
}
