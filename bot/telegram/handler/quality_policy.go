package handler

import (
	"context"
	"strings"

	botpkg "github.com/liuran001/MusicBot-Go/bot"
	"github.com/liuran001/MusicBot-Go/bot/platform"
	"github.com/liuran001/MusicBot-Go/plugins/applemusic"
	"github.com/liuran001/MusicBot-Go/plugins/kugou"
)

const appleMusicPlatformName = "applemusic"

const implicitQualityPrefix = "auto-"

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

// qualityIntentValue decodes the quality carried by callbacks and inline
// result IDs. Legacy concrete values remain explicit. New implicit requests
// carry the resolved default as "auto-<quality>" so pagination and inline
// hand-offs do not accidentally turn a default into a user override.
func qualityIntentValue(value string) (qualityValue string, explicit bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "", false
	}
	if strings.HasPrefix(value, implicitQualityPrefix) {
		candidate := strings.TrimPrefix(value, implicitQualityPrefix)
		if _, err := platform.ParseQuality(candidate); err == nil {
			return candidate, false
		}
	}
	return value, true
}

func implicitQualityToken(qualityValue string) string {
	qualityValue = strings.ToLower(strings.TrimSpace(qualityValue))
	if _, err := platform.ParseQuality(qualityValue); err != nil {
		return ""
	}
	return implicitQualityPrefix + qualityValue
}

func qualityIntentToken(qualityValue string, explicit bool) string {
	qualityValue = strings.ToLower(strings.TrimSpace(qualityValue))
	if explicit {
		return qualityValue
	}
	return implicitQualityToken(qualityValue)
}

func preferAppleMusicAtmosEnabled(
	ctx context.Context,
	repo botpkg.SongRepository,
	manager platform.Manager,
	scopeType string,
	scopeID int64,
	platformName string,
	explicitQuality bool,
) bool {
	if explicitQuality || repo == nil || scopeID == 0 || !platformSupportsAtmosSelection(manager, platformName) {
		return false
	}
	stored, err := repo.GetPluginSetting(ctx, scopeType, scopeID, applemusic.PreferAtmosDefinition().Plugin, applemusic.PreferAtmosKey)
	return err == nil && strings.EqualFold(strings.TrimSpace(stored), applemusic.PreferAtmosOn)
}

func preferredAppleMusicQuality(track *platform.Track, baseline platform.Quality, preferAtmos bool) (platform.Quality, bool) {
	if !preferAtmos || baseline == platform.QualityAtmos || track == nil || track.AtmosAvailable == nil || !*track.AtmosAvailable {
		return baseline, false
	}
	return platform.QualityAtmos, true
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
