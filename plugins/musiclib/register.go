package musiclib

import (
	"fmt"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/config"
	"github.com/liuran001/MusicBot-Go/bot/httpproxy"
	logpkg "github.com/liuran001/MusicBot-Go/bot/logger"
	platformplugins "github.com/liuran001/MusicBot-Go/bot/platform/plugins"
)

func init() {
	for _, name := range []string{"migu", "qianqian", "fivesing", "jamendo", "joox"} {
		if err := platformplugins.Register(name, factory(name)); err != nil {
			panic(err)
		}
	}
}

func factory(name string) platformplugins.Factory {
	return func(cfg *config.Config, _ *logpkg.Logger) (*platformplugins.Contribution, error) {
		if cfg == nil {
			return nil, fmt.Errorf("config required")
		}
		timeout := time.Duration(cfg.GetPluginInt(name, "timeout")) * time.Second
		if timeout <= 0 {
			timeout = 20 * time.Second
		}
		client, err := httpproxy.NewHTTPClient(cfg.ResolveAPIProxyConfig(name), timeout)
		if err != nil {
			return nil, err
		}
		return &platformplugins.Contribution{Platform: NewPlatform(name, cfg.GetPluginString(name, "cookie"), client, timeout)}, nil
	}
}
