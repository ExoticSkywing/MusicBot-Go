package soda

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/platform"
)

// Public API contracts from guohuiyuan/music-lib (095a3b9 and 02402db).
// Search uses the upstream unsigned request profile; track details use H5.
const sodaSearchUserAgent = "com.luna.music/100198030 (Linux; U; Android 15; zh_CN_#Hans; ABR-AL80; Build/V417IR;tt-ok/3.12.13.19)"

func (c *Client) getLunaJSON(ctx context.Context, path string, query url.Values) ([]byte, error) {
	params := sodaSearchParams()
	params.Set("count", "20")
	for _, key := range []string{"q", "cursor", "count"} {
		if query.Has(key) {
			params.Set(key, query.Get(key))
		}
	}
	rawURL := "https://api.qishui.com" + path + "?" + params.Encode()
	headers := map[string]string{
		"User-Agent":   sodaSearchUserAgent,
		"Content-Type": "application/json; charset=UTF-8",
	}
	body, err := c.getPublicJSONWithHeaders(ctx, rawURL, headers)
	if err != nil {
		return nil, err
	}
	if err := checkSodaAPIResponse(body); err != nil {
		return nil, err
	}
	return body, nil
}

func (c *Client) getPCPlaylistJSON(ctx context.Context, query url.Values) ([]byte, error) {
	params := sodaPCAppParams()
	for _, key := range []string{"playlist_id", "cursor", "count"} {
		params.Set(key, query.Get(key))
	}
	body, err := c.getPublicJSONWithHeaders(ctx, "https://api.qishui.com/luna/pc/playlist/detail?"+params.Encode(), map[string]string{
		"User-Agent":               "LunaPC/3.3.0(359450208)",
		"x-luna-background-type":   "foreground",
		"x-luna-is-background-req": "0",
		"x-luna-is-local-user":     "1",
	})
	if err != nil {
		return nil, err
	}
	if err := checkSodaAPIResponse(body); err != nil {
		return nil, err
	}
	return body, nil
}

// Public search and playlist requests can reject otherwise usable account cookies.
func (c *Client) getPublicJSONWithHeaders(ctx context.Context, rawURL string, headers map[string]string) ([]byte, error) {
	body, err := c.getJSONWithHeaders(ctx, rawURL, headers)
	// Some web/account cookies are rejected by this public search endpoint.
	// Retry that specific response anonymously without changing the account.
	if err == nil && strings.TrimSpace(c.cookie) != "" {
		var status struct {
			Code int `json:"status_code"`
		}
		if json.Unmarshal(body, &status) == nil && status.Code == 1000006 {
			anonymous := &Client{httpClient: c.httpClient}
			body, err = anonymous.getJSONWithHeaders(ctx, rawURL, headers)
		}
	}
	return body, err
}

func (c *Client) fetchTrackWeb(ctx context.Context, trackID string) (*sodaTrackV2Response, error) {
	trackID = strings.TrimSpace(trackID)
	if trackID == "" {
		return nil, platform.NewNotFoundError("soda", "track", trackID)
	}
	params := url.Values{"device_platform": {"web"}, "track_id": {trackID}}
	body, err := c.getJSON(ctx, "https://beta-luna.douyin.com/luna/h5/seo_track?"+params.Encode())
	if err != nil {
		return nil, err
	}
	if err := checkSodaAPIResponse(body); err != nil {
		return nil, err
	}
	var payload struct {
		sodaTrackV2Response
		SEOTrack struct {
			Track sodaTrack `json:"track"`
			Lyric struct {
				Content string `json:"content"`
			} `json:"lyric"`
		} `json:"seo_track"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("soda: parse seo_track response: %w", err)
	}
	if strings.TrimSpace(payload.SEOTrack.Track.ID) == "" {
		return nil, platform.NewNotFoundError("soda", "track", trackID)
	}
	payload.TrackInfo = payload.SEOTrack.Track
	payload.Track = payload.SEOTrack.Track
	if strings.TrimSpace(payload.Lyric.Content) == "" {
		payload.Lyric.Content = payload.SEOTrack.Lyric.Content
	}
	return &payload.sodaTrackV2Response, nil
}

func checkSodaAPIResponse(body []byte) error {
	if len(strings.TrimSpace(string(body))) == 0 {
		return fmt.Errorf("soda: empty API response")
	}
	var status struct {
		Code int `json:"status_code"`
		Info struct {
			Code int `json:"code"`
		} `json:"status_info"`
	}
	if err := json.Unmarshal(body, &status); err != nil {
		return fmt.Errorf("soda: invalid API response: %w", err)
	}
	if status.Code != 0 {
		return fmt.Errorf("soda: API status_code=%d", status.Code)
	}
	if status.Info.Code != 0 {
		return fmt.Errorf("soda: API status_info.code=%d", status.Info.Code)
	}
	return nil
}

func sodaSearchParams() url.Values {
	params := url.Values{}
	values := map[string]string{
		"device_platform":              "android",
		"os":                           "android",
		"ssmix":                        "a",
		"cdid":                         "46556f98-1720-4248-83da-62b74b60b46a",
		"channel":                      "xiaomi_8478_64",
		"aid":                          sodaAid,
		"app_name":                     "luna",
		"version_code":                 "100198030",
		"version_name":                 "19.8.0",
		"manifest_version_code":        "100198030",
		"update_version_code":          "100198030",
		"resolution":                   "1080*1920",
		"dpi":                          "480",
		"device_type":                  "ABR-AL80",
		"device_brand":                 "HUAWEI",
		"language":                     "zh",
		"os_api":                       "35",
		"os_version":                   "15",
		"ac":                           "wifi",
		"device_model":                 "ABR-AL80",
		"save_power":                   "0",
		"font_size":                    "1.00",
		"luna_first_launch_apk_type":   "normal_apk",
		"diversion_channel_name":       "xiaomi_8478_64",
		"is_car_play":                  "0",
		"battery":                      "0.99",
		"network_speed":                "10156",
		"hybrid_version_code":          "100198030",
		"tz_name":                      "Asia/Shanghai",
		"tz_offset":                    "28800",
		"luna_register_time":           "1784311292",
		"diversion_category_level_two": "Xiaomi%E5%95%86%E5%BA%97-%E8%87%AA%E7%84%B6",
		"package":                      "com.luna.music",
		"charge":                       "0",
		"luna_apk_type":                "normal_apk",
		"output_device_type":           "Phone",
		"volume":                       "1.00",
		"brightness":                   "0.08",
		"need_personal_recommend":      "1",
		"is_teen_mode":                 "0",
		"sim_region":                   "cn",
		"diversion_category_level_one": "%E5%8E%82%E5%95%86%E5%95%86%E5%BA%97-%E8%87%AA%E7%84%B6",
		"android_device_type":          "default",
		"iid":                          "2204957404569386",
		"device_id":                    "2204957404565290",
		"_rticket":                     strconv.FormatInt(time.Now().UnixMilli(), 10),
	}
	for key, value := range values {
		params.Set(key, value)
	}
	return params
}

func sodaPCAppParams() url.Values {
	now := time.Now().UnixMilli()
	deviceID := strconv.FormatInt(now, 10)
	iid := strconv.FormatInt(now+1, 10)

	params := url.Values{}
	params.Set("aid", "386088")
	params.Set("app_name", "luna_pc")
	params.Set("region", "cn")
	params.Set("geo_region", "cn")
	params.Set("os_region", "cn")
	params.Set("sim_region", "")
	params.Set("device_id", deviceID)
	params.Set("cdid", "")
	params.Set("iid", iid)
	params.Set("version_name", "3.3.0")
	params.Set("version_code", "30030000")
	params.Set("channel", "official")
	params.Set("build_mode", "master")
	params.Set("network_carrier", "")
	params.Set("ac", "wifi")
	params.Set("tz_name", "Asia/Shanghai")
	params.Set("resolution", "")
	params.Set("device_platform", "windows")
	params.Set("device_type", "Windows")
	params.Set("os_version", "Windows 11")
	params.Set("fp", deviceID)
	return params
}

// Signed media URLs must not surface query credentials in logs or user errors.
func redactSodaURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "[invalid URL]"
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	parsed.User = nil
	return parsed.String()
}

func redactSodaRequestError(err error) error {
	var requestError *url.Error
	if errors.As(err, &requestError) {
		safe := *requestError
		safe.URL = redactSodaURL(requestError.URL)
		return &safe
	}
	return err
}
