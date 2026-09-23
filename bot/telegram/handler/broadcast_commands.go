package handler

import (
	"context"
	"crypto/rand"
	"errors"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	botpkg "github.com/liuran001/MusicBot-Go/bot"
	"github.com/liuran001/MusicBot-Go/bot/admincmd"
	"github.com/liuran001/MusicBot-Go/bot/i18n"
	"github.com/liuran001/MusicBot-Go/bot/telegram"
	"github.com/mymmrac/telego"
	"github.com/mymmrac/telego/telegoapi"
)

const broadcastDraftTTL = 10 * time.Minute

type broadcastDraft struct {
	token      string
	owner      int64
	messageID  int
	text       string
	recipients []int64
	expires    time.Time
}

type broadcastRun struct {
	draft  *broadcastDraft
	cancel context.CancelFunc
	done   chan struct{}
}

// BroadcastCommands owns only manual, ephemeral drafts/jobs. No startup hook,
// scheduler or retry-on-restart exists. All state transitions are serialized so
// repeated confirmation callbacks cannot launch duplicate broadcasts.
type BroadcastCommands struct {
	repo      botpkg.BroadcastRepository
	admins    *AdminSet
	whitelist *Whitelist
	limiter   *telegram.RateLimiter
	logger    botpkg.Logger
	root      context.Context
	stop      context.CancelFunc
	mu        sync.Mutex
	drafts    map[int64]*broadcastDraft // At most one draft per administrator.
	active    *broadcastRun             // One broadcast for the whole bot.
	closed    bool
	now       func() time.Time
	interval  time.Duration
}

func NewBroadcastCommands(ctx context.Context, repo botpkg.BroadcastRepository, admins *AdminSet, whitelist *Whitelist, limiter *telegram.RateLimiter, logger botpkg.Logger) *BroadcastCommands {
	ctx, stop := context.WithCancel(ctx)
	return &BroadcastCommands{
		repo: repo, admins: admins, whitelist: whitelist, limiter: limiter, logger: logger,
		root: ctx, stop: stop, drafts: make(map[int64]*broadcastDraft), now: time.Now, interval: 200 * time.Millisecond,
	}
}

func (h *BroadcastCommands) Command() admincmd.Command {
	return admincmd.Command{
		Name: "broadcast", RichHandler: h.preview,
		CallbackPrefix: "admin broadcast ", CallbackHandler: h.callback,
	}
}

func (h *BroadcastCommands) preview(ctx context.Context, args string) (*admincmd.Response, error) {
	owner, ok := admincmd.ChatIDFromContext(ctx)
	response := func(key string) (*admincmd.Response, error) { return &admincmd.Response{Text: tr(ctx, key)}, nil }
	if !ok || owner <= 0 || !isBotAdmin(h.admins, owner) {
		return response("broadcast_private_only")
	}
	text := strings.TrimSpace(args)
	if text == "cancel" {
		h.mu.Lock()
		defer h.mu.Unlock()
		delete(h.drafts, owner)
		if h.active != nil && h.active.draft.owner == owner {
			h.active.cancel()
			return response("broadcast_stopping")
		}
		return response("broadcast_cancelled")
	}
	if text == "" {
		return response("broadcast_usage")
	}
	// AdminCommandHandler sanitizes previews too: send exactly the content the
	// administrator reviewed. Use literal text, never parse announcement markup.
	text = sanitizeSensitiveText(text)
	if len(utf16.Encode([]rune(text))) > 3000 {
		return response("broadcast_too_long")
	}
	if h.repo == nil {
		return response("broadcast_unavailable")
	}
	readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ids, err := h.repo.ListBroadcastRecipients(readCtx)
	if err != nil {
		return response("broadcast_unavailable")
	}
	recipients := make([]int64, 0, len(ids))
	for _, id := range ids {
		if h.allowedRecipient(id) {
			recipients = append(recipients, id)
		}
	}
	if len(recipients) == 0 {
		return response("broadcast_empty")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || h.root.Err() != nil {
		return response("broadcast_unavailable")
	}
	if h.active != nil {
		return response("broadcast_busy")
	}
	for id, draft := range h.drafts {
		if !h.now().Before(draft.expires) || !isBotAdmin(h.admins, id) {
			delete(h.drafts, id)
		}
	}
	draft := &broadcastDraft{token: rand.Text(), owner: owner, text: text, recipients: recipients, expires: h.now().Add(broadcastDraftTTL)}
	h.drafts[owner] = draft
	return &admincmd.Response{
		Text:        tr(ctx, "broadcast_preview", map[string]any{"Count": len(recipients), "Text": text}),
		ReplyMarkup: broadcastKeyboard(ctx, draft.token, true),
		AfterSend: func(_ context.Context, _ *telego.Bot, sent *telego.Message) {
			h.mu.Lock()
			defer h.mu.Unlock()
			if h.drafts[owner] == draft && sent != nil && sent.Chat.ID == owner {
				draft.messageID = sent.MessageID
			}
		},
	}, nil
}

func broadcastKeyboard(ctx context.Context, token string, preview bool) *telego.InlineKeyboardMarkup {
	var row []telego.InlineKeyboardButton
	if preview {
		row = append(row, telego.InlineKeyboardButton{Text: tr(ctx, "broadcast_confirm"), CallbackData: "admin broadcast send " + token})
	}
	key := "broadcast_stop"
	if preview {
		key = "broadcast_cancel"
	}
	row = append(row, telego.InlineKeyboardButton{Text: tr(ctx, key), CallbackData: "admin broadcast cancel " + token})
	return &telego.InlineKeyboardMarkup{InlineKeyboard: [][]telego.InlineKeyboardButton{row}}
}

func (h *BroadcastCommands) callback(ctx context.Context, b *telego.Bot, query *telego.CallbackQuery) error {
	if query == nil || b == nil {
		return nil
	}
	answer := func(key string) {
		text := ""
		if key != "" {
			text = tr(ctx, key)
		}
		_ = b.AnswerCallbackQuery(ctx, &telego.AnswerCallbackQueryParams{CallbackQueryID: query.ID, Text: text, ShowAlert: text != ""})
	}
	if !isBotAdmin(h.admins, query.From.ID) || query.Message == nil {
		answer("broadcast_private_only")
		return nil
	}
	msg := query.Message.Message()
	if msg == nil || msg.Chat.Type != "private" || msg.Chat.ID != query.From.ID {
		answer("broadcast_private_only")
		return nil
	}
	parts := strings.Fields(query.Data)
	if len(parts) != 4 || parts[0] != "admin" || parts[1] != "broadcast" || (parts[2] != "send" && parts[2] != "cancel") {
		answer("broadcast_expired")
		return nil
	}
	h.mu.Lock()
	if run := h.active; run != nil && parts[2] == "cancel" && run.draft.owner == query.From.ID &&
		run.draft.token == parts[3] && run.draft.messageID == msg.MessageID {
		run.cancel()
		h.mu.Unlock()
		answer("broadcast_stopping")
		return nil
	}
	draft := h.drafts[query.From.ID]
	if h.closed || h.root.Err() != nil || draft == nil || draft.token != parts[3] ||
		draft.messageID == 0 || draft.messageID != msg.MessageID || !h.now().Before(draft.expires) {
		h.mu.Unlock()
		answer("broadcast_expired")
		return nil
	}
	if parts[2] == "cancel" {
		delete(h.drafts, query.From.ID)
		h.mu.Unlock()
		answer("")
		h.edit(ctx, b, draft, tr(ctx, "broadcast_cancelled"), nil)
		return nil
	}
	if h.active != nil {
		h.mu.Unlock()
		answer("broadcast_busy")
		return nil
	}
	delete(h.drafts, query.From.ID)
	runCtx, cancel := context.WithCancel(h.root)
	runCtx = i18n.WithLocalizer(runCtx, i18n.From(ctx))
	run := &broadcastRun{draft: draft, cancel: cancel, done: make(chan struct{})}
	h.active = run
	// Start under the lock, so Shutdown always sees a worker that will exit.
	go h.run(runCtx, b, run)
	h.mu.Unlock()
	answer("")
	return nil
}

func (h *BroadcastCommands) allowedRecipient(id int64) bool {
	return id > 0 && !isBotAdmin(h.admins, id) && h.whitelist.IsAllowed(id, id)
}

type broadcastResult struct {
	sent, failed, skipped int
	stopped               bool
}

// Deliberately sequential and slower than Telegram's global limit. Recheck
// subscriptions and permissions at delivery time, not only when previewing.
func (h *BroadcastCommands) deliver(ctx context.Context, draft *broadcastDraft, send func(context.Context, int64, string) error) broadcastResult {
	var result broadcastResult
	for _, id := range draft.recipients {
		if err := waitBroadcast(ctx, h.interval); err != nil {
			result.stopped = true
			break
		}
		if ctx.Err() != nil || !isBotAdmin(h.admins, draft.owner) {
			result.stopped = true
			break
		}
		if !h.allowedRecipient(id) {
			result.skipped++
			continue
		}
		checkCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		allowed, err := h.repo.CanBroadcastTo(checkCtx, id)
		cancel()
		if err != nil { // Fail closed if opt-out cannot be checked.
			result.stopped = true
			break
		}
		if !allowed {
			result.skipped++
			continue
		}
		sendCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		err = send(sendCtx, id, draft.text)
		cancel()
		if err == nil {
			result.sent++
			continue
		}
		if broadcastUnreachable(err) {
			result.skipped++
			writeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			blockErr := h.repo.BlockBroadcastRecipient(writeCtx, id)
			cancel()
			if blockErr != nil && h.logger != nil {
				h.logger.Warn("failed to mark unreachable broadcast recipient", "error", blockErr)
			}
		} else {
			// Do not retry ambiguous network failures: Telegram may have already
			// delivered the message. Only explicit flood-control responses retry.
			result.failed++
		}
		var apiErr *telegoapi.Error
		var retryErr *telegram.APIError
		if ctx.Err() != nil || (errors.As(err, &apiErr) && (apiErr.ErrorCode == 429 || apiErr.ErrorCode == 401)) ||
			(errors.As(err, &retryErr) && retryErr.Code == 429) {
			result.stopped = true
			break
		}
	}
	return result
}

func broadcastUnreachable(err error) bool {
	var apiErr *telegoapi.Error
	if !errors.As(err, &apiErr) {
		return false
	}
	if apiErr.ErrorCode == 403 {
		return true
	}
	return apiErr.ErrorCode == 400 && (strings.Contains(strings.ToLower(apiErr.Description), "chat not found") ||
		strings.Contains(strings.ToLower(apiErr.Description), "user is deactivated"))
}

func waitBroadcast(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ctx.Err()
	}
}

func (h *BroadcastCommands) run(ctx context.Context, b *telego.Bot, run *broadcastRun) {
	defer func() {
		run.cancel()
		h.mu.Lock()
		h.active = nil
		close(run.done)
		h.mu.Unlock()
	}()
	draft := run.draft
	h.edit(ctx, b, draft, tr(ctx, "broadcast_sending", map[string]any{"Count": len(draft.recipients)}), broadcastKeyboard(ctx, draft.token, false))
	result := h.deliver(ctx, draft, func(sendCtx context.Context, id int64, text string) error {
		return telegram.WithRetry(sendCtx, h.limiter, id, func() error {
			_, err := b.SendMessage(sendCtx, &telego.SendMessageParams{
				ChatID: telego.ChatID{ID: id}, Text: text,
				LinkPreviewOptions: &telego.LinkPreviewOptions{IsDisabled: true},
			})
			return err
		})
	})
	key := "broadcast_finished"
	if result.stopped {
		key = "broadcast_stopped"
	}
	text := tr(ctx, key, map[string]any{
		"Sent": result.sent, "Failed": result.failed, "Skipped": result.skipped,
		"Remaining": len(draft.recipients) - result.sent - result.failed - result.skipped,
	})
	if h.logger != nil {
		h.logger.Info("manual broadcast finished", "sent", result.sent, "failed", result.failed, "skipped", result.skipped, "stopped", result.stopped)
	}
	// User cancellation cancels deliveries, not the final result notification.
	// App shutdown cancels root too, so shutdown never waits on Telegram here.
	if h.root.Err() == nil {
		h.edit(h.root, b, draft, text, nil)
	}
}

func (h *BroadcastCommands) edit(ctx context.Context, b *telego.Bot, draft *broadcastDraft, text string, keyboard *telego.InlineKeyboardMarkup) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if keyboard == nil {
		keyboard = &telego.InlineKeyboardMarkup{InlineKeyboard: [][]telego.InlineKeyboardButton{}}
	}
	err := telegram.WithRetry(ctx, h.limiter, draft.owner, func() error {
		_, err := b.EditMessageText(ctx, &telego.EditMessageTextParams{
			ChatID: telego.ChatID{ID: draft.owner}, MessageID: draft.messageID, Text: text, ReplyMarkup: keyboard,
		})
		return err
	})
	if err != nil && h.logger != nil {
		h.logger.Warn("failed to update broadcast status", "error", err)
	}
}

func (h *BroadcastCommands) Shutdown(ctx context.Context) error {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	h.closed = true
	h.stop()
	clear(h.drafts)
	run := h.active
	if run != nil {
		run.cancel()
	}
	h.mu.Unlock()
	if run == nil {
		return nil
	}
	select {
	case <-run.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
