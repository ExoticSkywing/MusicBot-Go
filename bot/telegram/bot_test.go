package telegram

import (
	"errors"
	"strings"
	"testing"

	botpkg "github.com/liuran001/MusicBot-Go/bot"
)

type telegramLogCapture struct {
	entries []string
}

func (l *telegramLogCapture) append(msg string, args ...any) {
	l.entries = append(l.entries, msg)
}

func (l *telegramLogCapture) Debug(msg string, args ...any)  { l.append(msg, args...) }
func (l *telegramLogCapture) Info(msg string, args ...any)   { l.append(msg, args...) }
func (l *telegramLogCapture) Warn(msg string, args ...any)   { l.append(msg, args...) }
func (l *telegramLogCapture) Error(msg string, args ...any)  { l.append(msg, args...) }
func (l *telegramLogCapture) With(args ...any) botpkg.Logger { return l }

func TestAllowedUpdates(t *testing.T) {
	want := []string{
		"message",
		"callback_query",
		"inline_query",
		"chosen_inline_result",
		"guest_message",
	}

	got := AllowedUpdates()
	if len(got) != len(want) {
		t.Fatalf("AllowedUpdates() len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("AllowedUpdates()[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	got[0] = "edited_message"
	again := AllowedUpdates()
	if again[0] != want[0] {
		t.Fatalf("AllowedUpdates() should return a copy, got first value %q, want %q", again[0], want[0])
	}
}

func TestLongPollingParams(t *testing.T) {
	params := LongPollingParams()
	if params == nil {
		t.Fatal("LongPollingParams() = nil")
	}
	if params.Timeout != longPollingTimeoutSeconds {
		t.Fatalf("LongPollingParams().Timeout = %d, want %d", params.Timeout, longPollingTimeoutSeconds)
	}

	want := AllowedUpdates()
	if len(params.AllowedUpdates) != len(want) {
		t.Fatalf("LongPollingParams().AllowedUpdates len = %d, want %d", len(params.AllowedUpdates), len(want))
	}
	for i := range want {
		if params.AllowedUpdates[i] != want[i] {
			t.Fatalf("LongPollingParams().AllowedUpdates[%d] = %q, want %q", i, params.AllowedUpdates[i], want[i])
		}
	}

	params.AllowedUpdates[0] = "edited_message"
	if AllowedUpdates()[0] != want[0] {
		t.Fatalf("LongPollingParams() should not share backing array with AllowedUpdates")
	}
}

func TestWebhookParams(t *testing.T) {
	params := WebhookParams("https://example.com/hook", "secret-token")
	if params == nil {
		t.Fatal("WebhookParams() = nil")
	}
	if params.URL != "https://example.com/hook" {
		t.Fatalf("WebhookParams().URL = %q", params.URL)
	}
	if params.SecretToken != "secret-token" {
		t.Fatalf("WebhookParams().SecretToken = %q", params.SecretToken)
	}

	want := AllowedUpdates()
	if len(params.AllowedUpdates) != len(want) {
		t.Fatalf("WebhookParams().AllowedUpdates len = %d, want %d", len(params.AllowedUpdates), len(want))
	}
	for i := range want {
		if params.AllowedUpdates[i] != want[i] {
			t.Fatalf("WebhookParams().AllowedUpdates[%d] = %q, want %q", i, params.AllowedUpdates[i], want[i])
		}
	}

	params.AllowedUpdates[0] = "edited_message"
	if AllowedUpdates()[0] != want[0] {
		t.Fatalf("WebhookParams() should not share backing array with AllowedUpdates")
	}
}

func TestTelegoLoggerRedactsExactBotToken(t *testing.T) {
	const token = "000000:synthetic-token-for-tests"
	capture := &telegramLogCapture{}
	log := telegoLogger{logger: capture, botToken: token}

	log.Debugf("polling %s", "https://telegram.invalid/bot"+token+"/getUpdates")
	log.Errorf("upload failed: %v", errors.New("request bot"+token+"/sendAudio"))

	output := strings.Join(capture.entries, "\n")
	if strings.Contains(output, token) {
		t.Fatalf("telego logger leaked synthetic bot token: %q", output)
	}
	if !strings.Contains(output, "bot[REDACTED]/getUpdates") {
		t.Fatalf("telego logger did not redact polling URL exactly: %q", output)
	}
	if !strings.Contains(output, "bot[REDACTED]/sendAudio") {
		t.Fatalf("telego logger did not redact error text exactly: %q", output)
	}
}

func TestTelegoLoggerEmptyBotTokenDoesNotAlterMessage(t *testing.T) {
	capture := &telegramLogCapture{}
	log := telegoLogger{logger: capture}

	log.Errorf("request %s", "bot/path")

	if got, want := strings.Join(capture.entries, "\n"), "request bot/path"; got != want {
		t.Fatalf("telego logger with empty token = %q, want %q", got, want)
	}
}
