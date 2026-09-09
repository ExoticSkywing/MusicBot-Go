package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/platform"
	"github.com/mymmrac/telego"
)

type verificationTelegramCall struct {
	method  string
	payload map[string]any
}

type verificationTelegramRecorder struct {
	mu    sync.Mutex
	calls []verificationTelegramCall
}

func newVerificationTestBot(t *testing.T) (*telego.Bot, *verificationTelegramRecorder) {
	t.Helper()
	recorder := &verificationTelegramRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode Telegram request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		recorder.mu.Lock()
		recorder.calls = append(recorder.calls, verificationTelegramCall{method: method, payload: payload})
		recorder.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch method {
		case "sendChatAction":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": true})
		case "sendMessage", "editMessageText", "editMessageReplyMarkup":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"result": map[string]any{
					"message_id": 700,
					"date":       1,
					"chat":       map[string]any{"id": 1001, "type": "private"},
					"text":       payload["text"],
				},
			})
		default:
			t.Errorf("unexpected Telegram method %q", method)
			http.Error(w, "unexpected method", http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	bot, err := telego.NewBot("123456:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghi", telego.WithAPIServer(server.URL))
	if err != nil {
		t.Fatalf("NewBot() error = %v", err)
	}
	return bot, recorder
}

func (r *verificationTelegramRecorder) payloads(method string) []map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	var payloads []map[string]any
	for _, call := range r.calls {
		if call.method == method {
			payloads = append(payloads, call.payload)
		}
	}
	return payloads
}

type verificationDownloadPlatform struct {
	*stubPlatform
	err error
}

func (p *verificationDownloadPlatform) GetDownloadInfo(context.Context, string, platform.Quality) (*platform.DownloadInfo, error) {
	return nil, p.err
}

func newVerificationMusicHandler(err error) *MusicHandler {
	manager := newStubManager()
	manager.Register(&verificationDownloadPlatform{stubPlatform: newStubPlatform("kugou"), err: err})
	return &MusicHandler{
		Repo:            newStubRepo(),
		PlatformManager: manager,
		DefaultQuality:  "lossless",
		ProcessTimeout:  time.Second,
	}
}

func telegramRecordedText(recorder *verificationTelegramRecorder) []string {
	var texts []string
	for _, method := range []string{"sendMessage", "editMessageText"} {
		for _, payload := range recorder.payloads(method) {
			if value, ok := payload["text"].(string); ok {
				texts = append(texts, value)
			}
		}
	}
	return texts
}

func assertVerificationURLDelivered(t *testing.T, recorder *verificationTelegramRecorder, verificationURL string) {
	t.Helper()
	for _, text := range telegramRecordedText(recorder) {
		if strings.Contains(text, verificationURL) && strings.Contains(text, "完成验证") && strings.Contains(text, "重新点歌") {
			return
		}
	}
	t.Fatalf("verification guidance was not delivered; texts=%q", telegramRecordedText(recorder))
}

func TestProcessMusicDeliversVerificationURL(t *testing.T) {
	for _, tt := range []struct {
		name    string
		chat    telego.Chat
		ctx     context.Context
		message string
	}{
		{
			name:    "private request",
			chat:    telego.Chat{ID: 1001, Type: "private"},
			ctx:     withForceNonSilent(zhCtx()),
			message: "/music test",
		},
		{
			name:    "silent group auto fetch",
			chat:    telego.Chat{ID: -1001, Type: "supergroup"},
			ctx:     zhCtx(),
			message: "https://www.kugou.com/song/test.html",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			bot, recorder := newVerificationTestBot(t)
			verificationURL := "https://verify.example/challenge?token=opaque"
			verificationErr := &platform.VerificationRequiredError{URL: verificationURL, ExpiresAt: time.Now().Add(time.Minute)}
			h := newVerificationMusicHandler(verificationErr)
			message := &telego.Message{
				MessageID: 7,
				Text:      tt.message,
				From:      &telego.User{ID: 42},
				Chat:      tt.chat,
			}

			err := h.processMusic(tt.ctx, bot, message, "kugou", "track-id", "lossless")
			var gotVerificationErr *platform.VerificationRequiredError
			if !errors.As(err, &gotVerificationErr) || gotVerificationErr != verificationErr {
				t.Fatalf("processMusic() error = %v", err)
			}
			assertVerificationURLDelivered(t, recorder, verificationURL)
		})
	}
}

func TestRunInlineMediaFlowDeliversVerificationURL(t *testing.T) {
	bot, recorder := newVerificationTestBot(t)
	verificationURL := "https://verify.example/challenge?token=inline-opaque"
	verificationErr := &platform.VerificationRequiredError{URL: verificationURL, ExpiresAt: time.Now().Add(time.Minute)}
	h := newVerificationMusicHandler(verificationErr)

	runInlineMediaFlow(
		withDownloadWorkAdmission(zhCtx()),
		bot,
		inlineMediaFlowDeps{Music: h},
		"inline-verification",
		42,
		"tester",
		"kugou",
		"track-id",
		"lossless",
		0,
		false,
	)

	assertVerificationURLDelivered(t, recorder, verificationURL)
}
