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

// Soda also accepts *.douyin.com hosts, so both plugins are loaded in
// registration order (sorted by name) to prove Douyin music links, and only
// those, land on the douyin platform.
func TestDouyinRegistrationAndRouting(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.ini")
	if err := os.WriteFile(configPath, []byte("BOT_TOKEN = test-token\n"), 0o600); err != nil {
		t.Fatalf("write minimal config: %v", err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("load minimal config: %v", err)
	}

	manager := platform.NewManager()
	for _, name := range []string{"douyin", "soda"} {
		factory, ok := platformplugins.Get(name)
		if !ok || factory == nil {
			t.Fatalf("%s factory is not registered", name)
		}
		contribution, err := factory(cfg, nil)
		if err != nil || contribution == nil || contribution.Platform == nil {
			t.Fatalf("build %s contribution: %v", name, err)
		}
		manager.Register(contribution.Platform)
	}

	caps := manager.Get("douyin").Capabilities()
	if !caps.Download || caps.Search || caps.Lyrics {
		t.Fatalf("douyin capabilities = %+v, want download only", caps)
	}

	tests := []struct {
		url          string
		wantPlatform string
		wantID       string
	}{
		{"https://www.douyin.com/music/7687226746303580947", "douyin", "7687226746303580947"},
		{"https://www.iesdouyin.com/share/music/7687226746303580947?from_ssr=1", "douyin", "7687226746303580947"},
		{"https://music.douyin.com/qishui/share/track?track_id=987654321", "soda", "987654321"},
	}
	for _, tt := range tests {
		gotPlatform, gotID, ok := manager.MatchURL(tt.url)
		if !ok || gotPlatform != tt.wantPlatform || gotID != tt.wantID {
			t.Fatalf("MatchURL(%q) = (%q,%q,%v), want (%q,%q,true)", tt.url, gotPlatform, gotID, ok, tt.wantPlatform, tt.wantID)
		}
	}
}
