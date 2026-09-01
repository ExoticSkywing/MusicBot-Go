package handler

import (
	"context"
	"strings"

	botpkg "github.com/liuran001/MusicBot-Go/bot"
	"github.com/mymmrac/telego"
)

const (
	StandaloneCoverPlugin = "telegram"
	StandaloneCoverKey    = "show_standalone_cover"
	StandaloneCoverOn     = "on"
	StandaloneCoverOff    = "off"
)

// StandaloneCoverSettingDefinition controls the extra full-size cover photo
// sent immediately before a normal chat audio message. Inline results are not
// covered because Telegram allows only one media item per inline result.
func StandaloneCoverSettingDefinition() botpkg.PluginSettingDefinition {
	return botpkg.PluginSettingDefinition{
		Plugin:         StandaloneCoverPlugin,
		Key:            StandaloneCoverKey,
		Icon:           "🖼",
		Title:          "独立展示歌曲封面",
		TitleKey:       "set_pdef_standalone_cover_title",
		Description:    "发送歌曲前单独展示大图封面",
		DescriptionKey: "set_pdef_standalone_cover_desc",
		DefaultUser:    StandaloneCoverOn,
		DefaultGroup:   StandaloneCoverOn,
		Order:          119,
		Options: []botpkg.PluginSettingOption{
			{Value: StandaloneCoverOn, Label: "开", LabelKey: "set_state_on"},
			{Value: StandaloneCoverOff, Label: "关", LabelKey: "set_state_off"},
		},
	}
}

func resolveStandaloneCoverEnabled(ctx context.Context, repo botpkg.SongRepository, message *telego.Message) bool {
	if message == nil {
		return true
	}
	scopeType := botpkg.PluginScopeUser
	scopeID := int64(0)
	if message.Chat.Type != "private" {
		scopeType = botpkg.PluginScopeGroup
		scopeID = message.Chat.ID
	} else if message.From != nil {
		scopeID = message.From.ID
	}
	if repo == nil || scopeID == 0 {
		return true
	}
	value, err := repo.GetPluginSetting(ctx, scopeType, scopeID, StandaloneCoverPlugin, StandaloneCoverKey)
	if err != nil || strings.TrimSpace(value) == "" {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(value), StandaloneCoverOn)
}
