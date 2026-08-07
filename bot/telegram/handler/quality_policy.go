package handler

import (
	"context"
	"strings"

	botpkg "github.com/liuran001/MusicBot-Go/bot"
	"github.com/liuran001/MusicBot-Go/bot/platform"
	"github.com/liuran001/MusicBot-Go/plugins/kugou"
)

const appleMusicPlatformName = "applemusic"

func isAppleMusicPlatform(platformName string) bool {
	return strings.EqualFold(strings.TrimSpace(platformName), appleMusicPlatformName)
}

// qualityValueForPlatform prevents the non-linear Atmos resource type from
// leaking into providers that only understand the four stereo quality tiers.
// The command parser rejects explicit non-Apple Atmos selections; this fallback
// also covers stale settings and forged callback payloads.
func qualityValueForPlatform(platformName, qualityValue string) string {
	qualityValue = strings.TrimSpace(strings.ToLower(qualityValue))
	if qualityValue == platform.QualityAtmos.String() && !isAppleMusicPlatform(platformName) {
		return platform.QualityHiRes.String()
	}
	return qualityValue
}

// platformSupportsAtmosSelection controls whether the settings UI exposes the
// Atmos button. Keep the Apple Music name check even though capabilities are
// generic: today Atmos is intentionally a provider-specific opt-in rather than
// a fifth item in the shared linear quality ladder.
func platformSupportsAtmosSelection(manager platform.Manager, platformName string) bool {
	if manager == nil || !isAppleMusicPlatform(platformName) {
		return false
	}
	provider := manager.Get(strings.ToLower(strings.TrimSpace(platformName)))
	return provider != nil && provider.Capabilities().Atmos
}

func qualitySelectableForPlatform(manager platform.Manager, platformName, qualityValue string) bool {
	quality, err := platform.ParseQuality(strings.TrimSpace(strings.ToLower(qualityValue)))
	if err != nil {
		return false
	}
	if quality != platform.QualityAtmos {
		return true
	}
	return platformSupportsAtmosSelection(manager, platformName)
}

func resolvePlatformQualityValue(ctx context.Context, repo botpkg.SongRepository, scopeType string, scopeID int64, platformName, qualityValue string, explicitOverride bool) string {
	platformName = strings.TrimSpace(strings.ToLower(platformName))
	qualityValue = qualityValueForPlatform(platformName, qualityValue)
	if explicitOverride || platformName != "kugou" || qualityValue != "hires" {
		return qualityValue
	}
	enabled := true
	if repo != nil && scopeID != 0 {
		if stored, err := repo.GetPluginSetting(ctx, scopeType, scopeID, "kugou", kugou.NoHiResWhenDefaultKey); err == nil && strings.TrimSpace(stored) != "" {
			enabled = strings.EqualFold(strings.TrimSpace(stored), kugou.NoHiResWhenDefaultOn)
		}
	}
	if enabled {
		return "lossless"
	}
	return qualityValue
}
