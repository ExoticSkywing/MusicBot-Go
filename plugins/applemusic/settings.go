package applemusic

import botpkg "github.com/liuran001/MusicBot-Go/bot"

const (
	PreferAtmosKey = "prefer_atmos"
	PreferAtmosOn  = "on"
	PreferAtmosOff = "off"
)

// PreferAtmosDefinition controls whether an implicit Apple Music quality
// request should try a Dolby Atmos rendition before the configured default.
// Explicit quality requests always take precedence.
func PreferAtmosDefinition() botpkg.PluginSettingDefinition {
	return botpkg.PluginSettingDefinition{
		Plugin:         "applemusic",
		Key:            PreferAtmosKey,
		Title:          "Apple Music 优先 Dolby Atmos",
		Description:    "未显式指定音质时，歌曲存在 Dolby Atmos 则优先发送，否则使用默认音质",
		TitleKey:       "set_pdef_applemusic_prefer_atmos_title",
		DescriptionKey: "set_pdef_applemusic_prefer_atmos_desc",
		DefaultUser:    PreferAtmosOff,
		DefaultGroup:   PreferAtmosOff,
		Order:          110,
		Options: []botpkg.PluginSettingOption{
			{Value: PreferAtmosOff, Label: "关", LabelKey: "set_state_off"},
			{Value: PreferAtmosOn, Label: "开", LabelKey: "set_state_on"},
		},
	}
}
