package musiclib

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/config"
	platformplugins "github.com/liuran001/MusicBot-Go/bot/platform/plugins"
)

func TestFactoriesKeepConfigurationSeparate(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.ini")
	contents := `BOT_TOKEN = test-token
[plugins.migu]
cookie = migu=value
timeout = 7
[plugins.joox]
cookie = joox=value
timeout = 9
api_proxy_enabled = true
api_proxy_type = http
api_proxy_host = 127.0.0.1
api_proxy_port = 8888
`
	if err := os.WriteFile(configPath, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"migu", "qianqian", "fivesing", "jamendo", "joox"} {
		factory, ok := platformplugins.Get(name)
		if !ok {
			t.Fatalf("missing factory %s", name)
		}
		if _, err := factory(nil, nil); err == nil {
			t.Fatal("nil config accepted")
		}
		contribution, err := factory(cfg, nil)
		if err != nil {
			t.Fatal(err)
		}
		p := contribution.Platform.(*Platform)
		defer p.Close()
		if p.name != name {
			t.Fatalf("factory %s created %s", name, p.name)
		}
		switch name {
		case "migu":
			if p.cookie != "migu=value" || p.timeout != 7*time.Second || p.client.Timeout != 7*time.Second {
				t.Fatal("migu config lost")
			}
		case "joox":
			if p.cookie != "joox=value" || p.timeout != 9*time.Second {
				t.Fatal("joox config lost")
			}
			transport, ok := p.client.Transport.(*http.Transport)
			if !ok || transport.Proxy == nil {
				t.Fatal("joox API proxy missing")
			}
			req, _ := http.NewRequest(http.MethodGet, "https://www.joox.com", nil)
			proxy, err := transport.Proxy(req)
			if err != nil || proxy.Host != "127.0.0.1:8888" {
				t.Fatalf("proxy = %v, %v", proxy, err)
			}
		default:
			if p.cookie != "" || p.timeout != 20*time.Second {
				t.Fatal("config leaked across platforms")
			}
		}
	}
}
