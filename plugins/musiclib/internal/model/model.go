// Adapted from guohuiyuan/music-lib, commit 3b22e851f4fa2f55ceab943fa846a71536fed4f9.
// SPDX-License-Identifier: AGPL-3.0-only
// See ../LICENSE for the upstream license.
package model

import (
	"errors"
	"net/url"
	"strings"
)

var ErrPlaylistCategoriesUnsupported = errors.New("playlist categories not supported")

// ErrPreviewOnly marks an upstream response that exposes an audition or other
// incomplete rendition instead of a full track.
var ErrPreviewOnly = errors.New("preview-only audio")

func ExplicitPreviewURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	for _, segment := range strings.Split(strings.ToLower(strings.Trim(u.Path, "/")), "/") {
		if segment == "preview" || segment == "audition" || segment == "trail_audio" {
			return true
		}
	}
	for key, values := range u.Query() {
		switch strings.ToLower(key) {
		case "preview", "is_preview", "node_is_preview", "audition", "trail_audio":
			for _, value := range values {
				switch strings.ToLower(strings.TrimSpace(value)) {
				case "", "0", "false", "no", "off":
					continue
				default:
					return true
				}
			}
		}
	}
	return false
}

// CatalogDurationsMatch compares integer-second durations while allowing one
// second for catalog rounding and at most the production verifier's 5%/3s
// tolerance. Unknown values cannot prove a mismatch and are left to ffprobe.
func CatalogDurationsMatch(catalog, candidate int) bool {
	if catalog <= 0 || candidate <= 0 {
		return true
	}
	tolerance := catalog / 20
	if tolerance < 1 {
		tolerance = 1
	}
	if tolerance > 3 {
		tolerance = 3
	}
	delta := catalog - candidate
	if delta < 0 {
		delta = -delta
	}
	return delta <= tolerance
}

// Song 是所有音乐源通用的歌曲结构
type Song struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Artist   string `json:"artist"`
	Album    string `json:"album"`
	AlbumID  string `json:"album_id"` // 某些源特有，用于获取封面
	Duration int    `json:"duration"` // 秒
	Size     int64  `json:"size"`     // 文件大小 (字节)
	Bitrate  int    `json:"bitrate"`  // 码率 (kbps)
	Source   string `json:"source"`   // kugou, netease, qq, bilibili...
	URL      string `json:"url"`      // 真实音频文件下载链接
	Ext      string `json:"ext"`      // 文件后缀 (mp3, flac...)
	Cover    string `json:"cover"`    // 封面图片链接

	// [新增] 歌曲原始链接 (例如网页地址)
	Link string `json:"link"`

	// 用于存储源特有的元数据，避免解析 ID
	Extra map[string]string `json:"extra,omitempty"`

	// [新增] 标记歌曲是否无效 (经过 Probe 探测后)
	IsInvalid bool `json:"is_invalid,omitempty"`

	// IsVIP marks tracks that require a paid/VIP entitlement for full playback or download.
	IsVIP bool `json:"is_vip,omitempty"`
}

// Playlist 是所有音乐源通用的歌单结构 [修改]
type Playlist struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Cover       string `json:"cover"`
	TrackCount  int    `json:"track_count"`
	PlayCount   int    `json:"play_count"`
	Creator     string `json:"creator"`
	Description string `json:"description"`
	Source      string `json:"source"`
	Link        string `json:"link"`

	// [新增] 用于存储源特有的元数据，避免在 ID 中拼接字符串
	Extra map[string]string `json:"extra,omitempty"`
}

type PlaylistCategory struct {
	ID     string            `json:"id"`
	Name   string            `json:"name"`
	Group  string            `json:"group"`
	Source string            `json:"source"`
	Count  int               `json:"count"`
	Hot    bool              `json:"hot,omitempty"`
	Extra  map[string]string `json:"extra,omitempty"`
}
