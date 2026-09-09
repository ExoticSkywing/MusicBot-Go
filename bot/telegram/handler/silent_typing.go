package handler

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/mymmrac/telego"
)

type silentTypingKey struct{}

func isSilentGroupLink(ctx context.Context, message *telego.Message) bool {
	return message != nil && (message.Chat.Type == "group" || message.Chat.Type == "supergroup") &&
		!isForceNonSilent(ctx) && !isCommandMessage(message) && !strings.HasPrefix(strings.TrimSpace(message.Text), "/") &&
		!looksLikeCookiePayload(message.Text) && len(extractURLs(message.Text)) > 0
}

// Telegram expires typing automatically after at most five seconds. Stopping
// refreshes is the only Bot API way to clear it without sending a message.
func startSilentLinkTyping(ctx context.Context, b *telego.Bot, message *telego.Message) (context.Context, func()) {
	if b == nil || !isSilentGroupLink(ctx, message) {
		return ctx, func() {}
	}
	stopSilentLinkTyping(ctx)
	actionCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(4 * time.Second)
		defer ticker.Stop()
		for {
			if actionCtx.Err() != nil {
				return
			}
			requestCtx, requestCancel := context.WithTimeout(actionCtx, 3*time.Second)
			_ = b.SendChatAction(requestCtx, &telego.SendChatActionParams{ChatID: telego.ChatID{ID: message.Chat.ID}, MessageThreadID: message.MessageThreadID, Action: telego.ChatActionTyping})
			requestCancel()
			select {
			case <-actionCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	var once sync.Once
	stop := func() { once.Do(func() { cancel(); <-done }) }
	return context.WithValue(ctx, silentTypingKey{}, stop), stop
}

func stopSilentLinkTyping(ctx context.Context) {
	if stop, ok := ctx.Value(silentTypingKey{}).(func()); ok {
		stop()
	}
}
