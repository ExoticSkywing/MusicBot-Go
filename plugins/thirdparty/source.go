package thirdparty

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/liuran001/MusicBot-Go/bot"
	"github.com/liuran001/MusicBot-Go/bot/platform"
)

// Mode controls whether third-party audio sources are disabled, tried before
// the official platform, or used only after the official platform fails.
type Mode string

const (
	ModeDisabled        Mode = "disabled"
	ModeOfficialFirst   Mode = "official_first"
	ModeThirdPartyFirst Mode = "third_party_first"
)

// ParseMode accepts the documented mode names and keeps an empty value
// backward-compatible by disabling third-party sources.
func ParseMode(raw string) (Mode, error) {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	normalized = strings.ReplaceAll(normalized, "-", "_")
	switch normalized {
	case "", "disabled", "disable", "off", "false", "0":
		return ModeDisabled, nil
	case "official_first", "fallback":
		return ModeOfficialFirst, nil
	case "third_party_first", "prefer":
		return ModeThirdPartyFirst, nil
	default:
		return ModeDisabled, fmt.Errorf("unknown third-party mode %q", raw)
	}
}

// ParseProviderNames parses a comma, semicolon, or whitespace-separated
// provider list while preserving the configured priority and removing repeats.
func ParseProviderNames(raw string) []string {
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
	seen := make(map[string]struct{}, len(parts))
	names := make([]string, 0, len(parts))
	for _, part := range parts {
		name := strings.ToLower(strings.TrimSpace(part))
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	return names
}

// Resolver resolves a platform track ID to a directly downloadable audio file.
// Implementations must report the actual returned format and quality rather
// than copying the requested quality blindly.
type Resolver interface {
	Resolve(ctx context.Context, platformName, trackID string, quality platform.Quality) (*platform.DownloadInfo, error)
}

type provider interface {
	Name() string
	Resolve(ctx context.Context, platformName, trackID string, quality platform.Quality) (*platform.DownloadInfo, error)
}

// Chain tries providers in configuration order. Provider failures are logged
// internally and deliberately collapse to the platform's normal unavailable
// error so third-party implementation details never leak to bot users.
type Chain struct {
	providers          []provider
	perProviderTimeout time.Duration
	logger             bot.Logger
}

// ProviderNames returns the configured providers in routing order.
func (c *Chain) ProviderNames() []string {
	if c == nil {
		return nil
	}
	names := make([]string, 0, len(c.providers))
	for _, item := range c.providers {
		if item != nil && strings.TrimSpace(item.Name()) != "" {
			names = append(names, item.Name())
		}
	}
	return names
}

// NewChain constructs the configured source chain. Unknown source names are a
// startup error instead of being silently ignored due to a configuration typo.
func NewChain(names []string, timeout time.Duration, logger bot.Logger) (*Chain, error) {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	providers := make([]provider, 0, len(names))
	for _, name := range names {
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "jbsou":
			item, err := newJBSouProvider(defaultJBSouBaseURL, timeout, nil, isQQMusicMediaURL)
			if err != nil {
				return nil, err
			}
			providers = append(providers, item)
		case "":
			continue
		default:
			return nil, fmt.Errorf("unknown third-party provider %q", name)
		}
	}
	if len(providers) == 0 {
		return nil, fmt.Errorf("at least one third-party provider is required")
	}
	return &Chain{providers: providers, perProviderTimeout: timeout, logger: logger}, nil
}

func (c *Chain) Resolve(ctx context.Context, platformName, trackID string, quality platform.Quality) (*platform.DownloadInfo, error) {
	platformName = strings.ToLower(strings.TrimSpace(platformName))
	trackID = strings.TrimSpace(trackID)
	if c == nil || len(c.providers) == 0 || platformName == "" || trackID == "" {
		return nil, platform.NewUnavailableError(platformName, "track", trackID)
	}

	for _, item := range c.providers {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		providerCtx, cancel := context.WithTimeout(ctx, c.perProviderTimeout)
		info, err := item.Resolve(providerCtx, platformName, trackID, quality)
		cancel()
		if err == nil && validDownloadInfo(info) {
			if c.logger != nil {
				c.logger.Info("third-party audio source resolved track", "provider", item.Name(), "platform", platformName, "track_id", trackID, "format", info.Format, "size", info.Size)
			}
			return info, nil
		}
		if err == nil {
			err = fmt.Errorf("provider returned incomplete download info")
		}
		if c.logger != nil {
			c.logger.Warn("third-party audio source failed", "provider", item.Name(), "platform", platformName, "track_id", trackID, "error", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, platform.NewUnavailableError(platformName, "track", trackID)
}

func validDownloadInfo(info *platform.DownloadInfo) bool {
	return info != nil && strings.TrimSpace(info.URL) != "" && strings.TrimSpace(info.Format) != "" && info.Size > 0
}
