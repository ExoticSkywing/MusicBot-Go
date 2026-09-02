package kugou

import (
	"context"
	"errors"
	"strings"

	"github.com/liuran001/MusicBot-Go/bot/platform"
	"github.com/liuran001/MusicBot-Go/plugins/thirdparty"
)

type kugouDownloadResolver func(context.Context, string, platform.Quality) (*platform.DownloadInfo, error)

// ConfigureThirdPartyAudio installs an optional audio-only source chain.
// Kugou remains responsible for search, metadata, covers, lyrics, and links.
func (k *KugouPlatform) ConfigureThirdPartyAudio(mode thirdparty.Mode, resolver thirdparty.Resolver) {
	if k == nil {
		return
	}
	k.thirdPartyMode = mode
	k.thirdPartyResolver = resolver
}

func (k *KugouPlatform) thirdPartyAudioAvailable() bool {
	return k != nil && k.thirdPartyResolver != nil && k.thirdPartyMode == thirdparty.ModeThirdPartyFirst
}

func (k *KugouPlatform) thirdPartyStatusLines() []string {
	if k == nil || k.thirdPartyResolver == nil || k.thirdPartyMode == thirdparty.ModeDisabled {
		return nil
	}
	names := []string{"第三方"}
	if named, ok := k.thirdPartyResolver.(interface{ ProviderNames() []string }); ok {
		if configured := named.ProviderNames(); len(configured) > 0 {
			names = configured
		}
	}
	switch k.thirdPartyMode {
	case thirdparty.ModeThirdPartyFirst:
		return []string{
			"音源策略：第三方优先",
			"调用顺序：" + strings.Join(append(names, "酷狗官方"), " → "),
		}
	case thirdparty.ModeOfficialFirst:
		return []string{
			"音源策略：官方优先",
			"调用顺序：" + strings.Join(append([]string{"酷狗官方"}, names...), " → "),
		}
	default:
		return nil
	}
}

func resolveKugouDownload(
	ctx context.Context,
	mode thirdparty.Mode,
	official kugouDownloadResolver,
	alternative kugouDownloadResolver,
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
	// Preserve the official error for users. Provider-specific failures are
	// already logged by the third-party chain.
	return nil, officialErr
}

func (k *KugouPlatform) getThirdPartyDownloadInfo(ctx context.Context, trackID string, quality platform.Quality) (*platform.DownloadInfo, error) {
	if k == nil || k.thirdPartyResolver == nil {
		return nil, platform.NewUnavailableError("kugou", "track", trackID)
	}
	hash := normalizeHash(trackID)
	if hash == "" && k.client != nil {
		song, err := k.client.GetTrack(ctx, trackID)
		if err != nil {
			return nil, err
		}
		if song != nil {
			hash = normalizeHash(song.ID)
			if hash == "" && song.Extra != nil {
				hash = normalizeHash(song.Extra["hash"])
			}
		}
	}
	if hash == "" {
		return nil, platform.NewNotFoundError("kugou", "track", trackID)
	}
	return k.thirdPartyResolver.Resolve(ctx, "kugou", hash, quality)
}
