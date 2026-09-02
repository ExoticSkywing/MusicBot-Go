package handler

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/mymmrac/telego"
)

func TestRecognitionMediaFromMessage(t *testing.T) {
	tests := []struct {
		name string
		msg  *telego.Message
		kind string
		id   string
		size int64
	}{
		{name: "voice", msg: &telego.Message{Voice: &telego.Voice{FileID: "voice-id", FileSize: 1}}, kind: "voice", id: "voice-id", size: 1},
		{name: "audio", msg: &telego.Message{Audio: &telego.Audio{FileID: "audio-id", FileSize: 2}}, kind: "audio", id: "audio-id", size: 2},
		{name: "video", msg: &telego.Message{Video: &telego.Video{FileID: "video-id", FileSize: 3}}, kind: "video", id: "video-id", size: 3},
		{name: "video note", msg: &telego.Message{VideoNote: &telego.VideoNote{FileID: "note-id", FileSize: 4}}, kind: "video_note", id: "note-id", size: 4},
		{name: "audio document by MIME", msg: &telego.Message{Document: &telego.Document{FileID: "mime-id", FileSize: 5, MimeType: "audio/mpeg"}}, kind: "document", id: "mime-id", size: 5},
		{name: "video document by extension", msg: &telego.Message{Document: &telego.Document{FileID: "ext-id", FileSize: 6, MimeType: "application/octet-stream", FileName: "clip.MKV"}}, kind: "document", id: "ext-id", size: 6},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			media, ok := recognitionMediaFromMessage(tt.msg)
			if !ok {
				t.Fatal("media was not recognized")
			}
			if media.message != tt.msg || media.kind != tt.kind || media.fileID != tt.id || media.fileSize != tt.size {
				t.Fatalf("unexpected media: %+v", media)
			}
		})
	}
}

func TestRecognitionMediaRejectsOtherDocuments(t *testing.T) {
	tests := []*telego.Message{
		nil,
		{},
		{Document: &telego.Document{FileID: "photo", MimeType: "image/jpeg", FileName: "cover.jpg"}},
		{Document: &telego.Document{FileID: "archive", MimeType: "application/zip", FileName: "files.zip"}},
		{Document: &telego.Document{MimeType: "audio/mpeg", FileName: "missing-id.mp3"}},
	}
	for i, msg := range tests {
		if _, ok := recognitionMediaFromMessage(msg); ok {
			t.Fatalf("case %d: non-media document was accepted", i)
		}
	}
}

func TestRecognitionMediaPrefersReply(t *testing.T) {
	reply := &telego.Message{MessageID: 10, Video: &telego.Video{FileID: "reply-video"}}
	message := &telego.Message{
		MessageID:      11,
		Audio:          &telego.Audio{FileID: "current-audio"},
		ReplyToMessage: reply,
	}
	media, ok := recognitionMediaForMessage(message)
	if !ok {
		t.Fatal("media was not recognized")
	}
	if media.message != reply || media.fileID != "reply-video" {
		t.Fatalf("got media from the wrong message: %+v", media)
	}
}

func TestPrivateRecognitionMedia(t *testing.T) {
	privateVideo := &telego.Message{Chat: telego.Chat{Type: telego.ChatTypePrivate}, Video: &telego.Video{FileID: "video"}}
	groupVideo := &telego.Message{Chat: telego.Chat{Type: telego.ChatTypeGroup}, Video: &telego.Video{FileID: "video"}}
	privateArchive := &telego.Message{Chat: telego.Chat{Type: telego.ChatTypePrivate}, Document: &telego.Document{FileID: "zip", FileName: "x.zip"}}
	if !isPrivateRecognitionMedia(privateVideo) {
		t.Fatal("private video should trigger automatic recognition")
	}
	if isPrivateRecognitionMedia(groupVideo) {
		t.Fatal("group video must not trigger automatic recognition")
	}
	if isPrivateRecognitionMedia(privateArchive) {
		t.Fatal("private non-media document must not trigger automatic recognition")
	}
}

func TestRecognizeCommandMatchesMediaCaption(t *testing.T) {
	predicate := matchRecognizeCommandFunc("MusicBot")
	tests := []struct {
		name string
		msg  *telego.Message
		want bool
	}{
		{name: "text command", msg: &telego.Message{Text: "/recognize"}, want: true},
		{name: "caption command", msg: &telego.Message{Caption: "/recognize@MusicBot"}, want: true},
		{name: "other bot", msg: &telego.Message{Caption: "/recognize@OtherBot"}, want: false},
		{name: "ordinary caption", msg: &telego.Message{Caption: "recognize this"}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := predicate(context.Background(), telego.Update{Message: tt.msg}); got != tt.want {
				t.Fatalf("predicate = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestReadFileLimited(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "media.bin")
	if err := os.WriteFile(filePath, []byte("12345"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	data, err := readFileLimited(filePath, 5)
	if err != nil || string(data) != "12345" {
		t.Fatalf("readFileLimited exact limit = %q, %v", data, err)
	}
	if _, err := readFileLimited(filePath, 4); !errors.Is(err, errRecognitionMediaTooLarge) {
		t.Fatalf("over-limit error = %v, want errRecognitionMediaTooLarge", err)
	}
}
