package kuwo

import (
	"context"
	"errors"
	"strings"

	"github.com/liuran001/MusicBot-Go/bot/platform"
	"github.com/liuran001/MusicBot-Go/plugins/thirdparty"
)

type kuwoDownloadResolver func(context.Context, string, platform.Quality) (*platform.DownloadInfo, error)

// ConfigureThirdPartyAudio installs an optional audio-only source chain.
// Kuwo remains responsible for search, metadata, covers, lyrics, and links.
func (p *KuwoPlatform) ConfigureThirdPartyAudio(mode thirdparty.Mode, resolver thirdparty.Resolver) {
	if p == nil {
		return
	}
	p.thirdPartyMode = mode
	p.thirdPartyResolver = resolver
}

func (p *KuwoPlatform) thirdPartyAudioAvailable() bool {
	return p != nil && p.thirdPartyResolver != nil && p.thirdPartyMode == thirdparty.ModeThirdPartyFirst
}

func (p *KuwoPlatform) thirdPartyStatusLines() []string {
	if p == nil || p.thirdPartyResolver == nil || p.thirdPartyMode == thirdparty.ModeDisabled {
		return nil
	}
	names := []string{"第三方"}
	if named, ok := p.thirdPartyResolver.(interface{ ProviderNames() []string }); ok {
		if configured := named.ProviderNames(); len(configured) > 0 {
			names = configured
		}
	}
	switch p.thirdPartyMode {
	case thirdparty.ModeThirdPartyFirst:
		return []string{
			"音源策略：第三方优先",
			"调用顺序：" + strings.Join(append(names, "酷我官方"), " → "),
		}
	case thirdparty.ModeOfficialFirst:
		return []string{
			"音源策略：官方优先",
			"调用顺序：" + strings.Join(append([]string{"酷我官方"}, names...), " → "),
		}
	default:
		return nil
	}
}

func resolveKuwoDownload(
	ctx context.Context,
	mode thirdparty.Mode,
	official kuwoDownloadResolver,
	alternative kuwoDownloadResolver,
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
	return nil, officialErr
}

func (p *KuwoPlatform) getThirdPartyDownloadInfo(ctx context.Context, trackID string, quality platform.Quality) (*platform.DownloadInfo, error) {
	if p == nil || p.thirdPartyResolver == nil {
		return nil, platform.NewUnavailableError("kuwo", "track", trackID)
	}
	rid := normalizeRID(trackID)
	if rid == "" {
		return nil, platform.NewNotFoundError("kuwo", "track", trackID)
	}
	return p.thirdPartyResolver.Resolve(ctx, "kuwo", rid, quality)
}
