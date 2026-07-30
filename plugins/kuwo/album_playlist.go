package kuwo

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"

	"github.com/liuran001/MusicBot-Go/bot/platform"
)

func albumUnavailable(albumID, reason string) error {
	base := platform.NewUnavailableError("kuwo", "album", albumID)
	if strings.TrimSpace(reason) == "" {
		return base
	}
	return fmt.Errorf("kuwo: %s: %w", reason, base)
}

// GetAlbumPlaylist renders an album as a playlist so an album link expands into
// its track list. Kuwo paginates albumInfo exactly like playListInfo — the same
// page window, the same out-of-range code -1 — so the traversal mirrors
// GetPlaylist rather than inventing a second pagination contract.
func (c *Client) GetAlbumPlaylist(
	ctx context.Context,
	albumID string,
	offset, limit int,
) (*platform.Playlist, error) {
	albumID = strings.TrimSpace(albumID)
	if !isASCIIUnsignedDecimal(albumID, 20) {
		return nil, platform.NewNotFoundError("kuwo", "album", albumID)
	}
	if c == nil {
		return nil, platform.NewUnavailableError("kuwo", "album", albumID)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = 50
	} else if limit > 100 {
		limit = 100
	}
	if offset > math.MaxInt-limit {
		return nil, platform.NewUnavailableError("kuwo", "album", albumID)
	}

	page, skip, ok := playlistPageWindow(uint64(offset), limit)
	if !ok {
		return nil, platform.NewUnavailableError("kuwo", "album", albumID)
	}

	first, total, err := c.fetchAlbumPage(ctx, albumID, page, limit)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	firstStart := skip
	if firstStart > len(first.MusicList) {
		firstStart = len(first.MusicList)
	}
	rawWindow := append([]trackWire(nil), first.MusicList[firstStart:]...)

	pageBase := int64(page-1) * int64(limit)
	needSecond := false
	if skip > 0 && int64(total) > pageBase {
		needSecond = int64(total)-pageBase > int64(limit)
	}
	if needSecond {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		second, secondTotal, err := c.fetchAlbumPage(ctx, albumID, page+1, limit)
		if err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if secondTotal != total {
			return nil, albumUnavailable(albumID, "album total changed between pages")
		}
		secondCount := skip
		if secondCount > len(second.MusicList) {
			secondCount = len(second.MusicList)
		}
		rawWindow = append(rawWindow, second.MusicList[:secondCount]...)
	}
	if len(rawWindow) > limit {
		rawWindow = rawWindow[:limit]
	}

	tracks := make([]platform.Track, 0, len(rawWindow))
	for _, item := range rawWindow {
		detail, _, ok := convertTrack(item)
		if ok {
			tracks = append(tracks, detail.Track)
		}
	}

	title := scalarText(first.Album)
	if title == "" {
		return nil, platform.NewNotFoundError("kuwo", "album", albumID)
	}
	return &platform.Playlist{
		ID:          encodeAlbumCollectionID(albumID),
		Platform:    "kuwo",
		Title:       title,
		Description: cleanUpstreamText(scalarText(first.AlbumInfo)),
		CoverURL:    normalizeCoverURL(scalarText(first.Pic)),
		Creator:     scalarText(first.Artist),
		TrackCount:  total,
		Tracks:      tracks,
		URL:         buildAlbumURL(albumID),
	}, nil
}

func (c *Client) fetchAlbumPage(
	ctx context.Context,
	albumID string,
	page, limit int,
) (*albumWire, int, error) {
	if page < 1 || page > maxKuwoPlaylistPage || limit < 1 || limit > 100 {
		return nil, 0, platform.NewUnavailableError("kuwo", "album", albumID)
	}
	endpoint := kuwoAlbumURL
	if c != nil && strings.TrimSpace(c.endpoints.album) != "" {
		endpoint = c.endpoints.album
	}
	requestURL, err := url.Parse(endpoint)
	if err != nil {
		return nil, 0, fmt.Errorf("kuwo: parse album URL: %w", err)
	}
	query := requestURL.Query()
	query.Set("albumId", albumID)
	query.Set("pn", strconv.Itoa(page))
	query.Set("rn", strconv.Itoa(limit))
	query.Set("httpsStatus", "1")
	requestURL.RawQuery = query.Encode()

	body, err := c.signedGet(ctx, requestURL.String(), buildAlbumURL(albumID))
	if err != nil {
		return nil, 0, err
	}
	if err := validateUniqueJSONKeys(body); err != nil {
		return nil, 0, albumUnavailable(albumID, "invalid album response JSON")
	}
	var response albumResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, 0, fmt.Errorf("kuwo: decode album response: %w", err)
	}
	// Upstream answers an out-of-range page with code -1 and a null payload,
	// which means the page does not exist rather than the album being broken.
	code, ok := playlistResponseCode(response.Code)
	if ok && code == -1 {
		return nil, 0, platform.NewNotFoundError("kuwo", "album", albumID)
	}
	if !ok || code != 200 || response.Data == nil {
		return nil, 0, albumUnavailable(albumID, "invalid album response")
	}
	responseID, ok := scalarASCIIUnsignedDecimal(response.Data.AlbumID, 20)
	if !ok || responseID != albumID {
		return nil, 0, albumUnavailable(albumID, "album identity mismatch")
	}
	total, ok := scalarNonNegativeInt(response.Data.Total)
	if !ok {
		return nil, 0, albumUnavailable(albumID, "invalid album total")
	}

	pageBase := int64(page-1) * int64(limit)
	remaining := 0
	if int64(total) > pageBase {
		difference := int64(total) - pageBase
		if difference > int64(limit) {
			remaining = limit
		} else {
			remaining = int(difference)
		}
	}
	if len(response.Data.MusicList) != remaining {
		return nil, 0, albumUnavailable(
			albumID,
			fmt.Sprintf("album page length %d does not match expected %d", len(response.Data.MusicList), remaining),
		)
	}
	return response.Data, total, nil
}
