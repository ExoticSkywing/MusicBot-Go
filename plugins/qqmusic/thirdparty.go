package qqmusic

import (
	"context"
	"errors"
	"strings"

	"github.com/liuran001/MusicBot-Go/bot/platform"
	"github.com/liuran001/MusicBot-Go/plugins/thirdparty"
)

type qqDownloadResolver func(context.Context, string, platform.Quality) (*platform.DownloadInfo, error)

// ConfigureThirdPartyAudio installs an optional audio-only source chain. QQ
// Music remains responsible for search, metadata, covers, lyrics, and links.
func (q *QQMusicPlatform) ConfigureThirdPartyAudio(mode thirdparty.Mode, resolver thirdparty.Resolver) {
	if q == nil {
		return
	}
	q.thirdPartyMode = mode
	q.thirdPartyResolver = resolver
}

func (q *QQMusicPlatform) thirdPartyAudioAvailable() bool {
	return q != nil && q.thirdPartyResolver != nil && q.thirdPartyMode == thirdparty.ModeThirdPartyFirst
}

func (q *QQMusicPlatform) thirdPartyStatusLines() []string {
	if q == nil || q.thirdPartyResolver == nil || q.thirdPartyMode == thirdparty.ModeDisabled {
		return nil
	}
	names := []string{"第三方"}
	if named, ok := q.thirdPartyResolver.(interface{ ProviderNames() []string }); ok {
		if configured := named.ProviderNames(); len(configured) > 0 {
			names = configured
		}
	}
	switch q.thirdPartyMode {
	case thirdparty.ModeThirdPartyFirst:
		return []string{
			"音源策略：第三方优先",
			"调用顺序：" + strings.Join(append(names, "QQ 官方"), " → "),
		}
	case thirdparty.ModeOfficialFirst:
		return []string{
			"音源策略：官方优先",
			"调用顺序：" + strings.Join(append([]string{"QQ 官方"}, names...), " → "),
		}
	default:
		return nil
	}
}

func resolveQQMusicDownload(
	ctx context.Context,
	mode thirdparty.Mode,
	official qqDownloadResolver,
	alternative qqDownloadResolver,
	trackID string,
	quality platform.Quality,
) (*platform.DownloadInfo, error) {
	if mode == thirdparty.ModeDisabled || alternative == nil {
		return official(ctx, trackID, quality)
	}

	if mode == thirdparty.ModeThirdPartyFirst {
		if info, err := alternative(ctx, trackID, quality); err == nil {
			return info, nil
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return official(ctx, trackID, quality)
	}

	info, officialErr := official(ctx, trackID, quality)
	if officialErr == nil {
		return info, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if info, err := alternative(ctx, trackID, quality); err == nil {
		return info, nil
	} else if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	// Keep the official error user-facing; provider-specific errors are logged
	// inside the third-party chain and should remain an implementation detail.
	return nil, officialErr
}

func (q *QQMusicPlatform) getThirdPartyDownloadInfo(ctx context.Context, trackID string, quality platform.Quality) (*platform.DownloadInfo, error) {
	if q == nil || q.thirdPartyResolver == nil {
		return nil, platform.NewUnavailableError("qqmusic", "track", trackID)
	}
	songMid := strings.TrimSpace(trackID)
	if isNumericID(songMid) && q.client != nil {
		detail, err := q.client.GetSongDetail(ctx, songMid)
		if err != nil {
			return nil, err
		}
		songMid = strings.TrimSpace(detail.Mid)
	}
	if songMid == "" {
		return nil, platform.NewNotFoundError("qqmusic", "track", trackID)
	}
	return q.thirdPartyResolver.Resolve(ctx, "qqmusic", songMid, quality)
}
