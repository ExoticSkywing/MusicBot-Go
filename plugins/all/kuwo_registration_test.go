package all_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/liuran001/MusicBot-Go/bot/config"
	platformplugins "github.com/liuran001/MusicBot-Go/bot/platform/plugins"
	_ "github.com/liuran001/MusicBot-Go/plugins/all"
)

func TestKuwoRegistration(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.ini")
	if err := os.WriteFile(configPath, []byte("BOT_TOKEN = test-token\n"), 0o600); err != nil {
		t.Fatalf("write minimal config: %v", err)
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("load minimal config: %v", err)
	}

	factory, ok := platformplugins.Get("kuwo")
	if !ok || factory == nil {
		t.Fatal("kuwo factory is not registered")
	}

	contribution, err := factory(cfg, nil)
	if err != nil {
		t.Fatalf("build Kuwo contribution without credentials: %v", err)
	}
	if contribution == nil {
		t.Fatal("Kuwo contribution is nil")
	}
	if contribution.Platform == nil {
		t.Fatal("Kuwo contribution platform is nil")
	}
	if got := contribution.Platform.Name(); got != "kuwo" {
		t.Fatalf("platform name = %q, want %q", got, "kuwo")
	}

	capabilities := contribution.Platform.Capabilities()
	if !capabilities.Download || !capabilities.Search || !capabilities.Lyrics {
		t.Fatalf(
			"required capabilities = {Download:%t Search:%t Lyrics:%t}, want all true",
			capabilities.Download,
			capabilities.Search,
			capabilities.Lyrics,
		)
	}
	if capabilities.Recognition || !capabilities.HiRes {
		t.Fatalf(
			"capabilities = {Recognition:%t HiRes:%t}, want false/true",
			capabilities.Recognition,
			capabilities.HiRes,
		)
	}
}
