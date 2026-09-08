package soda

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/liuran001/MusicBot-Go/bot/platform"
)

var errSodaIncompleteAudio = errors.New("soda: incomplete audio")

func (c *Client) searchUpstream(ctx context.Context, keyword string, limit int) ([]platform.Track, error) {
	if strings.TrimSpace(keyword) == "" {
		return nil, platform.NewNotFoundError("soda", "search", "")
	}
	if limit <= 0 {
		limit = 10
	}
	params := url.Values{}
	params.Set("q", keyword)
	params.Set("cursor", "0")
	body, err := c.getLunaJSON(ctx, "/luna/search/track", params)
	if err != nil {
		return nil, err
	}
	var resp sodaSearchResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("soda: parse upstream search response: %w", err)
	}
	tracks := make([]platform.Track, 0, limit)
	seen := make(map[string]struct{}, limit)
	for _, group := range resp.ResultGroups {
		for _, item := range group.Data {
			track := convertSodaTrack(item.Entity.Track)
			if track.ID == "" {
				continue
			}
			if _, exists := seen[track.ID]; exists {
				continue
			}
			seen[track.ID] = struct{}{}
			tracks = append(tracks, track)
			if len(tracks) >= limit {
				return tracks, nil
			}
		}
	}
	return tracks, nil
}

func (c *Client) getTrackUpstream(ctx context.Context, trackID string) (*platform.Track, string, error) {
	trackID = strings.TrimSpace(trackID)
	if trackID == "" {
		return nil, "", platform.NewNotFoundError("soda", "track", trackID)
	}
	resp, err := c.fetchTrackWeb(ctx, trackID)
	if err != nil {
		return nil, "", err
	}
	trackData := resp.TrackInfo
	if strings.TrimSpace(trackData.ID) == "" {
		trackData = resp.Track
	}
	track := convertSodaTrack(trackData)
	if track.ID == "" {
		return nil, "", platform.NewNotFoundError("soda", "track", trackID)
	}
	return &track, parseSodaLyric(resp.Lyric.Content), nil
}

func (c *Client) getPlaylistUpstream(ctx context.Context, playlistID string) (*platform.Playlist, error) {
	playlistID = strings.TrimSpace(playlistID)
	if playlistID == "" {
		return nil, platform.NewNotFoundError("soda", "playlist", playlistID)
	}
	offset := platform.PlaylistOffsetFromContext(ctx)
	if offset < 0 {
		offset = 0
	}
	limit := platform.PlaylistLimitFromContext(ctx)
	const defaultChunkSize = 20
	cursor := strconv.Itoa(offset)
	cursorPosition := offset
	if limit <= 0 {
		cursor = "0"
		cursorPosition = 0
	}
	var (
		playlist *platform.Playlist
		tracks   []platform.Track
		seen     = map[string]struct{}{}
		cursors  = map[string]struct{}{}
	)
	for {
		if _, repeated := cursors[cursor]; repeated {
			break
		}
		cursors[cursor] = struct{}{}
		cnt := defaultChunkSize
		if limit > 0 {
			remaining := limit - len(tracks)
			if remaining <= 0 {
				break
			}
			if remaining < cnt {
				cnt = remaining
			}
		}
		params := url.Values{}
		params.Set("playlist_id", playlistID)
		params.Set("cursor", cursor)
		params.Set("count", strconv.Itoa(cnt))
		body, err := c.getPCPlaylistJSON(ctx, params)
		if err != nil {
			return nil, err
		}
		var resp sodaPlaylistDetailResponse
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, fmt.Errorf("soda: parse upstream playlist response: %w", err)
		}
		if playlist == nil {
			playlist = convertSodaPlaylist(resp.Playlist)
			playlist.ID = playlistID
		}
		if playlist.TrackCount <= 0 {
			playlist.TrackCount = maxInt(resp.Playlist.TrackCount, resp.Playlist.CountTracks)
		}
		pageAdded := 0
		for _, item := range resp.MediaResources {
			if item.Type != "track" {
				continue
			}
			track := convertSodaTrack(item.Entity.TrackWrapper.Track)
			if track.ID == "" {
				continue
			}
			if _, exists := seen[track.ID]; exists {
				continue
			}
			seen[track.ID] = struct{}{}
			tracks = append(tracks, track)
			pageAdded++
		}
		if playlist == nil || pageAdded == 0 {
			break
		}
		if limit > 0 && len(tracks) >= limit {
			break
		}
		if resp.HasMore != nil && !*resp.HasMore {
			break
		}
		nextCursor := strings.TrimSpace(resp.NextCursor)
		nextPosition := cursorPosition + cnt
		if nextCursor == "" {
			nextCursor = strconv.Itoa(nextPosition)
		} else if parsed, parseErr := strconv.Atoi(nextCursor); parseErr == nil {
			nextPosition = parsed
		}
		if nextCursor == cursor {
			break
		}
		if playlist.TrackCount > 0 && nextPosition >= playlist.TrackCount {
			break
		}
		cursor = nextCursor
		cursorPosition = nextPosition
	}
	if playlist == nil {
		return nil, platform.NewNotFoundError("soda", "playlist", playlistID)
	}
	playlist.Tracks = tracks
	if playlist.TrackCount <= 0 {
		if offset > 0 {
			playlist.TrackCount = offset + len(tracks)
		} else {
			playlist.TrackCount = len(tracks)
		}
	}
	return playlist, nil
}

func (c *Client) searchPlaylistUpstream(ctx context.Context, keyword string, limit int) ([]platform.Playlist, error) {
	if strings.TrimSpace(keyword) == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 10
	}
	params := url.Values{}
	params.Set("q", keyword)
	params.Set("cursor", "0")
	body, err := c.getLunaJSON(ctx, "/luna/search/playlist", params)
	if err != nil {
		return nil, err
	}
	var resp sodaPlaylistSearchResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("soda: parse upstream playlist search response: %w", err)
	}
	playlists := make([]platform.Playlist, 0, limit)
	for _, group := range resp.ResultGroups {
		for _, item := range group.Data {
			playlist := convertSodaPlaylist(item.Entity.Playlist)
			if playlist.ID == "" {
				continue
			}
			playlists = append(playlists, *playlist)
			if len(playlists) >= limit {
				return playlists, nil
			}
		}
	}
	return playlists, nil
}

func (c *Client) fetchDownloadInfoUpstream(ctx context.Context, trackID string, quality platform.Quality) (*platform.DownloadInfo, error) {
	trackID = strings.TrimSpace(trackID)
	if trackID == "" {
		return nil, platform.NewNotFoundError("soda", "track", trackID)
	}
	resp, err := c.fetchTrackWeb(ctx, trackID)
	if err != nil {
		return nil, err
	}
	trackData := resp.TrackInfo
	if strings.TrimSpace(trackData.ID) == "" {
		trackData = resp.Track
	}
	if strings.TrimSpace(trackData.ID) == "" {
		return nil, platform.NewNotFoundError("soda", "track", trackID)
	}
	if mediaID, previewID := strings.TrimSpace(resp.TrackPlayer.MediaID), strings.TrimSpace(trackData.Preview.VID); mediaID != "" && previewID != "" && mediaID == previewID {
		return nil, fmt.Errorf("%w: %w: soda player selected the catalog preview media", platform.ErrIncompleteAudio, errSodaIncompleteAudio)
	}
	playerInfoURL := strings.TrimSpace(resp.TrackPlayer.URLPlayerInfo)
	if playerInfoURL == "" {
		return nil, fmt.Errorf("soda: player info url missing")
	}
	playInfos, err := c.fetchPlayInfosUpstream(ctx, playerInfoURL)
	if err != nil {
		return nil, fmt.Errorf("soda: fetch play infos: %w", err)
	}
	if len(playInfos) == 0 {
		return nil, platform.NewUnavailableError("soda", "track", trackID)
	}
	for i := range playInfos {
		if playInfos[i].Bitrate > 10000 {
			playInfos[i].Bitrate /= 1000
		}
		playInfos[i].Quality = strings.ToLower(strings.TrimSpace(playInfos[i].Quality))
	}
	completePlayInfos := make([]sodaPlayInfo, 0, len(playInfos))
	sawIncompleteDuration := false
	for _, item := range playInfos {
		if sodaPlayInfoIsShorterThanTrack(trackData.Duration, item.Duration) {
			sawIncompleteDuration = true
			continue
		}
		completePlayInfos = append(completePlayInfos, item)
	}
	if len(completePlayInfos) == 0 && sawIncompleteDuration {
		return nil, fmt.Errorf("%w: %w: soda player returned only shorter streams", platform.ErrIncompleteAudio, errSodaIncompleteAudio)
	}
	playInfos = completePlayInfos
	playInfo := selectSodaPlayInfo(playInfos, quality)
	if playInfo == nil {
		return nil, platform.NewUnavailableError("soda", "track", trackID)
	}
	rawURL := firstNonEmptyString(playInfo.MainPlayURL, playInfo.BackupPlayURL)
	if strings.TrimSpace(rawURL) == "" {
		return nil, platform.NewUnavailableError("soda", "track", trackID)
	}
	bitrate := playInfo.Bitrate
	if bitrate <= 0 && playInfo.Duration > 0 && playInfo.Size > 0 {
		bitrate = int(float64(playInfo.Size) * 8 / playInfo.Duration / 1000)
	}
	format := strings.TrimSpace(strings.ToLower(playInfo.Format))
	if format == "" {
		format = "m4a"
	}
	headers := map[string]string{"User-Agent": sodaUserAgent, "X-Soda-Play-Auth": playInfo.PlayAuth}
	qualityLevel := mapSodaQuality(playInfo, bitrate)
	candidates := make([]string, 0, 1)
	if backup := strings.TrimSpace(playInfo.BackupPlayURL); backup != "" && backup != rawURL {
		candidates = append(candidates, backup)
	}
	return &platform.DownloadInfo{
		URL:           rawURL,
		CandidateURLs: candidates,
		Headers:       headers,
		Size:          playInfo.Size,
		Format:        format,
		Bitrate:       bitrate,
		Quality:       qualityLevel,
		Downloader:    c.DownloadAndDecrypt,
	}, nil
}

func (c *Client) canAccessCoreContentUpstream(ctx context.Context) bool {
	resp, err := c.fetchTrackWeb(ctx, "7620326800652224539")
	if err != nil || resp == nil {
		return false
	}
	trackData := resp.TrackInfo
	if strings.TrimSpace(trackData.ID) == "" {
		trackData = resp.Track
	}
	if strings.TrimSpace(trackData.ID) == "" || strings.TrimSpace(resp.TrackPlayer.URLPlayerInfo) == "" {
		return false
	}
	playInfos, err := c.fetchPlayInfosUpstream(ctx, resp.TrackPlayer.URLPlayerInfo)
	if err != nil || len(playInfos) == 0 {
		return false
	}
	for _, info := range playInfos {
		if strings.TrimSpace(firstNonEmptyString(info.MainPlayURL, info.BackupPlayURL)) != "" {
			return true
		}
	}
	return false
}

// fetchPlayInfosUpstream uses the signed player-info URL as-is. The upstream
// H5 contract embeds its own authorization in that URL; routing it through the
// legacy BDMS signer would alter the request and can cause false failures.
func (c *Client) fetchPlayInfosUpstream(ctx context.Context, playerInfoURL string) ([]sodaPlayInfo, error) {
	if c == nil {
		return nil, platform.NewUnavailableError("soda", "track", "")
	}
	unsigned := &Client{httpClient: c.httpClient}
	return unsigned.fetchPlayInfos(ctx, playerInfoURL)
}

// sodaPlayInfoIsShorterThanTrack compares a media declaration in seconds with
// the catalog duration in milliseconds. It rejects a trial window while
// allowing normal catalog rounding and codec padding.
func sodaPlayInfoIsShorterThanTrack(trackDurationMS int, playDurationSeconds float64) bool {
	if trackDurationMS <= 0 || playDurationSeconds <= 0 {
		return false
	}
	catalogSeconds := float64(trackDurationMS) / 1000
	tolerance := catalogSeconds * 0.05
	if tolerance > 3 {
		tolerance = 3
	}
	return catalogSeconds-playDurationSeconds > tolerance
}
