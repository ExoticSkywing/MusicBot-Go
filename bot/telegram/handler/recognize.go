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

const maxRemoteRecognitionFileSize int64 = 20 * 1024 * 1024

var (
	errRecognitionMediaTooLarge = errors.New("recognition media exceeds remote download limit")
	errRecognitionGetMedia      = errors.New("failed to get recognition media")
	errRecognitionDownloadMedia = errors.New("failed to download recognition media")
)

// RecognizeHandler handles audio recognition from Telegram media.
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
	media, ok := recognitionMediaForMessage(message)
	if !ok {
		sendText(ctx, b, chatID, replyID, tr(ctx, "guest_reply_voice_note"))
		return
	}
	replyID = media.message.MessageID

	if h.RecognizeService == nil {
		sendText(ctx, b, chatID, replyID, tr(ctx, "guest_recognize_service_unavailable_admin"))
		return
	}

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

	result, inputSize, err := recognizeTelegramMedia(ctx, b, h.DownloadBot, h.RecognizeService, media)
	if err != nil {
		if h.Logger != nil {
			h.Logger.Warn("failed to recognize media", "media_type", media.kind, "file_size", inputSize, "error", err)
		}
		switch {
		case errors.Is(err, errRecognitionMediaTooLarge):
			sendText(ctx, b, chatID, replyID, tr(ctx, "guest_voice_too_large"))
		case errors.Is(err, errRecognitionGetMedia):
			sendText(ctx, b, chatID, replyID, tr(ctx, "guest_get_voice_failed"))
		case errors.Is(err, errRecognitionDownloadMedia):
			sendText(ctx, b, chatID, replyID, tr(ctx, "guest_download_voice_failed"))
		default:
			sendText(ctx, b, chatID, replyID, tr(ctx, "guest_recognize_failed_retry"))
		}
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
		h.Music.dispatch(ctx, b, media.message, result.Platform, result.TrackID, "")
	}
}

type recognitionMedia struct {
	message  *telego.Message
	fileID   string
	fileSize int64
	kind     string
}

// recognitionMediaForMessage prefers the replied media, preserving the
// established "/recognize as a reply" behavior, and otherwise accepts media on
// the command/current message (including automatic recognition in private
// chats).
func recognitionMediaForMessage(message *telego.Message) (recognitionMedia, bool) {
	if message == nil {
		return recognitionMedia{}, false
	}
	if media, ok := recognitionMediaFromMessage(message.ReplyToMessage); ok {
		return media, true
	}
	return recognitionMediaFromMessage(message)
}

func recognitionMediaFromMessage(message *telego.Message) (recognitionMedia, bool) {
	if message == nil {
		return recognitionMedia{}, false
	}
	switch {
	case message.Voice != nil && strings.TrimSpace(message.Voice.FileID) != "":
		return recognitionMedia{message: message, fileID: message.Voice.FileID, fileSize: message.Voice.FileSize, kind: "voice"}, true
	case message.Audio != nil && strings.TrimSpace(message.Audio.FileID) != "":
		return recognitionMedia{message: message, fileID: message.Audio.FileID, fileSize: message.Audio.FileSize, kind: "audio"}, true
	case message.Video != nil && strings.TrimSpace(message.Video.FileID) != "":
		return recognitionMedia{message: message, fileID: message.Video.FileID, fileSize: message.Video.FileSize, kind: "video"}, true
	case message.VideoNote != nil && strings.TrimSpace(message.VideoNote.FileID) != "":
		return recognitionMedia{message: message, fileID: message.VideoNote.FileID, fileSize: int64(message.VideoNote.FileSize), kind: "video_note"}, true
	case message.Document != nil && recognizableMediaDocument(message.Document):
		return recognitionMedia{message: message, fileID: message.Document.FileID, fileSize: message.Document.FileSize, kind: "document"}, true
	default:
		return recognitionMedia{}, false
	}
}

func recognizableMediaDocument(document *telego.Document) bool {
	if document == nil || strings.TrimSpace(document.FileID) == "" {
		return false
	}
	mimeType := strings.ToLower(strings.TrimSpace(strings.SplitN(document.MimeType, ";", 2)[0]))
	if strings.HasPrefix(mimeType, "audio/") || strings.HasPrefix(mimeType, "video/") {
		return true
	}
	switch strings.ToLower(filepath.Ext(strings.TrimSpace(document.FileName))) {
	case ".aac", ".ac3", ".aiff", ".alac", ".amr", ".ape", ".flac", ".m4a", ".mp3", ".oga", ".ogg", ".opus", ".wav", ".wma",
		".3gp", ".avi", ".m2ts", ".m4v", ".mkv", ".mov", ".mp4", ".mpeg", ".mpg", ".mts", ".ogv", ".ts", ".webm", ".wmv":
		return true
	default:
		return false
	}
}

func hasRecognitionMedia(message *telego.Message) bool {
	_, ok := recognitionMediaFromMessage(message)
	return ok
}

type recognitionInput struct {
	localPath string
	data      []byte
	fileSize  int64
}

func recognizeTelegramMedia(ctx context.Context, primaryBot, fallbackBot *telego.Bot, service recognize.Service, media recognitionMedia) (*recognize.Result, int64, error) {
	input, err := loadTelegramRecognitionInput(ctx, primaryBot, fallbackBot, media)
	if err != nil {
		return nil, media.fileSize, err
	}
	if input.localPath != "" {
		if fileService, ok := service.(recognize.FileService); ok {
			result, err := fileService.RecognizeFile(ctx, input.localPath)
			return result, input.fileSize, err
		}
		data, err := readFileLimited(input.localPath, maxRemoteRecognitionFileSize)
		if err != nil {
			return nil, input.fileSize, err
		}
		input.data = data
	}
	result, err := service.Recognize(ctx, input.data)
	return result, input.fileSize, err
}

func loadTelegramRecognitionInput(ctx context.Context, primaryBot, fallbackBot *telego.Bot, media recognitionMedia) (recognitionInput, error) {
	bots := make([]*telego.Bot, 0, 2)
	seen := make(map[*telego.Bot]struct{}, 2)
	for _, bot := range []*telego.Bot{primaryBot, fallbackBot} {
		if bot == nil {
			continue
		}
		if _, ok := seen[bot]; ok {
			continue
		}
		seen[bot] = struct{}{}
		bots = append(bots, bot)
	}
	if len(bots) == 0 {
		return recognitionInput{}, fmt.Errorf("%w: bot client is nil", errRecognitionGetMedia)
	}

	var lastErr error
	gotFile := false
	tooLarge := false
	for _, bot := range bots {
		fileInfo, err := bot.GetFile(ctx, &telego.GetFileParams{FileID: media.fileID})
		if err != nil || fileInfo == nil || strings.TrimSpace(fileInfo.FilePath) == "" {
			if err == nil {
				err = errors.New("empty Telegram file path")
			}
			lastErr = err
			continue
		}
		gotFile = true
		filePath := strings.TrimSpace(fileInfo.FilePath)
		fileSize := fileInfo.FileSize
		if fileSize <= 0 {
			fileSize = media.fileSize
		}

		if filepath.IsAbs(filePath) {
			info, statErr := os.Stat(filePath)
			if statErr == nil && info.Mode().IsRegular() {
				if fileSize <= 0 {
					fileSize = info.Size()
				}
				return recognitionInput{localPath: filePath, fileSize: fileSize}, nil
			}
			if statErr == nil {
				statErr = errors.New("Telegram file path is not a regular file")
			}
			lastErr = statErr
			continue
		}

		if fileSize > maxRemoteRecognitionFileSize {
			tooLarge = true
			continue
		}
		data, downloadErr := downloadTelegramFile(ctx, bot, filePath)
		if downloadErr != nil {
			if errors.Is(downloadErr, errRecognitionMediaTooLarge) {
				tooLarge = true
			}
			lastErr = downloadErr
			continue
		}
		if fileSize <= 0 {
			fileSize = int64(len(data))
		}
		return recognitionInput{data: data, fileSize: fileSize}, nil
	}

	if tooLarge {
		return recognitionInput{}, errRecognitionMediaTooLarge
	}
	if lastErr == nil {
		lastErr = errors.New("no usable Telegram file source")
	}
	if !gotFile {
		return recognitionInput{}, fmt.Errorf("%w: %v", errRecognitionGetMedia, lastErr)
	}
	return recognitionInput{}, fmt.Errorf("%w: %v", errRecognitionDownloadMedia, lastErr)
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
		if resp.ContentLength > maxRemoteRecognitionFileSize {
			_ = resp.Body.Close()
			lastErr = errRecognitionMediaTooLarge
			continue
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, maxRemoteRecognitionFileSize+1))
		_ = resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if int64(len(data)) > maxRemoteRecognitionFileSize {
			lastErr = errRecognitionMediaTooLarge
			continue
		}
		return data, nil
	}
	if lastErr == nil {
		lastErr = errors.New("download failed")
	}
	return nil, lastErr
}

func readFileLimited(filePath string, maxSize int64) ([]byte, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errRecognitionDownloadMedia, err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxSize+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errRecognitionDownloadMedia, err)
	}
	if int64(len(data)) > maxSize {
		return nil, errRecognitionMediaTooLarge
	}
	return data, nil
}
