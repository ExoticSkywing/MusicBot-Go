package handler

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	botpkg "github.com/liuran001/MusicBot-Go/bot"
	"github.com/liuran001/MusicBot-Go/bot/admincmd"
	"github.com/liuran001/MusicBot-Go/bot/telegram"
	"github.com/mymmrac/telego"
)

const userActivityPageSize = 8

// AdminCommandHandler performs the command permission check; callbacks check
// again here so a retained keyboard cannot outlive revoked admin privileges.
func BuildUserActivityCommands(repo botpkg.UserActivityRepository, admins *AdminSet, limiter *telegram.RateLimiter) []admincmd.Command {
	h := &userActivityCommands{repo: repo, admins: admins, limiter: limiter, now: time.Now}
	return []admincmd.Command{
		{Name: "stats", RichHandler: h.stats},
		{Name: "users", RichHandler: h.users, CallbackPrefix: "admin users ", CallbackHandler: h.callback},
	}
}

type userActivityCommands struct {
	repo    botpkg.UserActivityRepository
	admins  *AdminSet
	limiter *telegram.RateLimiter
	now     func() time.Time
}

func activityPrivateContext(ctx context.Context) bool {
	chatID, ok := admincmd.ChatIDFromContext(ctx)
	// Telegram private chat IDs are positive; groups/channels are negative.
	return ok && chatID > 0
}

func activityText(ctx context.Context, key string) *admincmd.Response {
	return &admincmd.Response{Text: tr(ctx, key)}
}

func (h *userActivityCommands) stats(ctx context.Context, args string) (*admincmd.Response, error) {
	if !activityPrivateContext(ctx) {
		return activityText(ctx, "activity_private_only"), nil
	}
	if strings.TrimSpace(args) != "" {
		return activityText(ctx, "activity_stats_usage"), nil
	}
	if h.repo == nil {
		return activityText(ctx, "activity_unavailable"), nil
	}
	now := h.now()
	stats, err := h.repo.GetUserActivityStats(ctx, now, h.admins.IDs()...)
	if err != nil {
		return activityText(ctx, "activity_unavailable"), nil
	}
	return &admincmd.Response{Text: tr(ctx, "activity_stats", map[string]any{
		"Total": stats.TotalUsers, "Today": stats.ActiveToday, "Week": stats.Active7Days,
		"Timezone": activityTimezone(now),
	})}, nil
}

func (h *userActivityCommands) users(ctx context.Context, args string) (*admincmd.Response, error) {
	if !activityPrivateContext(ctx) {
		return activityText(ctx, "activity_private_only"), nil
	}
	page := 1
	if arg := strings.TrimSpace(args); arg != "" {
		value, err := strconv.Atoi(arg)
		if err != nil || value < 1 {
			return activityText(ctx, "activity_users_usage"), nil
		}
		page = value
	}
	return h.renderUsers(ctx, page), nil
}

func (h *userActivityCommands) renderUsers(ctx context.Context, page int) *admincmd.Response {
	if h.repo == nil {
		return activityText(ctx, "activity_unavailable")
	}
	result, err := h.repo.ListUserActivity(ctx, page, userActivityPageSize, h.admins.IDs()...)
	if err != nil {
		return activityText(ctx, "activity_unavailable")
	}
	if result.TotalUsers == 0 {
		return activityText(ctx, "activity_empty")
	}
	now := h.now()
	lines := []string{tr(ctx, "activity_users_header", map[string]any{"Total": result.TotalUsers, "Page": result.Page, "Pages": result.TotalPages, "Timezone": activityTimezone(now)})}
	for i, user := range result.Users {
		name := user.DisplayName
		if name == "" {
			name = tr(ctx, "activity_no_name")
		}
		// Bound display length even for emoji-heavy names so eight rows fit
		// Telegram's message limit in every supported language.
		if runes := []rune(name); len(runes) > 64 {
			name = string(runes[:64]) + "…"
		}
		username := tr(ctx, "activity_no_username")
		if user.Username != "" {
			username = "@" + user.Username
		}
		lines = append(lines, tr(ctx, "activity_user_row", map[string]any{
			"Index": (result.Page-1)*userActivityPageSize + i + 1, "Name": name, "Username": username, "ID": user.UserID,
			"First": user.FirstSeenAt.In(now.Location()).Format("2006-01-02 15:04:05"),
			"Last":  user.LastSeenAt.In(now.Location()).Format("2006-01-02 15:04:05"), "Count": user.RequestCount,
		}))
	}
	lines = append(lines, tr(ctx, "activity_note"))
	button := func(key string, target int) telego.InlineKeyboardButton {
		return telego.InlineKeyboardButton{Text: tr(ctx, key), CallbackData: fmt.Sprintf("admin users page %d", target)}
	}
	var buttons []telego.InlineKeyboardButton
	if result.Page > 1 {
		buttons = append(buttons, button("activity_prev", result.Page-1))
	}
	buttons = append(buttons, button("activity_refresh", result.Page))
	if result.Page < result.TotalPages {
		buttons = append(buttons, button("activity_next", result.Page+1))
	}
	return &admincmd.Response{Text: strings.Join(lines, "\n\n"), ReplyMarkup: &telego.InlineKeyboardMarkup{InlineKeyboard: [][]telego.InlineKeyboardButton{buttons}}}
}

func activityTimezone(now time.Time) string {
	return now.Location().String() + " (UTC" + now.Format("-07:00") + ")"
}

func (h *userActivityCommands) callback(ctx context.Context, b *telego.Bot, query *telego.CallbackQuery) error {
	if query == nil || b == nil {
		return nil
	}
	answer := func(text string) {
		_ = b.AnswerCallbackQuery(ctx, &telego.AnswerCallbackQueryParams{CallbackQueryID: query.ID, Text: text, ShowAlert: text != ""})
	}
	if !isBotAdmin(h.admins, query.From.ID) || query.Message == nil {
		answer(tr(ctx, "activity_private_only"))
		return nil
	}
	message := query.Message.Message()
	if message == nil || message.Chat.Type != "private" || message.Chat.ID != query.From.ID {
		answer(tr(ctx, "activity_private_only"))
		return nil
	}
	parts := strings.Fields(query.Data)
	if len(parts) != 4 || parts[0] != "admin" || parts[1] != "users" || parts[2] != "page" {
		answer(tr(ctx, "activity_users_usage"))
		return nil
	}
	page, err := strconv.Atoi(parts[3])
	if err != nil || page < 1 {
		answer(tr(ctx, "activity_users_usage"))
		return nil
	}
	answer("")
	response := h.renderUsers(ctx, page)
	params := &telego.EditMessageTextParams{ChatID: telego.ChatID{ID: message.Chat.ID}, MessageID: message.MessageID, Text: response.Text, ReplyMarkup: response.ReplyMarkup}
	if h.limiter != nil {
		_, err = telegram.EditMessageTextWithRetry(ctx, h.limiter, b, params)
	} else {
		_, err = b.EditMessageText(ctx, params)
	}
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "message is not modified") {
		return nil
	}
	return err
}
