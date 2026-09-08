package soda

import (
	"context"
	"fmt"
	"strings"

	"github.com/liuran001/MusicBot-Go/bot/platform"
)

// sodaAPIStrategy identifies the two independently usable Soda API paths.
// Legacy keeps the existing BDMS-signed PC implementation; upstream uses the
// current public search/H5 contracts from upstream MusicBot-Go.
type sodaAPIStrategy string

const (
	sodaAPIStrategyLegacy   sodaAPIStrategy = "legacy"
	sodaAPIStrategyUpstream sodaAPIStrategy = "upstream"
	sodaAPIStrategyAuto     sodaAPIStrategy = "auto"
)

func normalizeSodaAPIStrategy(raw string) (sodaAPIStrategy, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "legacy", "signer", "pc_signed":
		return sodaAPIStrategyLegacy, nil
	case "upstream", "h5":
		return sodaAPIStrategyUpstream, nil
	case "auto":
		return sodaAPIStrategyAuto, nil
	default:
		return "", fmt.Errorf("soda: unsupported api_strategy %q (want legacy, upstream, or auto)", raw)
	}
}

// SetAPIStrategy selects the Soda API path for subsequent operations. An empty
// value preserves the production legacy path for backwards compatibility.
func (c *Client) SetAPIStrategy(raw string) error {
	if c == nil {
		return nil
	}
	strategy, err := normalizeSodaAPIStrategy(raw)
	if err != nil {
		return err
	}
	c.apiStrategy = strategy
	return nil
}

// APIStrategy returns the effective strategy name for status and diagnostics.
func (c *Client) APIStrategy() string {
	if c == nil || c.apiStrategy == "" {
		return string(sodaAPIStrategyLegacy)
	}
	return string(c.apiStrategy)
}

// StrategyHighlights exposes the selected path without leaking signer URLs or
// tokens into /status. It is intentionally concise because detailed account
// status is rendered only for administrators.
func (c *Client) StrategyHighlights() []string {
	switch c.effectiveAPIStrategy() {
	case sodaAPIStrategyUpstream:
		return []string{
			"API 方案：上游公共搜索 / H5",
		}
	case sodaAPIStrategyAuto:
		return []string{
			"API 方案：自动回退",
			"调用顺序：上游公共搜索 / H5 → BDMS signer / PC",
		}
	default:
		return []string{
			"API 方案：BDMS signer / PC",
		}
	}
}

func (c *Client) effectiveAPIStrategy() sodaAPIStrategy {
	if c == nil || c.apiStrategy == "" {
		return sodaAPIStrategyLegacy
	}
	return c.apiStrategy
}

// CanAccessCoreContent probes the content endpoint selected by the current
// strategy. Auto accepts either path so account status does not report a
// healthy installation as unavailable while one implementation is rotating.
func (c *Client) CanAccessCoreContent(ctx context.Context) bool {
	switch c.effectiveAPIStrategy() {
	case sodaAPIStrategyUpstream:
		return c.canAccessCoreContentUpstream(ctx)
	case sodaAPIStrategyAuto:
		return c.canAccessCoreContentUpstream(ctx) || c.canAccessCoreContentLegacy(ctx)
	default:
		return c.canAccessCoreContentLegacy(ctx)
	}
}

func sodaStrategyCanFallback(ctx context.Context) bool {
	return ctx == nil || ctx.Err() == nil
}

func (c *Client) logStrategyFallback(operation string, primary, fallback sodaAPIStrategy, primaryErr, fallbackErr error) {
	if c == nil || c.logger == nil {
		return
	}
	args := []any{"operation", operation, "from", primary, "to", fallback}
	if primaryErr != nil {
		args = append(args, "primary_error", primaryErr)
	} else {
		args = append(args, "reason", "empty result")
	}
	if fallbackErr != nil {
		args = append(args, "fallback_error", fallbackErr)
	}
	c.logger.Warn("soda: API strategy fallback", args...)
}

// Search dispatches to the configured strategy. Auto tries the lightweight
// upstream path first and uses the signed legacy path for errors or an empty
// result.
func (c *Client) Search(ctx context.Context, keyword string, limit int) ([]platform.Track, error) {
	switch c.effectiveAPIStrategy() {
	case sodaAPIStrategyUpstream:
		return c.searchUpstream(ctx, keyword, limit)
	case sodaAPIStrategyAuto:
		tracks, upstreamErr := c.searchUpstream(ctx, keyword, limit)
		if upstreamErr == nil && len(tracks) > 0 {
			return tracks, nil
		}
		if !sodaStrategyCanFallback(ctx) {
			return nil, ctx.Err()
		}
		legacyTracks, legacyErr := c.searchLegacy(ctx, keyword, limit)
		if legacyErr == nil {
			c.logStrategyFallback("search", sodaAPIStrategyUpstream, sodaAPIStrategyLegacy, upstreamErr, nil)
			return legacyTracks, nil
		}
		c.logStrategyFallback("search", sodaAPIStrategyUpstream, sodaAPIStrategyLegacy, upstreamErr, legacyErr)
		if upstreamErr != nil {
			return nil, fmt.Errorf("soda: upstream search failed: %v; legacy search failed: %w", upstreamErr, legacyErr)
		}
		return nil, legacyErr
	default:
		return c.searchLegacy(ctx, keyword, limit)
	}
}

// GetTrack dispatches track metadata and lyrics using the configured strategy.
func (c *Client) GetTrack(ctx context.Context, trackID string) (*platform.Track, string, error) {
	switch c.effectiveAPIStrategy() {
	case sodaAPIStrategyUpstream:
		return c.getTrackUpstream(ctx, trackID)
	case sodaAPIStrategyAuto:
		track, lyric, upstreamErr := c.getTrackUpstream(ctx, trackID)
		if upstreamErr == nil && track != nil {
			return track, lyric, nil
		}
		if !sodaStrategyCanFallback(ctx) {
			return nil, "", ctx.Err()
		}
		legacyTrack, legacyLyric, legacyErr := c.getTrackLegacy(ctx, trackID)
		if legacyErr == nil {
			c.logStrategyFallback("track", sodaAPIStrategyUpstream, sodaAPIStrategyLegacy, upstreamErr, nil)
			return legacyTrack, legacyLyric, nil
		}
		c.logStrategyFallback("track", sodaAPIStrategyUpstream, sodaAPIStrategyLegacy, upstreamErr, legacyErr)
		if upstreamErr != nil {
			return nil, "", fmt.Errorf("soda: upstream track failed: %v; legacy track failed: %w", upstreamErr, legacyErr)
		}
		return nil, "", legacyErr
	default:
		return c.getTrackLegacy(ctx, trackID)
	}
}

// GetPlaylist dispatches playlist metadata and tracks using the configured
// strategy. Album links continue to use the shared HTML implementation.
func (c *Client) GetPlaylist(ctx context.Context, playlistID string) (*platform.Playlist, error) {
	switch c.effectiveAPIStrategy() {
	case sodaAPIStrategyUpstream:
		return c.getPlaylistUpstream(ctx, playlistID)
	case sodaAPIStrategyAuto:
		playlist, upstreamErr := c.getPlaylistUpstream(ctx, playlistID)
		if upstreamErr == nil && playlist != nil && (len(playlist.Tracks) > 0 || playlist.TrackCount <= 0) {
			return playlist, nil
		}
		if !sodaStrategyCanFallback(ctx) {
			return nil, ctx.Err()
		}
		legacyPlaylist, legacyErr := c.getPlaylistLegacy(ctx, playlistID)
		if legacyErr == nil {
			c.logStrategyFallback("playlist", sodaAPIStrategyUpstream, sodaAPIStrategyLegacy, upstreamErr, nil)
			return legacyPlaylist, nil
		}
		c.logStrategyFallback("playlist", sodaAPIStrategyUpstream, sodaAPIStrategyLegacy, upstreamErr, legacyErr)
		if upstreamErr != nil {
			return nil, fmt.Errorf("soda: upstream playlist failed: %v; legacy playlist failed: %w", upstreamErr, legacyErr)
		}
		return nil, legacyErr
	default:
		return c.getPlaylistLegacy(ctx, playlistID)
	}
}

// SearchPlaylist dispatches playlist search using the configured strategy.
func (c *Client) SearchPlaylist(ctx context.Context, keyword string, limit int) ([]platform.Playlist, error) {
	switch c.effectiveAPIStrategy() {
	case sodaAPIStrategyUpstream:
		return c.searchPlaylistUpstream(ctx, keyword, limit)
	case sodaAPIStrategyAuto:
		playlists, upstreamErr := c.searchPlaylistUpstream(ctx, keyword, limit)
		if upstreamErr == nil && len(playlists) > 0 {
			return playlists, nil
		}
		if !sodaStrategyCanFallback(ctx) {
			return nil, ctx.Err()
		}
		legacyPlaylists, legacyErr := c.searchPlaylistLegacy(ctx, keyword, limit)
		if legacyErr == nil {
			c.logStrategyFallback("playlist search", sodaAPIStrategyUpstream, sodaAPIStrategyLegacy, upstreamErr, nil)
			return legacyPlaylists, nil
		}
		c.logStrategyFallback("playlist search", sodaAPIStrategyUpstream, sodaAPIStrategyLegacy, upstreamErr, legacyErr)
		if upstreamErr != nil {
			return nil, fmt.Errorf("soda: upstream playlist search failed: %v; legacy playlist search failed: %w", upstreamErr, legacyErr)
		}
		return nil, legacyErr
	default:
		return c.searchPlaylistLegacy(ctx, keyword, limit)
	}
}

// FetchDownloadInfo dispatches playback resolution. Auto tries the lightweight
// upstream H5 path first, then uses the signer/high-quality path when upstream
// returns an error or only exposes a preview stream.
func (c *Client) FetchDownloadInfo(ctx context.Context, trackID string, quality platform.Quality) (*platform.DownloadInfo, error) {
	switch c.effectiveAPIStrategy() {
	case sodaAPIStrategyUpstream:
		return c.fetchDownloadInfoUpstream(ctx, trackID, quality)
	case sodaAPIStrategyAuto:
		info, upstreamErr := c.fetchDownloadInfoUpstream(ctx, trackID, quality)
		if upstreamErr == nil && info != nil && strings.TrimSpace(info.URL) != "" {
			return info, nil
		}
		if !sodaStrategyCanFallback(ctx) {
			return nil, ctx.Err()
		}
		legacyInfo, legacyErr := c.fetchDownloadInfoLegacy(ctx, trackID, quality)
		if legacyErr == nil {
			c.logStrategyFallback("download info", sodaAPIStrategyUpstream, sodaAPIStrategyLegacy, upstreamErr, nil)
			return legacyInfo, nil
		}
		c.logStrategyFallback("download info", sodaAPIStrategyUpstream, sodaAPIStrategyLegacy, upstreamErr, legacyErr)
		if upstreamErr != nil {
			return nil, fmt.Errorf("soda: upstream download info failed: %v; legacy download info failed: %w", upstreamErr, legacyErr)
		}
		return nil, legacyErr
	default:
		return c.fetchDownloadInfoLegacy(ctx, trackID, quality)
	}
}
