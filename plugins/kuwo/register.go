package kuwo

import (
	"fmt"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/config"
	logpkg "github.com/liuran001/MusicBot-Go/bot/logger"
	platformplugins "github.com/liuran001/MusicBot-Go/bot/platform/plugins"
	"github.com/liuran001/MusicBot-Go/plugins/thirdparty"
)

func init() {
	if err := platformplugins.Register("kuwo", buildContribution); err != nil {
		panic(err)
	}
}

func buildContribution(
	cfg *config.Config,
	logger *logpkg.Logger,
) (*platformplugins.Contribution, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config required")
	}
	timeoutSec := cfg.GetPluginInt("kuwo", "timeout")
	if timeoutSec <= 0 {
		timeoutSec = 20
	}
	client := NewClient(time.Duration(timeoutSec)*time.Second, logger)
	if err := client.SetAPIProxy(cfg.ResolveAPIProxyConfig("kuwo")); err != nil {
		return nil, err
	}
	downloadTimeout := time.Duration(cfg.GetInt("DownloadTimeout")) * time.Second
	if err := client.SetDownloadConfig(
		cfg.GetString("DownloadProxy"),
		downloadTimeout,
		cfg.GetInt("DownloadMaxRetries"),
	); err != nil {
		return nil, err
	}
	platform := NewPlatform(client)
	thirdPartyMode, err := thirdparty.ParseMode(cfg.GetPluginString("kuwo", "third_party_mode"))
	if err != nil {
		return nil, fmt.Errorf("kuwo: %w", err)
	}
	if thirdPartyMode != thirdparty.ModeDisabled {
		providerNames := thirdparty.ParseProviderNames(cfg.GetPluginString("kuwo", "third_party_providers"))
		thirdPartyTimeoutSec := cfg.GetPluginInt("kuwo", "third_party_timeout")
		if thirdPartyTimeoutSec <= 0 {
			thirdPartyTimeoutSec = 5
		}
		resolver, resolverErr := thirdparty.NewChain(providerNames, time.Duration(thirdPartyTimeoutSec)*time.Second, logger)
		if resolverErr != nil {
			return nil, fmt.Errorf("kuwo: configure third-party audio: %w", resolverErr)
		}
		platform.ConfigureThirdPartyAudio(thirdPartyMode, resolver)
	}
	return &platformplugins.Contribution{Platform: platform}, nil
}
