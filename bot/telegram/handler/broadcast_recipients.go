package handler

import (
	"context"
	"time"

	botpkg "github.com/liuran001/MusicBot-Go/bot"
	"github.com/mymmrac/telego"
)

func BroadcastSettingDefinition() botpkg.PluginSettingDefinition {
	return botpkg.PluginSettingDefinition{
		Plugin: botpkg.BroadcastSettingPlugin, Key: botpkg.BroadcastSettingKey,
		Icon: "📣", Title: "接收更新通知", TitleKey: "broadcast_setting_title",
		Description: "接收管理员手动发送的更新公告", DescriptionKey: "broadcast_setting_desc",
		DefaultUser: "on", UserOnly: true, Order: 130,
		Options: []botpkg.PluginSettingOption{
			{Value: "on", Label: "开", LabelKey: "set_state_on"},
			{Value: "off", Label: "关", LabelKey: "set_state_off"},
		},
	}
}

// Called by middleware, independent of activity counting (which excludes admins
// and includes group/inline users). Failures never prevent normal bot usage.
func (r *Router) recordBroadcastRecipient(ctx context.Context, update *telego.Update) {
	if r.BroadcastRecipients == nil || update == nil {
		return
	}
	message := update.Message
	var user *telego.User
	if message != nil {
		user = message.From
	} else if q := update.CallbackQuery; q != nil && q.Message != nil {
		message, user = q.Message.Message(), &q.From
	}
	if message == nil || user == nil || user.IsBot || user.ID <= 0 ||
		message.Chat.Type != "private" || message.Chat.ID != user.ID ||
		message.SenderChat != nil || message.IsAutomaticForward ||
		!r.Whitelist.IsAllowed(message.Chat.ID, user.ID) {
		return
	}
	writeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := r.BroadcastRecipients.RecordBroadcastRecipient(writeCtx, user.ID); err != nil && r.Logger != nil {
		r.Logger.Warn("failed to record broadcast recipient", "error", err)
	}
}
