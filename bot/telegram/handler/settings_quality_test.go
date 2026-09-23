package handler

import (
	"sort"
	"strings"
	"testing"

	botpkg "github.com/liuran001/MusicBot-Go/bot"
	"github.com/liuran001/MusicBot-Go/bot/platform"
	"github.com/mymmrac/telego"
)

func findSettingsCallbackButton(keyboard *telego.InlineKeyboardMarkup, callbackData string) *telego.InlineKeyboardButton {
	if keyboard == nil {
		return nil
	}
	for _, row := range keyboard.InlineKeyboard {
		for index := range row {
			if row[index].CallbackData == callbackData {
				return &row[index]
			}
		}
	}
	return nil
}

func TestSettingsAtmosButtonRequiresAppleMusicCapability(t *testing.T) {
	manager := newStubManager()
	manager.Register(&stubPlatform{
		name:         "applemusic",
		capabilities: platform.Capabilities{Atmos: true},
	})
	manager.Register(newStubPlatform("netease"))
	handler := &SettingsHandler{PlatformManager: manager}

	appleSettings := &botpkg.UserSettings{UserID: 1, DefaultPlatform: "applemusic", DefaultQuality: "atmos"}
	keyboard := handler.buildSettingsKeyboard(zhCtx(), "private", appleSettings, nil, manager.List())
	button := findSettingsCallbackButton(keyboard, "settings quality atmos")
	if button == nil {
		t.Fatal("Atmos-capable Apple Music should expose the Atmos settings button")
	}
	if !strings.Contains(button.Text, "Dolby Atmos") || !strings.Contains(button.Text, "✅") {
		t.Fatalf("selected Atmos button text = %q", button.Text)
	}

	neteaseSettings := &botpkg.UserSettings{UserID: 1, DefaultPlatform: "netease", DefaultQuality: "hires"}
	keyboard = handler.buildSettingsKeyboard(zhCtx(), "private", neteaseSettings, nil, manager.List())
	if button := findSettingsCallbackButton(keyboard, "settings quality atmos"); button != nil {
		t.Fatalf("non-Apple platform unexpectedly exposed Atmos button: %+v", button)
	}

	managerWithoutAtmos := newStubManager()
	managerWithoutAtmos.Register(newStubPlatform("applemusic"))
	handler.PlatformManager = managerWithoutAtmos
	keyboard = handler.buildSettingsKeyboard(zhCtx(), "private", appleSettings, nil, managerWithoutAtmos.List())
	if button := findSettingsCallbackButton(keyboard, "settings quality atmos"); button != nil {
		t.Fatalf("Apple Music without Atmos capability unexpectedly exposed button: %+v", button)
	}
}

func TestQualitySelectableForPlatform(t *testing.T) {
	manager := newStubManager()
	manager.Register(&stubPlatform{name: "applemusic", capabilities: platform.Capabilities{Atmos: true}})
	manager.Register(newStubPlatform("netease"))

	if !qualitySelectableForPlatform(manager, "applemusic", "atmos") {
		t.Fatal("Atmos should be selectable for capable Apple Music")
	}
	if qualitySelectableForPlatform(manager, "netease", "atmos") {
		t.Fatal("Atmos should not be selectable for non-Apple providers")
	}
	if !qualitySelectableForPlatform(manager, "netease", "hires") {
		t.Fatal("existing stereo quality tiers must remain selectable")
	}
}

type linkOnlyStubPlatform struct {
	*stubPlatform
}

func (p *linkOnlyStubPlatform) SupportsSearch() bool { return false }

func TestDefaultPlatformChoicesSkipLinkOnlyPlatforms(t *testing.T) {
	manager := newStubManager()
	manager.Register(newStubPlatform("netease"))
	manager.Register(&linkOnlyStubPlatform{stubPlatform: newStubPlatform("douyin")})
	manager.Register(newStubPlatform("soda"))

	choices := defaultPlatformChoices(manager)
	sorted := append([]string(nil), choices...)
	sort.Strings(sorted) // the stub manager lists platforms in map order
	if strings.Join(sorted, ",") != "netease,soda" {
		t.Fatalf("default platform choices = %v, want [netease soda]", choices)
	}

	handler := &SettingsHandler{PlatformManager: manager}
	settings := &botpkg.UserSettings{UserID: 1, DefaultPlatform: "netease", DefaultQuality: "hires"}
	keyboard := handler.buildSettingsKeyboard(zhCtx(), "private", settings, nil, choices)
	if button := findSettingsCallbackButton(keyboard, "settings platform douyin"); button != nil {
		t.Fatalf("link-only platform offered as default: %+v", button)
	}
}
