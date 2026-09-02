package kugou

import (
	"context"
	"fmt"
	"time"

	botpkg "github.com/liuran001/MusicBot-Go/bot"
	"github.com/liuran001/MusicBot-Go/bot/config"
	logpkg "github.com/liuran001/MusicBot-Go/bot/logger"
	platformplugins "github.com/liuran001/MusicBot-Go/bot/platform/plugins"
	"github.com/liuran001/MusicBot-Go/plugins/thirdparty"
)

func init() {
	if err := platformplugins.Register("kugou", buildContribution); err != nil {
		panic(err)
	}
}

func buildContribution(cfg *config.Config, logger *logpkg.Logger) (*platformplugins.Contribution, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config required")
	}
	persist := func(pairs map[string]string) error {
		return cfg.PersistPluginConfig("kugou", pairs)
	}
	client := NewClient("", logger)
	apiProxyCfg := loadKugouAPIProxyConfig(cfg)
	if err := client.SetAPIProxy(apiProxyCfg); err != nil {
		return nil, err
	}
	if err := client.SetSearchProxy(cfg.GetPluginString("kugou", "search_proxy")); err != nil {
		return nil, err
	}
	concept := loadConceptSessionFromConfig(cfg.GetPluginString, cfg.GetPluginBool, cfg.GetPluginInt)
	manager := NewConceptSessionManager(logger, persist, concept)
	manager.SetHTTPClient(client.apiHTTPClient)
	manager.SetBaseURL(cfg.GetPluginString("kugou", "concept_base_url"))
	manager.StartAutoRefreshDaemon(context.Background())
	client.AttachConcept(manager)
	platform := NewPlatform(client)
	thirdPartyMode, err := thirdparty.ParseMode(cfg.GetPluginString("kugou", "third_party_mode"))
	if err != nil {
		return nil, fmt.Errorf("kugou: %w", err)
	}
	if thirdPartyMode != thirdparty.ModeDisabled {
		providerNames := thirdparty.ParseProviderNames(cfg.GetPluginString("kugou", "third_party_providers"))
		thirdPartyTimeoutSec := cfg.GetPluginInt("kugou", "third_party_timeout")
		if thirdPartyTimeoutSec <= 0 {
			thirdPartyTimeoutSec = 5
		}
		resolver, resolverErr := thirdparty.NewChain(providerNames, time.Duration(thirdPartyTimeoutSec)*time.Second, logger)
		if resolverErr != nil {
			return nil, fmt.Errorf("kugou: configure third-party audio: %w", resolverErr)
		}
		platform.ConfigureThirdPartyAudio(thirdPartyMode, resolver)
	}
	contrib := &platformplugins.Contribution{
		Platform: platform,
		SettingDefinitions: []botpkg.PluginSettingDefinition{
			NoHiResWhenDefaultDefinition(),
		},
	}
	return contrib, nil
}
