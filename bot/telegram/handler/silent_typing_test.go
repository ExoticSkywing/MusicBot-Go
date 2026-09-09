package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mymmrac/telego"
)

func TestSilentGroupLinkEligibility(t *testing.T) {
	for _, tc := range []struct {
		name, chat, text string
		forced, want     bool
	}{
		{"group", "group", "https://music.apple.com/us/song/123", false, true},
		{"topic", "supergroup", "https://example.com", false, true},
		{"private", "private", "https://example.com", false, false},
		{"command", "group", "/music https://example.com", false, false},
		{"text", "group", "hello", false, false},
		{"explicit", "group", "https://example.com", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.forced {
				ctx = withForceNonSilent(ctx)
			}
			if got := isSilentGroupLink(ctx, &telego.Message{Chat: telego.Chat{Type: tc.chat}, Text: tc.text}); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestSilentTypingStopsWithoutSendingMessage(t *testing.T) {
	actions := make(chan telego.SendChatActionParams, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/sendChatAction") {
			t.Errorf("unexpected message API: %s", r.URL.Path)
		}
		var action telego.SendChatActionParams
		if err := json.NewDecoder(r.Body).Decode(&action); err != nil {
			t.Error(err)
		}
		actions <- action
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer server.Close()
	b, err := telego.NewBot("123456789:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghi", telego.WithAPIServer(server.URL), telego.WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := startSilentLinkTyping(context.Background(), b, &telego.Message{Chat: telego.Chat{ID: -100, Type: "supergroup"}, MessageThreadID: 42, Text: "https://example.com"})
	defer stop()
	select {
	case action := <-actions:
		if action.Action != telego.ChatActionTyping || action.ChatID.ID != -100 || action.MessageThreadID != 42 {
			t.Fatalf("wrong action: %+v", action)
		}
	case <-time.After(time.Second):
		t.Fatal("no typing action")
	}
	stopSilentLinkTyping(ctx)
	stop() // Completion and send handoff may both stop the same lifecycle.
	select {
	case action := <-actions:
		t.Fatalf("action after stop: %+v", action)
	case <-time.After(4200 * time.Millisecond):
	}
}
