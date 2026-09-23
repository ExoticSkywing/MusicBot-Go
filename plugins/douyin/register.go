package douyin

import (
	"fmt"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/config"
	"github.com/liuran001/MusicBot-Go/bot/httpproxy"
	logpkg "github.com/liuran001/MusicBot-Go/bot/logger"
	platformplugins "github.com/liuran001/MusicBot-Go/bot/platform/plugins"
)

func init() {
	if err := platformplugins.Register(platformName, buildContribution); err != nil {
		panic(err)
	}
}

func buildContribution(cfg *config.Config, _ *logpkg.Logger) (*platformplugins.Contribution, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config required")
	}
	timeout := time.Duration(cfg.GetPluginInt(platformName, "timeout")) * time.Second
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	httpClient, err := httpproxy.NewHTTPClient(cfg.ResolveAPIProxyConfig(platformName), timeout)
	if err != nil {
		return nil, err
	}
	return &platformplugins.Contribution{Platform: NewPlatform(NewClient(httpClient, timeout))}, nil
}
