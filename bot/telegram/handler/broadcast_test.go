package handler

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"

	botpkg "github.com/liuran001/MusicBot-Go/bot"
	"github.com/liuran001/MusicBot-Go/bot/admincmd"
	"github.com/liuran001/MusicBot-Go/bot/i18n"
	"github.com/liuran001/MusicBot-Go/bot/telegram"
	"github.com/mymmrac/telego"
	"github.com/mymmrac/telego/telegoapi"
	th "github.com/mymmrac/telego/telegohandler"
)

type broadcastTestRepo struct {
	mu      sync.Mutex
	users   map[int64]bool
	writes  []int64
	blocked []int64
	err     error
}

func (r *broadcastTestRepo) RecordBroadcastRecipient(_ context.Context, id int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.writes = append(r.writes, id)
	if r.users == nil {
		r.users = make(map[int64]bool)
	}
	r.users[id] = true
	return r.err
}

func (r *broadcastTestRepo) ListBroadcastRecipients(context.Context) ([]int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var ids []int64
	for id, enabled := range r.users {
		if enabled {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids, r.err
}

func (r *broadcastTestRepo) CanBroadcastTo(_ context.Context, id int64) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.users[id], r.err
}

func (r *broadcastTestRepo) BlockBroadcastRecipient(_ context.Context, id int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.users[id] = false
	r.blocked = append(r.blocked, id)
	return r.err
}

func newBroadcastTestHandler(t *testing.T) (*BroadcastCommands, *broadcastTestRepo) {
	t.Helper()
	repo := &broadcastTestRepo{users: map[int64]bool{42: true, 101: true, 102: true}}
	h := NewBroadcastCommands(context.Background(), repo, NewAdminSet(map[int64]struct{}{42: {}}), nil, nil, nil)
	h.interval = 0
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := h.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	return h, repo
}

func previewBroadcast(t *testing.T, h *BroadcastCommands, text string) (*admincmd.Response, *telego.CallbackQuery) {
	t.Helper()
	resp, err := h.preview(admincmd.WithChatID(zhCtx(), 42), text)
	if err != nil || resp.ReplyMarkup == nil {
		t.Fatalf("preview: %+v %v", resp, err)
	}
	msg := &telego.Message{MessageID: 10, Chat: telego.Chat{ID: 42, Type: "private"}}
	resp.AfterSend(zhCtx(), nil, msg)
	return resp, &telego.CallbackQuery{ID: "cb", From: telego.User{ID: 42}, Message: msg, Data: resp.ReplyMarkup.InlineKeyboard[0][0].CallbackData}
}

func waitBroadcastTestFinished(t *testing.T, h *BroadcastCommands) {
	t.Helper()
	h.mu.Lock()
	run := h.active
	h.mu.Unlock()
	if run != nil {
		select {
		case <-run.done:
		case <-time.After(3 * time.Second):
			t.Fatal("broadcast worker did not finish")
		}
	}
}

func TestBroadcastPreviewAndRepeatedConfirmation(t *testing.T) {
	h, _ := newBroadcastTestHandler(t)
	b, calls := newActivityTestBot(t)
	text := "更新公告\n已修复 YouTube。<b>普通文本</b>"
	resp, query := previewBroadcast(t, h, text)
	if !strings.Contains(resp.Text, "尚未发送") || !strings.Contains(resp.Text, "2 人") || len(calls.payloads("sendMessage")) != 0 {
		t.Fatalf("preview not safe: %s", resp.Text)
	}
	if len(query.Data) > 64 {
		t.Fatal("callback exceeds Telegram limit")
	}
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() { defer wg.Done(); _ = h.callback(zhCtx(), b, query) }()
	}
	wg.Wait()
	waitBroadcastTestFinished(t, h)
	var recipients []int64
	for _, payload := range calls.payloads("sendMessage") {
		recipients = append(recipients, int64(payload["chat_id"].(float64)))
		if payload["text"] != text || payload["parse_mode"] != nil || payload["reply_markup"] != nil {
			t.Fatalf("announcement changed or contains admin controls: %#v", payload)
		}
	}
	if !reflect.DeepEqual(recipients, []int64{101, 102}) {
		t.Fatalf("duplicate/wrong recipients: %v", recipients)
	}
	if !strings.Contains(strings.Join(telegramRecordedText(calls), "\n"), "成功：2 人") {
		t.Fatal("missing completion summary")
	}
}

func TestBroadcastRejectsUnsafeCallbacks(t *testing.T) {
	for _, mode := range []string{"nonadmin", "other admin", "group", "wrong chat", "wrong message", "wrong token", "expired", "revoked", "not sent", "replaced", "cancelled", "restart"} {
		t.Run(mode, func(t *testing.T) {
			h, _ := newBroadcastTestHandler(t)
			b, calls := newActivityTestBot(t)
			_, query := previewBroadcast(t, h, "announcement")
			switch mode {
			case "nonadmin":
				query.From.ID = 99
			case "other admin":
				h.admins.Replace(map[int64]struct{}{42: {}, 99: {}})
				query.From.ID = 99
				query.Message = &telego.Message{MessageID: 10, Chat: telego.Chat{ID: 99, Type: "private"}}
			case "group":
				query.Message.Message().Chat = telego.Chat{ID: -100, Type: "group"}
			case "wrong chat":
				query.Message.Message().Chat.ID = 99
			case "wrong message":
				query.Message.Message().MessageID++
			case "wrong token":
				query.Data += "bad"
			case "expired":
				h.now = func() time.Time { return time.Now().Add(broadcastDraftTTL) }
			case "revoked":
				h.admins.Replace(nil)
			case "not sent":
				h.drafts[42].messageID = 0
			case "replaced":
				_, _ = h.preview(admincmd.WithChatID(zhCtx(), 42), "replacement")
			case "cancelled":
				_, _ = h.preview(admincmd.WithChatID(zhCtx(), 42), "cancel")
			case "restart":
				h, _ = newBroadcastTestHandler(t)
			}
			_ = h.callback(zhCtx(), b, query)
			waitBroadcastTestFinished(t, h)
			if len(calls.payloads("sendMessage")) != 0 {
				t.Fatal("unauthorized/stale preview sent an announcement")
			}
		})
	}
}

func TestBroadcastCommandAuthorizationLimitsAndLocales(t *testing.T) {
	h, repo := newBroadcastTestHandler(t)
	for _, id := range []int64{-100, 99, 0} {
		resp, _ := h.preview(admincmd.WithChatID(zhCtx(), id), "announcement")
		if resp.ReplyMarkup != nil || !strings.Contains(resp.Text, "管理员") {
			t.Fatal("non-admin/private preview allowed")
		}
	}
	ctx := admincmd.WithChatID(zhCtx(), 42)
	resp, _ := h.preview(ctx, strings.Repeat("😀", 1501))
	if resp.ReplyMarkup != nil || !strings.Contains(resp.Text, "3000") {
		t.Fatal("UTF16 limit ignored")
	}
	for _, lang := range i18n.SupportedLanguages {
		ctx := admincmd.WithChatID(i18n.WithLocalizer(context.Background(), i18n.For(lang)), 42)
		resp, _ := h.preview(ctx, strings.Repeat("😀", 1500))
		if resp.ReplyMarkup == nil || len(utf16.Encode([]rune(resp.Text))) > 4096 {
			t.Fatalf("preview too long or unavailable in %s", lang)
		}
		if help := buildAdminHelp(ctx, []admincmd.Command{h.Command()}); strings.Contains(help, "help_admincmd") || !strings.Contains(help, "/broadcast") {
			t.Fatalf("help missing in %s", lang)
		}
	}
	repo.err = errors.New("secret database error")
	resp, _ = h.preview(ctx, "announcement")
	if resp.ReplyMarkup != nil || strings.Contains(resp.Text, "secret") {
		t.Fatal("failed preview leaked details or allowed sending")
	}
}

func TestBroadcastRecipientTrackingOnlyPrivateConversations(t *testing.T) {
	for _, mode := range []string{"private", "admin private", "private callback", "group", "inline", "guest", "bot", "wrong ID", "no user", "denied"} {
		t.Run(mode, func(t *testing.T) {
			repo := &broadcastTestRepo{}
			router := &Router{BroadcastRecipients: repo}
			u := activityTestUpdate()
			switch mode {
			case "admin private":
				router.Activity = NewUserActivityTracker(nil, NewAdminSet(map[int64]struct{}{42: {}}))
			case "private callback":
				u.CallbackQuery = &telego.CallbackQuery{ID: "cb", From: *u.Message.From, Message: u.Message}
				u.Message = nil
			case "group":
				u.Message.Chat = telego.Chat{ID: -100, Type: "supergroup"}
			case "inline":
				u.InlineQuery = &telego.InlineQuery{ID: "q", From: *u.Message.From}
				u.Message = nil
			case "guest":
				u.GuestMessage, u.Message = u.Message, nil
			case "bot":
				u.Message.From.IsBot = true
			case "wrong ID":
				u.Message.Chat.ID = 99
			case "no user":
				u.Message.From = nil
			case "denied":
				router.Whitelist = NewWhitelist(true, nil, nil, "")
			}
			router.recordBroadcastRecipient(context.Background(), u)
			want := mode == "private" || mode == "admin private" || mode == "private callback"
			if (len(repo.writes) > 0) != want {
				t.Fatalf("tracked = %v; want %v", repo.writes, want)
			}
		})
	}
}

func TestBroadcastDeliveryRechecksSubscriptionsAndFailures(t *testing.T) {
	h, repo := newBroadcastTestHandler(t)
	repo.users = map[int64]bool{101: true, 102: false, 103: true, 104: true}
	draft := &broadcastDraft{owner: 42, text: "announcement", recipients: []int64{101, 102, 103, 104}}
	var attempts []int64
	result := h.deliver(context.Background(), draft, func(_ context.Context, id int64, _ string) error {
		attempts = append(attempts, id)
		switch id {
		case 103:
			return &telegoapi.Error{ErrorCode: 403, Description: "Forbidden: bot was blocked by the user"}
		case 104:
			return errors.New("ambiguous connection error")
		}
		return nil
	})
	if result != (broadcastResult{sent: 1, skipped: 2, failed: 1}) || !reflect.DeepEqual(attempts, []int64{101, 103, 104}) || !reflect.DeepEqual(repo.blocked, []int64{103}) {
		t.Fatalf("unexpected delivery: %+v attempts=%v blocked=%v", result, attempts, repo.blocked)
	}
}

func TestBroadcastStopsOnCancellationRevocationAndFloodControl(t *testing.T) {
	for _, mode := range []string{"cancel", "revoke", "rate limit", "retry exhausted", "storage", "whitelist", "opt out"} {
		t.Run(mode, func(t *testing.T) {
			h, repo := newBroadcastTestHandler(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var attempts []int64
			draft := &broadcastDraft{owner: 42, recipients: []int64{101, 102}}
			result := h.deliver(ctx, draft, func(_ context.Context, id int64, _ string) error {
				attempts = append(attempts, id)
				switch mode {
				case "cancel":
					cancel()
				case "revoke":
					h.admins.Replace(nil)
				case "rate limit":
					return &telegoapi.Error{ErrorCode: 429, Description: "Too Many Requests"}
				case "retry exhausted":
					return &telegram.APIError{Code: 429, Message: "max retries exceeded"}
				case "storage":
					repo.err = errors.New("unavailable")
				case "whitelist":
					h.whitelist = NewWhitelist(true, nil, nil, "")
				case "opt out":
					repo.users[102] = false
				}
				return nil
			})
			if !reflect.DeepEqual(attempts, []int64{101}) {
				t.Fatalf("continued after change: %v", attempts)
			}
			if mode != "opt out" && mode != "whitelist" && !result.stopped {
				t.Fatalf("not reported as stopped: %+v", result)
			}
		})
	}
}

func TestBroadcastSettingsPrivateOnly(t *testing.T) {
	def := BroadcastSettingDefinition()
	h := &SettingsHandler{}
	if def.DefaultUser != "on" || !h.shouldShowPluginSetting(def, false, false) || h.shouldShowPluginSetting(def, true, true) {
		t.Fatal("notification setting is not private-only and on by default")
	}
}

func TestBroadcastSettingsToggleUsesExistingSettingsPersistence(t *testing.T) {
	b, _ := newActivityTestBot(t)
	repo := newStubRepo()
	if err := repo.UpdateUserSettings(context.Background(), &botpkg.UserSettings{UserID: 101}); err != nil {
		t.Fatal(err)
	}
	manager := newStubManager()
	settings := &SettingsHandler{Repo: repo, PlatformManager: manager, PluginSettingDefinitions: []botpkg.PluginSettingDefinition{BroadcastSettingDefinition()}}
	h := &SettingsCallbackHandler{Repo: repo, PlatformManager: manager, SettingsHandler: settings}
	query := &telego.CallbackQuery{
		ID: "settings", From: telego.User{ID: 101}, Data: "settings pcycle telegram update_notifications",
		Message: &telego.Message{MessageID: 10, Chat: telego.Chat{ID: 101, Type: "private"}},
	}
	for _, want := range []string{"off", "on"} {
		h.Handle(zhCtx(), b, &telego.Update{CallbackQuery: query})
		got, err := repo.GetPluginSetting(context.Background(), botpkg.PluginScopeUser, 101, botpkg.BroadcastSettingPlugin, botpkg.BroadcastSettingKey)
		if err != nil || got != want {
			t.Fatalf("notification preference: %q %v; want %q", got, err, want)
		}
	}
}

func TestBroadcastRouterWiresCommandsAndRecipientObservation(t *testing.T) {
	b, calls := newActivityTestBot(t)
	h, repo := newBroadcastTestHandler(t)
	admin := &AdminCommandHandler{AdminIDs: h.admins, Commands: []admincmd.Command{h.Command()}}
	router := &Router{Admin: admin, AdminCommands: []string{"broadcast"}, BroadcastRecipients: repo}
	updates := make(chan telego.Update, 3)
	bh, err := th.NewBotHandler(b, updates)
	if err != nil {
		t.Fatal(err)
	}
	router.Register(bh, "music_bot")
	for _, id := range []int64{42, 99} {
		updates <- telego.Update{Message: &telego.Message{
			MessageID: 1, Text: "/broadcast Test announcement", From: &telego.User{ID: id}, Chat: telego.Chat{ID: id, Type: "private"},
		}}
	}
	close(updates)
	if err := bh.Start(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := bh.StopWithContext(ctx); err != nil {
		t.Fatal(err)
	}
	messages := calls.payloads("sendMessage")
	if len(messages) != 1 || messages[0]["chat_id"] != float64(42) || !strings.Contains(messages[0]["text"].(string), "Test announcement") {
		t.Fatalf("router did not restrict previews to admin: %+v", messages)
	}
	repo.mu.Lock()
	defer repo.mu.Unlock()
	slices.Sort(repo.writes)
	if !reflect.DeepEqual(repo.writes, []int64{42, 99}) {
		t.Fatalf("middleware did not record private chats: %v", repo.writes)
	}
}

func TestBroadcastCancelAndShutdownInterruptWorker(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "shutdown"}[shutdown], func(t *testing.T) {
			h, _ := newBroadcastTestHandler(t)
			h.interval = time.Hour // Worker must be interruptible, not sleep blindly.
			b, calls := newActivityTestBot(t)
			_, query := previewBroadcast(t, h, "announcement")
			_ = h.callback(zhCtx(), b, query)
			if shutdown {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				if err := h.Shutdown(ctx); err != nil {
					t.Fatal(err)
				}
			} else {
				query.Data = strings.Replace(query.Data, " send ", " cancel ", 1)
				_ = h.callback(zhCtx(), b, query)
				waitBroadcastTestFinished(t, h)
			}
			if len(calls.payloads("sendMessage")) != 0 {
				t.Fatal("cancelled job sent messages")
			}
		})
	}
}
