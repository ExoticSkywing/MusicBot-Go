package handler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	logpkg "github.com/liuran001/MusicBot-Go/bot/logger"
	"github.com/liuran001/MusicBot-Go/bot/recognize"
	"github.com/liuran001/MusicBot-Go/bot/telegram"
	"github.com/mymmrac/telego"
)

// RecognizeHandler handles voice recognition.
type RecognizeHandler struct {
	CacheDir         string
	Music            *MusicHandler
	RateLimiter      *telegram.RateLimiter
	ResourceLimiter  *ResourceRateLimiter
	RecognizeService recognize.Service
	Logger           *logpkg.Logger
	DownloadBot      *telego.Bot
}

func (h *RecognizeHandler) Handle(ctx context.Context, b *telego.Bot, update *telego.Update) {
	if update == nil || update.Message == nil {
		return
	}
	message := update.Message
	chatID := message.Chat.ID
	replyID := message.MessageID
	voiceMessage := message.ReplyToMessage
	if voiceMessage != nil && voiceMessage.Voice != nil {
		replyID = voiceMessage.MessageID
	} else if message.Voice != nil {
		voiceMessage = message
	} else {
		sendText(ctx, b, chatID, replyID, tr(ctx, "guest_reply_voice_note"))
		return
	}

	if h.CacheDir == "" {
		h.CacheDir = "./cache"
	}
	ensureDir(h.CacheDir)

	// Recognition is the single most expensive op (Telegram file download +
	// ffmpeg transcode + external fingerprint API, then a chained download), so
	// throttle it before any of that work begins. Platform is unknown until after
	// recognition, so this keys on user + global only.
	var recognizeUserID int64
	if message.From != nil {
		recognizeUserID = message.From.ID
	}
	if !h.ResourceLimiter.AllowFor(ActionRecognize, recognizeUserID, chatID, "") {
		sendText(ctx, b, chatID, replyID, tr(ctx, "err_rate_limited"))
		return
	}

	fileBot := b
	if h.DownloadBot != nil {
		fileBot = h.DownloadBot
	}
	fileInfo, err := fileBot.GetFile(ctx, &telego.GetFileParams{FileID: voiceMessage.Voice.FileID})
	if err != nil || fileInfo == nil || fileInfo.FilePath == "" {
		sendText(ctx, b, chatID, replyID, tr(ctx, "guest_get_voice_failed"))
		return
	}
	if fileInfo.FileSize > 20*1024*1024 {
		sendText(ctx, b, chatID, replyID, tr(ctx, "guest_voice_too_large"))
		return
	}
	audioData, err := downloadTelegramFile(ctx, fileBot, fileInfo.FilePath)
	if err != nil {
		if h.Logger != nil {
			h.Logger.Warn("failed to download voice", "file_path", fileInfo.FilePath, "error", err)
		}
		sendText(ctx, b, chatID, replyID, tr(ctx, "guest_download_voice_failed"))
		return
	}

	if h.RecognizeService == nil {
		sendText(ctx, b, chatID, replyID, tr(ctx, "guest_recognize_service_unavailable_admin"))
		return
	}

	result, err := h.RecognizeService.Recognize(ctx, audioData)
	if err != nil {
		if h.Logger != nil {
			h.Logger.Error("recognition service error", "error", err, "audio_size", len(audioData))
		}
		sendText(ctx, b, chatID, replyID, tr(ctx, "guest_recognize_failed_retry"))
		return
	}

	if result == nil || result.TrackID == "" || result.Platform == "" {
		if h.Logger != nil {
			h.Logger.Info("recognition returned no results")
		}
		sendText(ctx, b, chatID, replyID, tr(ctx, "guest_recognize_failed_short"))
		return
	}

	if h.Logger != nil {
		h.Logger.Debug("recognition result", "platform", result.Platform, "track_id", result.TrackID)
	}

	if result.URL != "" {
		params := &telego.SendMessageParams{
			ChatID:          telego.ChatID{ID: chatID},
			Text:            result.URL,
			ReplyParameters: &telego.ReplyParameters{MessageID: replyID},
		}
		if h.RateLimiter != nil {
			_, _ = telegram.SendMessageWithRetry(ctx, h.RateLimiter, b, params)
		} else {
			_, _ = b.SendMessage(ctx, params)
		}
	}

	if h.Music != nil {
		h.Music.dispatch(ctx, b, voiceMessage, result.Platform, result.TrackID, "")
	}
}

func sendText(ctx context.Context, b *telego.Bot, chatID int64, replyID int, text string) {
	if b == nil {
		return
	}
	params := &telego.SendMessageParams{
		ChatID:          telego.ChatID{ID: chatID},
		Text:            text,
		ReplyParameters: &telego.ReplyParameters{MessageID: replyID},
	}
	_, _ = b.SendMessage(ctx, params)
}

func downloadTelegramFile(ctx context.Context, b *telego.Bot, filePath string) ([]byte, error) {
	filePath = strings.TrimSpace(filePath)
	if filePath == "" {
		return nil, fmt.Errorf("empty file path")
	}
	if filepath.IsAbs(filePath) {
		if data, err := os.ReadFile(filePath); err == nil {
			return data, nil
		}
	}
	if b == nil {
		return nil, fmt.Errorf("bot client is nil")
	}
	fileURLs := make([]string, 0, 3)
	fileURLs = append(fileURLs, b.FileDownloadURL(filePath))
	trimmed := strings.TrimLeft(filePath, "/")
	if trimmed != filePath {
		fileURLs = append(fileURLs, b.FileDownloadURL(trimmed))
	}
	if token := b.Token(); token != "" {
		needle := token + "/"
		if idx := strings.Index(filePath, needle); idx >= 0 {
			relative := strings.TrimLeft(filePath[idx+len(needle):], "/")
			if relative != "" {
				fileURLs = append(fileURLs, b.FileDownloadURL(relative))
			}
		}
		for _, urlStr := range append([]string(nil), fileURLs...) {
			noTokenURL := strings.Replace(urlStr, "/file/bot"+token+"/", "/file/", 1)
			if noTokenURL != urlStr {
				fileURLs = append(fileURLs, noTokenURL)
			}
		}
	}
	client := &http.Client{Timeout: 30 * time.Second}
	var lastErr error
	for _, fileURL := range fileURLs {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL, nil)
		if err != nil {
			lastErr = err
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			lastErr = fmt.Errorf("unexpected status: %s", resp.Status)
			_ = resp.Body.Close()
			continue
		}
		data, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		return data, nil
	}
	if lastErr == nil {
		lastErr = errors.New("download failed")
	}
	return nil, lastErr
}
