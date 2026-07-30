package kugou

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/liuran001/MusicBot-Go/bot/platform"
)

const kugouSingerInfoURL = "https://mobiles.kugou.com/api/v5/singer/info"

// kugouSingerInfo is the subset of /api/v5/singer/info this plugin consumes.
type kugouSingerInfo struct {
	Status  int    `json:"status"`
	ErrCode int    `json:"errcode"`
	Error   string `json:"error"`
	Data    struct {
		SingerID   json.Number `json:"singerid"`
		SingerName string      `json:"singername"`
		ImgURL     string      `json:"imgurl"`
		SongCount  int         `json:"songcount"`
		AlbumCount int         `json:"albumcount"`
	} `json:"data"`
}

// buildSingerAvatarURL fills in the {size} placeholder Kugou embeds in portrait
// URLs and upgrades the plain-HTTP host it returns.
func buildSingerAvatarURL(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return ""
	}
	rawURL = strings.ReplaceAll(rawURL, "{size}", "400")
	// A leftover placeholder would render as a broken image.
	if strings.ContainsAny(rawURL, "{}") {
		return ""
	}
	if strings.HasPrefix(rawURL, "http://") {
		rawURL = "https://" + strings.TrimPrefix(rawURL, "http://")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return ""
	}
	return rawURL
}

// convertSingerInfo maps a singer/info payload onto the unified artist type.
// It is deliberately separate from the request so the mapping — including the
// identity guard — is testable without network access.
func convertSingerInfo(response *kugouSingerInfo, artistID string) (*platform.Artist, int, error) {
	if response == nil || response.Status != 1 {
		return nil, 0, platform.NewNotFoundError("kugou", "artist", artistID)
	}
	name := strings.TrimSpace(response.Data.SingerName)
	if name == "" {
		return nil, 0, platform.NewNotFoundError("kugou", "artist", artistID)
	}
	// Identity guard: never present a different artist under the requested ID.
	if responseID := strings.TrimSpace(response.Data.SingerID.String()); responseID != "" && responseID != artistID {
		return nil, 0, platform.NewUnavailableError("kugou", "artist", artistID)
	}

	artist := &platform.Artist{
		ID:        artistID,
		Platform:  "kugou",
		Name:      name,
		AvatarURL: buildSingerAvatarURL(response.Data.ImgURL),
		URL:       buildArtistURL(artistID),
	}
	trackCount := response.Data.SongCount
	if trackCount < 0 {
		trackCount = 0
	}
	return artist, trackCount, nil
}

// GetSingerInfo fetches artist metadata. Kugou artist IDs are bare integers —
// the same ones that appear in singer page URLs.
func (c *Client) GetSingerInfo(ctx context.Context, artistID string) (*platform.Artist, int, error) {
	artistID = strings.TrimSpace(artistID)
	if _, err := strconv.ParseUint(artistID, 10, 64); err != nil {
		return nil, 0, platform.NewNotFoundError("kugou", "artist", artistID)
	}
	if c == nil {
		return nil, 0, platform.NewUnavailableError("kugou", "artist", artistID)
	}

	query := url.Values{}
	query.Set("singerid", artistID)

	var response kugouSingerInfo
	if err := c.doJSONRequest(ctx, http.MethodGet, kugouSingerInfoURL, query, nil, map[string]string{
		"User-Agent": "Mozilla/5.0",
		"Referer":    "https://www.kugou.com/",
	}, &response); err != nil {
		return nil, 0, platform.NewUnavailableError("kugou", "artist", artistID)
	}
	return convertSingerInfo(&response, artistID)
}
