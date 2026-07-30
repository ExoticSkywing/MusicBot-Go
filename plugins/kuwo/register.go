package kuwo

import (
	"fmt"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/config"
	logpkg "github.com/liuran001/MusicBot-Go/bot/logger"
	platformplugins "github.com/liuran001/MusicBot-Go/bot/platform/plugins"
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
	return &platformplugins.Contribution{Platform: NewPlatform(client)}, nil
}
