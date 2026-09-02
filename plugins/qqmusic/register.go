package qqmusic

import (
	"fmt"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/config"
	logpkg "github.com/liuran001/MusicBot-Go/bot/logger"
	platformplugins "github.com/liuran001/MusicBot-Go/bot/platform/plugins"
	"github.com/liuran001/MusicBot-Go/plugins/thirdparty"
)

func init() {
	if err := platformplugins.Register("qqmusic", buildContribution); err != nil {
		panic(err)
	}
}

func buildContribution(cfg *config.Config, logger *logpkg.Logger) (*platformplugins.Contribution, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config required")
	}
	cookie := cfg.GetPluginString("qqmusic", "cookie")
	timeoutSec := cfg.GetPluginInt("qqmusic", "timeout")
	if timeoutSec <= 0 {
		timeoutSec = 10
	}
	autoRenewEnabled := cfg.GetPluginBool("qqmusic", "auto_renew_enabled")
	intervalSec := cfg.GetPluginInt("qqmusic", "auto_renew_interval_sec")
	var interval time.Duration
	if intervalSec > 0 {
		interval = time.Duration(intervalSec) * time.Second
	}
	persist := func(pairs map[string]string) error {
		return cfg.PersistPluginConfig("qqmusic", pairs)
	}
	client := NewClient(cookie, time.Duration(timeoutSec)*time.Second, logger, autoRenewEnabled, interval, persist)
	if err := client.SetAPIProxy(cfg.ResolveAPIProxyConfig("qqmusic")); err != nil {
		return nil, err
	}
	platform := NewPlatform(client)
	thirdPartyMode, err := thirdparty.ParseMode(cfg.GetPluginString("qqmusic", "third_party_mode"))
	if err != nil {
		return nil, fmt.Errorf("qqmusic: %w", err)
	}
	if thirdPartyMode != thirdparty.ModeDisabled {
		providerNames := thirdparty.ParseProviderNames(cfg.GetPluginString("qqmusic", "third_party_providers"))
		thirdPartyTimeoutSec := cfg.GetPluginInt("qqmusic", "third_party_timeout")
		if thirdPartyTimeoutSec <= 0 {
			thirdPartyTimeoutSec = 5
		}
		resolver, resolverErr := thirdparty.NewChain(providerNames, time.Duration(thirdPartyTimeoutSec)*time.Second, logger)
		if resolverErr != nil {
			return nil, fmt.Errorf("qqmusic: configure third-party audio: %w", resolverErr)
		}
		platform.ConfigureThirdPartyAudio(thirdPartyMode, resolver)
	}
	return &platformplugins.Contribution{Platform: platform}, nil
}
