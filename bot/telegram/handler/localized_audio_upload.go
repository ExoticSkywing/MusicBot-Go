package handler

import (
	"context"
	"errors"
	"os"
	"strings"

	botpkg "github.com/liuran001/MusicBot-Go/bot"
	"github.com/liuran001/MusicBot-Go/bot/telegram"
	"github.com/mymmrac/telego"
)

// uploadInlineAudio publishes a prepared file to the configured staging chat.
// Its FileID becomes reusable only after Telegram confirms the upload.
func (h *MusicHandler) uploadInlineAudio(ctx context.Context, b *telego.Bot, songInfo *botpkg.SongInfo, musicPath, picPath string, progress func(string)) error {
	uploadChatID := h.InlineUploadChatID
	if uploadChatID == 0 {
		return errors.New("InlineUploadChatID not configured")
	}

	if progress != nil {
		progress(buildMusicInfoText(ctx, songInfo.SongName, songInfo.SongAlbum, formatFileInfo(songInfo.FileExt, songInfo.MusicSize), tr(ctx, "uploading")))
	}

	uploadBot := b
	if h.UploadBot != nil {
		uploadBot = h.UploadBot
	}
	file, err := os.Open(musicPath)
	if err != nil {
		return err
	}
	defer file.Close()
	caption := buildMusicCaption(ctx, h.PlatformManager, songInfo, h.BotName)
	params := &telego.SendAudioParams{
		ChatID:    telego.ChatID{ID: uploadChatID},
		Audio:     telego.InputFile{File: file},
		Caption:   caption,
		ParseMode: telego.ModeHTML,
		Title:     songInfo.SongName,
		Performer: songInfo.SongArtists,
		Duration:  songInfo.Duration,
	}
	if strings.TrimSpace(picPath) != "" {
		if thumbStat, thumbErr := os.Stat(picPath); thumbErr == nil && thumbStat.Size() > 0 {
			if thumbFile, thumbOpenErr := os.Open(picPath); thumbOpenErr == nil {
				defer thumbFile.Close()
				params.Thumbnail = &telego.InputFile{File: thumbFile}
			}
		}
	}
	var uploaded *telego.Message
	if h.RateLimiter != nil {
		uploaded, err = telegram.SendAudioWithRetry(ctx, h.RateLimiter, uploadBot, params)
	} else {
		uploaded, err = uploadBot.SendAudio(ctx, params)
	}
	if err != nil || uploaded == nil || uploaded.Audio == nil || strings.TrimSpace(uploaded.Audio.FileID) == "" {
		if err == nil {
			err = errors.New("upload failed")
		}
		return err
	}
	songInfo.FileID = uploaded.Audio.FileID
	if uploaded.Audio.Thumbnail != nil {
		songInfo.ThumbFileID = uploaded.Audio.Thumbnail.FileID
	}

	if h.Repo != nil {
		_ = h.Repo.Create(ctx, songInfo)
	}
	return nil
}
