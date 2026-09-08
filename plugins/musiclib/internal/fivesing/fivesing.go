// Adapted from guohuiyuan/music-lib, commit 3b22e851f4fa2f55ceab943fa846a71536fed4f9.
// SPDX-License-Identifier: AGPL-3.0-only

package fivesing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/liuran001/MusicBot-Go/plugins/musiclib/internal/model"
	utils "github.com/liuran001/MusicBot-Go/plugins/musiclib/internal/request"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	UserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/134.0.0.0 Safari/537.36"
)

type Fivesing struct {
	ctx    context.Context
	client *http.Client
	cookie string
}

func New(ctx context.Context, cookie string, client *http.Client) *Fivesing {
	if ctx == nil {
		ctx = context.Background()
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &Fivesing{ctx: ctx, client: client, cookie: cookie}
}

// fetchCreatorName 辅助函数：仅获取创建者名称
func (f *Fivesing) fetchCreatorName(id string) (string, error) {
	infoURL := fmt.Sprintf("http://mobileapi.5sing.kugou.com/song/getsonglist?id=%s&songfields=user", id)
	infoBody, err := utils.Get(f.ctx, f.client, infoURL, utils.WithHeader("User-Agent", UserAgent))
	if err != nil {
		return "", err
	}

	// 使用 RawMessage 避免 data 为 [] 时报错
	var rawResp struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(infoBody, &rawResp); err != nil {
		return "", err
	}

	// 检查 Data 是否是对象 (以 '{' 开头)
	dataStr := strings.TrimSpace(string(rawResp.Data))
	if len(dataStr) == 0 || dataStr[0] != '{' {
		return "", nil // 数据为空或格式不对，直接返回空名
	}

	var data struct {
		User struct {
			UserName string `json:"NN"`
		} `json:"user"`
	}

	if err := json.Unmarshal(rawResp.Data, &data); err != nil {
		return "", err
	}
	return data.User.UserName, nil
}

// fetchPlaylistDetail [核心] 获取歌单详情 (API 获取元数据 + HTML 解析歌曲)
func (f *Fivesing) fetchPlaylistDetail(id string) (*model.Playlist, []model.Song, error) {
	// 1. 调用 API 获取歌单元数据 (标题、封面、关键的 UserId)
	infoURL := fmt.Sprintf("http://mobileapi.5sing.kugou.com/song/getsonglist?id=%s&songfields=ID,user", id)
	infoBody, err := utils.Get(f.ctx, f.client, infoURL, utils.WithHeader("User-Agent", UserAgent))
	if err != nil {
		return nil, nil, fmt.Errorf("fetch info failed: %w", err)
	}

	// 使用 RawMessage 处理多态类型 (data 可能是 object 或 array)
	var rawResp struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(infoBody, &rawResp); err != nil {
		return nil, nil, fmt.Errorf("fetch playlist info json error: %w", err)
	}

	// 检查 Data 是否是有效对象 (以 '{' 开头)。如果是 '[]' (空数组)，说明歌单不存在或无权限
	dataStr := strings.TrimSpace(string(rawResp.Data))
	if len(dataStr) == 0 || dataStr[0] != '{' {
		return nil, nil, errors.New("playlist info not found or invalid (api returned empty list)")
	}

	// 定义真实的数据结构
	var data struct {
		Title     string `json:"T"`
		Content   string `json:"C"`
		Picture   string `json:"P"`
		Click     int    `json:"H"`
		SongCount int    `json:"E"`
		User      struct {
			ID       int64  `json:"ID"`
			UserName string `json:"NN"`
		} `json:"user"`
	}

	if err := json.Unmarshal(rawResp.Data, &data); err != nil {
		return nil, nil, fmt.Errorf("parse playlist data error: %w", err)
	}

	// 验证 UserId
	userId := ""
	if data.User.ID != 0 {
		userId = strconv.FormatInt(data.User.ID, 10)
	}
	if userId == "" {
		return nil, nil, errors.New("playlist user not found or invalid id")
	}

	// 构造 Playlist 对象
	playlist := &model.Playlist{
		Source:      "fivesing",
		ID:          id,
		Name:        data.Title,
		Cover:       data.Picture,
		TrackCount:  data.SongCount,
		PlayCount:   data.Click,
		Creator:     data.User.UserName,
		Description: data.Content,
		Link:        fmt.Sprintf("http://5sing.kugou.com/%s/dj/%s.html", userId, id),
		Extra:       map[string]string{"user_id": userId},
	}

	// 2. 构造歌单页面 URL 并获取 HTML
	pageURL := playlist.Link
	htmlBodyBytes, err := utils.Get(f.ctx, f.client, pageURL,
		utils.WithHeader("User-Agent", UserAgent),
		utils.WithHeader("Cookie", f.cookie),
	)
	if err != nil {
		// 如果获取 HTML 失败，至少返回元数据
		return playlist, nil, fmt.Errorf("fetch html failed: %w", err)
	}

	// 3. 解析 HTML 提取歌曲
	songs, err := f.parseSongsFromHTML(string(htmlBodyBytes))
	if err != nil {
		// 解析失败不影响元数据返回，但记录错误 (有些歌单可能确实为空)
		return playlist, nil, nil
	}

	return playlist, songs, nil
}

// parseSongsFromHTML [私有] 从歌单 HTML 中提取歌曲信息
func (f *Fivesing) parseSongsFromHTML(htmlContent string) ([]model.Song, error) {
	// A. 提取所有 <li class="p_rel">...</li> 块
	blockRe := regexp.MustCompile(`<li class="p_rel">([\s\S]*?)</li>`)
	blocks := blockRe.FindAllStringSubmatch(htmlContent, -1)

	if len(blocks) == 0 {
		return nil, errors.New("no songs found in playlist html (structure mismatch)")
	}

	// B. 预编译块内提取正则
	// 提取歌曲: 匹配 href="/yc/123.html" 和 歌名
	songRe := regexp.MustCompile(`href="http://5sing\.kugou\.com/(yc|fc|bz)/(\d+)\.html"[^>]*>([^<]+)</a>`)
	// 提取歌手: 匹配 class="s_soner" (注意: 5sing 拼写错误 soner)
	artistRe := regexp.MustCompile(`class="s_soner[^"]*".*?>([^<]+)</a>`)

	var songs []model.Song
	seen := make(map[string]bool)

	for _, match := range blocks {
		blockHTML := match[1]

		// 提取歌曲信息
		songMatch := songRe.FindStringSubmatch(blockHTML)
		if len(songMatch) < 4 {
			continue
		}
		kind := songMatch[1] // yc, fc, bz
		songID := songMatch[2]
		rawName := songMatch[3]

		// 提取歌手信息
		artist := "Unknown"
		artistMatch := artistRe.FindStringSubmatch(blockHTML)
		if len(artistMatch) >= 2 {
			artist = artistMatch[1]
		}

		// 去重
		uniqueKey := kind + "|" + songID
		if seen[uniqueKey] {
			continue
		}
		seen[uniqueKey] = true

		// 清理转义字符
		name := strings.TrimSpace(html.UnescapeString(rawName))
		artist = strings.TrimSpace(html.UnescapeString(artist))

		songs = append(songs, model.Song{
			Source: "fivesing",
			ID:     fmt.Sprintf("%s|%s", songID, kind),
			Name:   name,
			Artist: artist,
			Link:   fmt.Sprintf("http://5sing.kugou.com/%s/%s.html", kind, songID),
			Extra: map[string]string{
				"songid":   songID,
				"songtype": kind,
			},
		})
	}
	return songs, nil
}

// fetchSongInfo 获取歌曲元数据。音频地址由 GetDownloadURL 单独解析，
// 避免普通曲目查询因媒体权限或地区限制失败。
func (f *Fivesing) fetchSongInfo(songID, songType string) (*model.Song, error) {
	params := url.Values{}
	params.Set("songid", songID)
	params.Set("songtype", songType)
	metaURL := "http://mobileapi.5sing.kugou.com/song/newget?" + params.Encode()

	metaBody, err := utils.Get(f.ctx, f.client, metaURL, utils.WithHeader("User-Agent", UserAgent), utils.WithHeader("Cookie", f.cookie))
	if err != nil {
		return nil, err
	}

	var name, artist, cover string
	var size int64
	var metaResp struct {
		Data struct {
			SN   string `json:"SN"`
			Size int64  `json:"SS"`
			User struct {
				NN string `json:"NN"`
				I  string `json:"I"`
			} `json:"user"`
		} `json:"data"`
	}
	if err := json.Unmarshal(metaBody, &metaResp); err != nil {
		return nil, fmt.Errorf("fivesing metadata json parse error: %w", err)
	}
	name = metaResp.Data.SN
	artist = metaResp.Data.User.NN
	cover = metaResp.Data.User.I
	size = metaResp.Data.Size

	if name == "" {
		name = fmt.Sprintf("5sing_%s_%s", songType, songID)
	}
	duration := 0
	if size > 0 {
		duration = int((size * 8) / 320000)
	}

	return &model.Song{
		Source:   "fivesing",
		ID:       fmt.Sprintf("%s|%s", songID, songType),
		Name:     name,
		Artist:   artist,
		Cover:    cover,
		Size:     size,
		Duration: duration,
		Link:     fmt.Sprintf("http://5sing.kugou.com/%s/%s.html", songType, songID),
		Extra: map[string]string{
			"songid":   songID,
			"songtype": songType,
		},
	}, nil
}

// fetchAudioLink 仅获取音频链接
func (f *Fivesing) fetchAudioLink(songID, songType string) (string, error) {
	params := url.Values{}
	params.Set("songid", songID)
	params.Set("songtype", songType)

	apiURL := "http://mobileapi.5sing.kugou.com/song/getSongUrl?" + params.Encode()
	body, err := utils.Get(f.ctx, f.client, apiURL, utils.WithHeader("User-Agent", UserAgent), utils.WithHeader("Cookie", f.cookie))
	if err != nil {
		return "", err
	}

	var resp struct {
		Code int `json:"code"`
		Data struct {
			SQUrl       string `json:"squrl"`
			SQUrlBackup string `json:"squrl_backup"`
			HQUrl       string `json:"hqurl"`
			HQUrlBackup string `json:"hqurl_backup"`
			LQUrl       string `json:"lqurl"`
			LQUrlBackup string `json:"lqurl_backup"`
		} `json:"data"`
	}

	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("json parse error: %w", err)
	}

	if resp.Code != 1000 {
		return "", errors.New("api returned error code")
	}

	if url := getFirstValid(resp.Data.SQUrl, resp.Data.SQUrlBackup); url != "" {
		return url, nil
	}
	if url := getFirstValid(resp.Data.HQUrl, resp.Data.HQUrlBackup); url != "" {
		return url, nil
	}
	if url := getFirstValid(resp.Data.LQUrl, resp.Data.LQUrlBackup); url != "" {
		return url, nil
	}
	for _, candidate := range []string{resp.Data.SQUrl, resp.Data.SQUrlBackup, resp.Data.HQUrl, resp.Data.HQUrlBackup, resp.Data.LQUrl, resp.Data.LQUrlBackup} {
		if model.ExplicitPreviewURL(candidate) {
			return "", fmt.Errorf("%w: 5sing returned only preview media", model.ErrPreviewOnly)
		}
	}

	return "", errors.New("no valid download url found")
}

func getFirstValid(urls ...string) string {
	for _, u := range urls {
		if u != "" && !model.ExplicitPreviewURL(u) {
			return u
		}
	}
	return ""
}

var emTagPattern = regexp.MustCompile(`(?i)</?em(?:\s+[^>]*)?>`)

func removeEmTags(s string) string {
	return strings.TrimSpace(emTagPattern.ReplaceAllString(s, ""))
}
