package handler

import (
	"context"

	"github.com/liuran001/MusicBot-Go/bot/telegram"
	"github.com/mymmrac/telego"
)

// CancelHandler backs /cancel: it stops every download and upload the calling
// user currently has in flight and frees the scratch files those tasks were
// holding.
//
// Scope is deliberately per-user, keyed on the sender's Telegram ID, so running
// it in a group cannot abort someone else's download. Freeing the cache happens
// through each task's own cleanup path as it unwinds — the shared cache
// directory is never swept wholesale, since other users' in-flight files live
// there too.
type CancelHandler struct {
	Music       *MusicHandler
	RateLimiter *telegram.RateLimiter
}

func (h *CancelHandler) Handle(ctx context.Context, b *telego.Bot, update *telego.Update) {
	if h == nil || update == nil || update.Message == nil {
		return
	}
	message := update.Message
	if message.From == nil {
		return
	}
	if h.Music == nil {
		h.reply(ctx, b, message, tr(ctx, "cancel_unavailable"))
		return
	}

	downloads, uploads := h.Music.CancelUserJobs(message.From.ID)
	if downloads == 0 && uploads == 0 {
		h.reply(ctx, b, message, tr(ctx, "cancel_nothing"))
		return
	}
	h.reply(ctx, b, message, tr(ctx, "cancel_done", map[string]any{
		"Downloads": downloads,
		"Uploads":   uploads,
	}))
}

func (h *CancelHandler) reply(ctx context.Context, b *telego.Bot, message *telego.Message, text string) {
	params := &telego.SendMessageParams{
		ChatID:          telego.ChatID{ID: message.Chat.ID},
		Text:            text,
		ReplyParameters: &telego.ReplyParameters{MessageID: message.MessageID},
	}
	if h.RateLimiter != nil {
		_, _ = telegram.SendMessageWithRetry(ctx, h.RateLimiter, b, params)
		return
	}
	_, _ = b.SendMessage(ctx, params)
}
