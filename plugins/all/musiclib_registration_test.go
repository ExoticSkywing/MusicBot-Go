package all_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/liuran001/MusicBot-Go/bot/config"
	"github.com/liuran001/MusicBot-Go/bot/platform"
	platformplugins "github.com/liuran001/MusicBot-Go/bot/platform/plugins"
	_ "github.com/liuran001/MusicBot-Go/plugins/all"
)

func TestMusiclibRegistrations(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.ini")
	if err := os.WriteFile(configPath, []byte("BOT_TOKEN = test-token\n"), 0o600); err != nil {
		t.Fatalf("write minimal config: %v", err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("load minimal config: %v", err)
	}

	tests := []struct {
		name          string
		supportsLyric bool
	}{
		{name: "migu", supportsLyric: true},
		{name: "qianqian", supportsLyric: true},
		{name: "fivesing", supportsLyric: true},
		{name: "jamendo", supportsLyric: false},
		{name: "joox", supportsLyric: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			factory, ok := platformplugins.Get(tt.name)
			if !ok || factory == nil {
				t.Fatalf("%s factory is not registered", tt.name)
			}
			contribution, err := factory(cfg, nil)
			if err != nil {
				t.Fatalf("build %s contribution without credentials: %v", tt.name, err)
			}
			if contribution == nil || contribution.Platform == nil {
				t.Fatalf("%s contribution has no platform", tt.name)
			}

			plat := contribution.Platform
			if got := plat.Name(); got != tt.name {
				t.Fatalf("platform name = %q, want %q", got, tt.name)
			}
			capabilities := plat.Capabilities()
			if !capabilities.Download || !capabilities.Search || capabilities.Lyrics != tt.supportsLyric {
				t.Fatalf("capabilities = %+v, want download/search true and lyrics %t", capabilities, tt.supportsLyric)
			}
			if capabilities.Recognition || capabilities.HiRes || capabilities.Atmos {
				t.Fatalf("unsupported capabilities unexpectedly enabled: %+v", capabilities)
			}

			metadataProvider, ok := plat.(platform.MetadataProvider)
			if !ok {
				t.Fatal("platform does not expose metadata")
			}
			meta := metadataProvider.Metadata()
			if meta.Name != tt.name || meta.DisplayName == "" || !containsAlias(meta.Aliases, tt.name) {
				t.Fatalf("metadata = %+v, want canonical name, display name, and canonical alias", meta)
			}
		})
	}
}

func containsAlias(aliases []string, want string) bool {
	for _, alias := range aliases {
		if alias == want {
			return true
		}
	}
	return false
}
