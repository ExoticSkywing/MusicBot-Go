package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf16"

	botpkg "github.com/liuran001/MusicBot-Go/bot"
	"github.com/liuran001/MusicBot-Go/bot/admincmd"
	"github.com/liuran001/MusicBot-Go/bot/i18n"
	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
)

type activityTestRepo struct {
	writes atomic.Int64
	reads  atomic.Int64
	fail   atomic.Bool
	page   atomic.Int64
}

func (r *activityTestRepo) RecordUserActivity(context.Context, int64, string, string, time.Time) error {
	r.writes.Add(1)
	if r.fail.Load() {
		return errors.New("test storage failure")
	}
	return nil
}
func (r *activityTestRepo) GetUserActivityStats(context.Context, time.Time) (botpkg.UserActivityStats, error) {
	r.reads.Add(1)
	if r.fail.Load() {
		return botpkg.UserActivityStats{}, errors.New("private database failure")
	}
	return botpkg.UserActivityStats{TotalUsers: 9, ActiveToday: 2, Active7Days: 5}, nil
}
func (r *activityTestRepo) ListUserActivity(_ context.Context, page, _ int) (botpkg.UserActivityPage, error) {
	r.reads.Add(1)
	r.page.Store(int64(page))
	if r.fail.Load() {
		return botpkg.UserActivityPage{}, errors.New("private database failure")
	}
	return botpkg.UserActivityPage{
		TotalUsers: 9, Page: page, TotalPages: 2,
		Users: []botpkg.UserActivity{{UserID: 987654, Username: "test_user", DisplayName: "Test <User>", FirstSeenAt: time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC), LastSeenAt: time.Date(2026, 9, 13, 1, 0, 0, 0, time.UTC), RequestCount: 12}},
	}, nil
}

func activityTestUpdate() *telego.Update {
	return &telego.Update{UpdateID: 1, Message: &telego.Message{MessageID: 10, Text: "/start", From: &telego.User{ID: 42, Username: "tester"}, Chat: telego.Chat{ID: 42, Type: "private"}}}
}

func TestUserActivityTrackerDeduplicatesAndRetriesFailure(t *testing.T) {
	repo := &activityTestRepo{}
	router := &Router{Activity: NewUserActivityTracker(repo)}
	update := activityTestUpdate()
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() { defer wg.Done(); router.recordUserActivity(context.Background(), update) }()
	}
	wg.Wait()
	// The same message received as a guest update must not count twice.
	router.recordUserActivity(context.Background(), &telego.Update{GuestMessage: update.Message})
	if got := repo.writes.Load(); got != 1 {
		t.Fatalf("duplicate writes = %d", got)
	}
	repo.fail.Store(true)
	update.Message.MessageID++
	router.recordUserActivity(context.Background(), update)
	repo.fail.Store(false)
	router.recordUserActivity(context.Background(), update)
	router.recordUserActivity(context.Background(), update)
	if got := repo.writes.Load(); got != 3 {
		t.Fatalf("failed write was cached or successful write repeated: %d", got)
	}
}

func TestUserActivityTrackerIgnoresNonUsersAndDisallowedRequests(t *testing.T) {
	cases := []struct {
		name   string
		change func(*telego.Update)
	}{
		{"bot", func(u *telego.Update) { u.Message.From.IsBot = true }},
		{"no user", func(u *telego.Update) { u.Message.From = nil }},
		{"invalid ID", func(u *telego.Update) { u.Message.From.ID = 0 }},
		{"automatic forward", func(u *telego.Update) { u.Message.IsAutomaticForward = true }},
		{"sender chat", func(u *telego.Update) { u.Message.SenderChat = &telego.Chat{ID: -5} }},
		{"own inline card", func(u *telego.Update) { u.Message.ViaBot = &telego.User{Username: "music_bot"} }},
		{"edited message", func(u *telego.Update) { u.EditedMessage = u.Message; u.Message = nil }},
		{"empty inline query", func(u *telego.Update) {
			u.Message = nil
			u.InlineQuery = &telego.InlineQuery{ID: "q", From: telego.User{ID: 42}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &activityTestRepo{}
			router := &Router{Activity: NewUserActivityTracker(repo), BotName: "music_bot"}
			u := activityTestUpdate()
			tc.change(u)
			router.recordUserActivity(context.Background(), u)
			if repo.writes.Load() != 0 {
				t.Fatal("ignored request was counted")
			}
		})
	}
	repo := &activityTestRepo{}
	router := &Router{Activity: NewUserActivityTracker(repo), Whitelist: NewWhitelist(true, nil, nil, "")}
	query := &telego.CallbackQuery{ID: "callback", From: telego.User{ID: 42}, Message: activityTestUpdate().Message}
	for _, u := range []*telego.Update{activityTestUpdate(), {CallbackQuery: query}, {GuestMessage: activityTestUpdate().Message}} {
		router.recordUserActivity(context.Background(), u)
	}
	if repo.writes.Load() != 0 {
		t.Fatal("non-whitelisted requests were counted")
	}
}

func newActivityTestBot(t *testing.T) (*telego.Bot, *verificationTelegramRecorder) {
	t.Helper()
	recorder := &verificationTelegramRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		recorder.mu.Lock()
		recorder.calls = append(recorder.calls, verificationTelegramCall{method: method, payload: payload})
		recorder.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if method == "answerCallbackQuery" || method == "sendChatAction" {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": true})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 10, "date": 1, "chat": map[string]any{"id": 42, "type": "private"}, "text": payload["text"]}})
	}))
	t.Cleanup(server.Close)
	b, err := telego.NewBot("123456:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghi", telego.WithAPIServer(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	return b, recorder
}

func TestUserActivityCommandsOnlyDiscloseInAdminPrivateChat(t *testing.T) {
	for _, command := range []string{"/stats", "/users"} {
		for _, tc := range []struct {
			name           string
			userID, chatID int64
			chatType       string
			allowed        bool
		}{
			{"admin private", 42, 42, "private", true},
			{"nonadmin private", 99, 99, "private", false},
			{"admin group", 42, -100, "supergroup", false},
		} {
			t.Run(command+"/"+tc.name, func(t *testing.T) {
				b, calls := newActivityTestBot(t)
				repo := &activityTestRepo{}
				admins := NewAdminSet(map[int64]struct{}{42: {}})
				h := &AdminCommandHandler{AdminIDs: admins, Commands: BuildUserActivityCommands(repo, admins, nil)}
				h.Handle(zhCtx(), b, &telego.Update{Message: &telego.Message{MessageID: 1, Text: command, From: &telego.User{ID: tc.userID}, Chat: telego.Chat{ID: tc.chatID, Type: tc.chatType}}})
				if got := repo.reads.Load() > 0; got != tc.allowed {
					t.Fatalf("database read allowed = %v, want %v", got, tc.allowed)
				}
				if tc.allowed {
					texts := strings.Join(telegramRecordedText(calls), "\n")
					want := "累计用户：9 人"
					if command == "/users" {
						want = "987654"
					}
					if !strings.Contains(texts, want) {
						t.Fatalf("missing %q in %q", want, texts)
					}
				}
			})
		}
	}
}

func TestUserActivityCallbacksRecheckPermissionAndPagination(t *testing.T) {
	for _, tc := range []struct {
		name           string
		userID, chatID int64
		chatType, data string
		allowed        bool
	}{
		{"allowed page", 42, 42, "private", "admin users page 2", true},
		{"nonadmin", 99, 99, "private", "admin users page 2", false},
		{"group", 42, -100, "supergroup", "admin users page 2", false},
		{"wrong private chat", 42, 99, "private", "admin users page 2", false},
		{"malformed", 42, 42, "private", "admin users page -1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, calls := newActivityTestBot(t)
			repo := &activityTestRepo{}
			admins := NewAdminSet(map[int64]struct{}{42: {}})
			commands := BuildUserActivityCommands(repo, admins, nil)
			query := &telego.CallbackQuery{ID: "cb", From: telego.User{ID: tc.userID}, Data: tc.data, Message: &telego.Message{MessageID: 10, Chat: telego.Chat{ID: tc.chatID, Type: tc.chatType}}}
			if err := commands[1].CallbackHandler(zhCtx(), b, query); err != nil {
				t.Fatal(err)
			}
			if got := len(calls.payloads("editMessageText")) > 0; got != tc.allowed {
				t.Fatalf("edited private data = %v", got)
			}
			if got := repo.reads.Load() > 0; got != tc.allowed {
				t.Fatalf("queried private data = %v", got)
			}
			if tc.allowed {
				if repo.page.Load() != 2 {
					t.Fatal("page not forwarded")
				}
				admins.Replace(nil)
				if err := commands[1].CallbackHandler(zhCtx(), b, query); err != nil {
					t.Fatal(err)
				}
				if repo.reads.Load() != 1 {
					t.Fatal("revoked admin could still query")
				}
			}
		})
	}
}

func TestUserActivityCommandsTimezoneErrorsAndHelp(t *testing.T) {
	repo := &activityTestRepo{}
	h := &userActivityCommands{repo: repo, now: func() time.Time { return time.Date(2026, 9, 13, 12, 0, 0, 0, time.FixedZone("Asia/Shanghai", 8*3600)) }}
	ctx := admincmd.WithChatID(zhCtx(), 42)
	resp, _ := h.users(ctx, "")
	for _, want := range []string{"2026-09-13 08:00:00", "2026-09-13 09:00:00", "Asia/Shanghai (UTC+08:00)", "累计交互：12 次"} {
		if !strings.Contains(resp.Text, want) {
			t.Fatalf("missing %q in %q", want, resp.Text)
		}
	}
	if resp.ReplyMarkup == nil {
		t.Fatal("missing pagination")
	}
	for _, arg := range []string{"0", "-2", "x", "1 extra", "99999999999999999999999"} {
		resp, _ := h.users(ctx, arg)
		if !strings.Contains(resp.Text, "用法") {
			t.Fatalf("invalid page accepted: %q", arg)
		}
	}
	repo.fail.Store(true)
	resp, _ = h.stats(ctx, "")
	if !strings.Contains(resp.Text, "稍后重试") || strings.Contains(resp.Text, "database") {
		t.Fatalf("unsafe storage error: %s", resp.Text)
	}
	help := buildAdminHelp(zhCtx(), BuildUserActivityCommands(repo, nil, nil))
	if !strings.Contains(help, "/stats") || !strings.Contains(help, "/users") || !strings.Contains(help, "管理员私聊") {
		t.Fatalf("missing localized admin help: %s", help)
	}
}

type activityLongNameRepo struct{ activityTestRepo }

func (*activityLongNameRepo) ListUserActivity(context.Context, int, int) (botpkg.UserActivityPage, error) {
	page := botpkg.UserActivityPage{TotalUsers: 8, Page: 1, TotalPages: 1}
	for i := range 8 {
		page.Users = append(page.Users, botpkg.UserActivity{UserID: int64(i + 1), DisplayName: strings.Repeat("🎵", 128), Username: strings.Repeat("x", 64), FirstSeenAt: time.Now(), LastSeenAt: time.Now(), RequestCount: 12345})
	}
	return page, nil
}

func TestUserActivityListFitsTelegramLimitInAllLanguages(t *testing.T) {
	_ = zhCtx()
	h := &userActivityCommands{repo: &activityLongNameRepo{}, now: time.Now}
	for _, lang := range i18n.SupportedLanguages {
		ctx := i18n.WithLocalizer(context.Background(), i18n.For(lang))
		resp := h.renderUsers(ctx, 1)
		if n := len(utf16.Encode([]rune(resp.Text))); n > 4096 {
			t.Fatalf("%s user page too long: %d UTF-16 units", lang, n)
		}
		if strings.Contains(resp.Text, "activity_users_header") {
			t.Fatalf("missing %s catalog", lang)
		}
	}
}

type activityNoopHandler struct{ calls atomic.Int64 }

func (h *activityNoopHandler) Handle(context.Context, *telego.Bot, *telego.Update) { h.calls.Add(1) }

func TestUserActivityRouterTracksDispatchedRequestsOnly(t *testing.T) {
	b, _ := newActivityTestBot(t)
	repo := &activityTestRepo{}
	h := &activityNoopHandler{}
	router := &Router{Music: h, Search: h, Callback: h, Inline: h, ChosenInline: h, GuestMode: h, Activity: NewUserActivityTracker(repo)}
	updates := make(chan telego.Update, 8)
	bhandler, err := th.NewBotHandler(b, updates)
	if err != nil {
		t.Fatal(err)
	}
	router.Register(bhandler, "music_bot")
	updates <- *activityTestUpdate()
	updates <- telego.Update{UpdateID: 2, Message: &telego.Message{MessageID: 11, Text: "song", From: &telego.User{ID: 42}, Chat: telego.Chat{ID: 42, Type: "private"}}}
	updates <- telego.Update{UpdateID: 3, CallbackQuery: &telego.CallbackQuery{ID: "cb", Data: "music anything", From: telego.User{ID: 42}}}
	updates <- telego.Update{UpdateID: 4, InlineQuery: &telego.InlineQuery{ID: "inline", Query: "song", From: telego.User{ID: 42}}}
	updates <- telego.Update{UpdateID: 5, ChosenInlineResult: &telego.ChosenInlineResult{ResultID: "song", From: telego.User{ID: 42}}}
	updates <- telego.Update{UpdateID: 6, GuestMessage: &telego.Message{MessageID: 12, Text: "song", From: &telego.User{ID: 42}, Chat: telego.Chat{ID: -100, Type: "supergroup"}}}
	updates <- telego.Update{UpdateID: 7, Message: &telego.Message{MessageID: 13, Text: "ordinary group chat", From: &telego.User{ID: 42}, Chat: telego.Chat{ID: -100, Type: "supergroup"}}}
	close(updates)
	if err := bhandler.Start(); err != nil {
		t.Fatal(err)
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := bhandler.StopWithContext(stopCtx); err != nil {
		t.Fatal(err)
	}
	if got := repo.writes.Load(); got != 6 {
		t.Fatalf("recorded %d requests, want 6 dispatched interactions", got)
	}
	if got := h.calls.Load(); got != 6 {
		t.Fatalf("handled %d requests, want 6", got)
	}
}
