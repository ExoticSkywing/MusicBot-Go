package handler

import (
	"context"
	"fmt"
	"strings"
	"time"

	botpkg "github.com/liuran001/MusicBot-Go/bot"
	"github.com/mymmrac/telego"
)

// UserActivityTracker observes dispatched requests, not successful downloads.
// Its bounded, in-memory deduplication cache is not an activity-history table.
type UserActivityTracker struct {
	Repo botpkg.UserActivityRepository
	seen *ttlStore[struct{}]
}

func NewUserActivityTracker(repo botpkg.UserActivityRepository) *UserActivityTracker {
	return &UserActivityTracker{Repo: repo, seen: newTTLStoreWithCap[struct{}](24*time.Hour, 16384)}
}

// Called only at router dispatch boundaries. Nested handlers (e.g. recognition
// followed by music retrieval) do not create additional activity counts.
func (r *Router) recordUserActivity(ctx context.Context, update *telego.Update) {
	if r.Activity == nil || r.Activity.Repo == nil || update == nil {
		return
	}
	user, chatID, key := activityIdentity(update)
	if user == nil || user.ID <= 0 || user.IsBot || key == "" {
		return
	}
	if r.isOwnInlineMessage(update.Message) || r.isOwnInlineMessage(update.GuestMessage) || !r.Whitelist.IsAllowed(chatID, user.ID) {
		return
	}
	at := time.Now()
	_, err := r.Activity.seen.Do(key, func() (struct{}, error) {
		writeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		err := r.Activity.Repo.RecordUserActivity(writeCtx, user.ID, user.Username, strings.TrimSpace(user.FirstName+" "+user.LastName), at)
		return struct{}{}, err
	})
	// Analytics failure must never block the actual music/command handler.
	if err != nil && r.Logger != nil {
		r.Logger.Warn("failed to record user activity", "error", err)
	}
}

func activityIdentity(update *telego.Update) (*telego.User, int64, string) {
	if update == nil {
		return nil, 0, ""
	}
	message := update.Message
	if message == nil {
		message = update.GuestMessage
	}
	if message != nil {
		if message.IsAutomaticForward || message.SenderChat != nil || message.MessageID <= 0 {
			return nil, 0, ""
		}
		return message.From, message.Chat.ID, fmt.Sprintf("message:%d:%d", message.Chat.ID, message.MessageID)
	}
	if query := update.CallbackQuery; query != nil && query.ID != "" {
		chatID := query.From.ID // Inline callbacks have no ordinary chat message.
		if query.Message != nil {
			if msg := query.Message.Message(); msg != nil {
				chatID = msg.Chat.ID
			}
		}
		return &query.From, chatID, "callback:" + query.ID
	}
	if query := update.InlineQuery; query != nil && query.ID != "" && strings.TrimSpace(query.Query) != "" {
		return &query.From, query.From.ID, "inline:" + query.ID
	}
	if chosen := update.ChosenInlineResult; chosen != nil && update.UpdateID > 0 {
		return &chosen.From, chosen.From.ID, fmt.Sprintf("chosen:%d", update.UpdateID)
	}
	return nil, 0, ""
}
