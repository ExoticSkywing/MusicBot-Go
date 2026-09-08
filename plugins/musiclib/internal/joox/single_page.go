// Adapted from guohuiyuan/music-lib, commit 3b22e851f4fa2f55ceab943fa846a71536fed4f9.
// SPDX-License-Identifier: AGPL-3.0-only

package joox

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strings"

	"github.com/liuran001/MusicBot-Go/plugins/musiclib/internal/model"
	utils "github.com/liuran001/MusicBot-Go/plugins/musiclib/internal/request"
)

var nextDataPattern = regexp.MustCompile(`(?s)<script[^>]*id="__NEXT_DATA__"[^>]*>(.*?)</script>`)

// jooxPageSingle mirrors the public /page/single payload used by JOOX's own
// single-detail page. Pointer fields preserve the distinction between an
// explicit playback decision and an omitted field.
type jooxPageSingle struct {
	ID              string       `json:"id"`
	Name            string       `json:"name"`
	AlbumID         string       `json:"album_id"`
	AlbumName       string       `json:"album_name"`
	ArtistList      []jooxArtist `json:"artist_list"`
	Images          []jooxImage  `json:"images"`
	PlayDuration    int          `json:"play_duration"`
	VIPFlag         int          `json:"vip_flag"`
	Lyric           string       `json:"lrc_content"`
	LyricExists     int          `json:"lrc_exist"`
	IsPlayable      *bool        `json:"is_playable"`
	NodeIsPreview   *bool        `json:"node_is_preview"`
	ErrorCode       *int         `json:"error_code"`
	StatusCode      *int         `json:"status_code"`
	PlayURLList     []string     `json:"play_url_list"`
	RefrainURL      string       `json:"refrain_url"`
	RefrainDuration int          `json:"refrain_duration"`
}

func (j *Joox) fetchSongInfo(songID string) (*model.Song, error) {
	single, currentErr := j.fetchPageSingle(songID)
	if currentErr == nil {
		return songFromPageSingle(songID, single), nil
	}

	legacy, legacyErr := j.fetchLegacySongInfo(songID)
	if legacyErr == nil {
		return legacy, nil
	}
	return nil, fmt.Errorf("joox song details unavailable: current page API: %v; legacy API: %w", currentErr, legacyErr)
}

func (j *Joox) fetchPageSingle(songID string) (*jooxPageSingle, error) {
	songID = normalizeJooxID(songID)
	if songID == "" {
		return nil, errors.New("joox song id is empty")
	}

	params := url.Values{}
	params.Set("country", "hk")
	params.Set("lang", "zh_cn")
	params.Set("id", songID)
	params.Set("regionURI", "hk-zh_cn")
	params.Set("num", "10")
	params.Set("device", "desktop")
	apiURL := "https://cache.api.joox.com/page/single?" + params.Encode()

	body, apiErr := utils.Get(j.ctx, j.client, apiURL,
		utils.WithHeader("User-Agent", UserAgent),
		utils.WithHeader("Cookie", j.cookie),
	)
	if apiErr == nil {
		var response struct {
			Single jooxPageSingle `json:"single"`
		}
		if err := json.Unmarshal(body, &response); err == nil {
			if response.Single.ID != "" || response.Single.Name != "" {
				return &response.Single, nil
			}
			apiErr = errors.New("joox page API returned no song")
		} else {
			apiErr = fmt.Errorf("joox page API json error: %w", err)
		}
	}

	pageURL := fmt.Sprintf("https://www.joox.com/hk/single/%s", url.PathEscape(songID))
	body, pageErr := utils.Get(j.ctx, j.client, pageURL,
		utils.WithHeader("User-Agent", UserAgent),
		utils.WithHeader("Cookie", j.cookie),
	)
	if pageErr != nil {
		return nil, fmt.Errorf("joox page API: %v; page HTML: %w", apiErr, pageErr)
	}

	matches := nextDataPattern.FindSubmatch(body)
	if len(matches) < 2 {
		return nil, fmt.Errorf("joox page API: %v; page HTML has no __NEXT_DATA__", apiErr)
	}
	var nextData struct {
		Props struct {
			PageProps struct {
				Single jooxPageSingle `json:"passingArgumentsData"`
			} `json:"pageProps"`
		} `json:"props"`
	}
	if err := json.Unmarshal(matches[1], &nextData); err != nil {
		return nil, fmt.Errorf("joox page HTML json error: %w", err)
	}
	if nextData.Props.PageProps.Single.ID == "" && nextData.Props.PageProps.Single.Name == "" {
		return nil, errors.New("joox page HTML returned no song")
	}
	return &nextData.Props.PageProps.Single, nil
}

func songFromPageSingle(requestedID string, single *jooxPageSingle) *model.Song {
	songID := normalizeJooxID(firstNonEmpty(single.ID, requestedID))
	duration := single.PlayDuration
	if single.NodeIsPreview == nil || *single.NodeIsPreview {
		// The public page reports the preview clip length as play_duration.
		duration = 0
	}

	extra := map[string]string{"songid": songID}
	albumID := normalizeJooxID(single.AlbumID)
	if albumID != "" {
		extra["album_id"] = albumID
	}

	streamURL := fullStreamURL(single)
	streamExt := ""
	if streamURL != "" {
		if parsed, err := url.Parse(streamURL); err == nil {
			path := parsed.Path
			if dot := strings.LastIndexByte(path, '.'); dot >= 0 && dot+1 < len(path) {
				streamExt = strings.ToLower(path[dot+1:])
			}
		}
	}

	return &model.Song{
		Source:   "joox",
		ID:       songID,
		Name:     single.Name,
		Artist:   joinJooxArtists(single.ArtistList),
		Album:    single.AlbumName,
		AlbumID:  albumID,
		Duration: duration,
		Cover:    pickJooxImage(single.Images),
		URL:      streamURL,
		Ext:      streamExt,
		Link:     fmt.Sprintf("https://www.joox.com/hk/single/%s", url.PathEscape(songID)),
		Extra:    extra,
		IsVIP:    single.VIPFlag > 0,
	}
}

func fullStreamURL(single *jooxPageSingle) string {
	if single.IsPlayable == nil || !*single.IsPlayable ||
		single.NodeIsPreview == nil || *single.NodeIsPreview ||
		single.ErrorCode == nil || *single.ErrorCode != 0 ||
		single.StatusCode == nil || *single.StatusCode != 0 {
		return ""
	}
	previewURL := strings.TrimSpace(html.UnescapeString(single.RefrainURL))
	for _, candidate := range single.PlayURLList {
		candidate = strings.TrimSpace(html.UnescapeString(candidate))
		if candidate != "" && candidate != previewURL {
			return candidate
		}
	}
	return ""
}
