package handler

import (
	"context"
	"testing"

	botpkg "github.com/liuran001/MusicBot-Go/bot"
	"github.com/mymmrac/telego"
)

const (
	scopeTestUserID  = int64(4242)
	scopeTestGroupID = int64(-100777)
)

func newLyricScopeRepo() *stubSongRepository {
	repo := newStubRepo()
	repo.userSettings[scopeTestUserID] = &botpkg.UserSettings{
		UserID:             scopeTestUserID,
		DefaultLyricFormat: "qrc",
	}
	repo.groupSettings[scopeTestGroupID] = &botpkg.GroupSettings{
		ChatID:             scopeTestGroupID,
		DefaultLyricFormat: "ass",
	}
	return repo
}

// TestSendTrackLyricsUsesUserDefaultInPrivateChat is the regression test for
// issue #5: tapping the "歌词" button under a song in a private chat produced an
// LRC document even when the user had set another default in /settings. The
// call site built a synthetic telego.Message whose Chat.Type was empty, which
// read as "not private" and sent the lookup to GetGroupSettings keyed by the
// private chat id -- a row that never exists.
func TestSendTrackLyricsUsesUserDefaultInPrivateChat(t *testing.T) {
	h := &LyricHandler{Repo: newLyricScopeRepo()}

	scope := lyricScopeFor(string(telego.ChatTypePrivate), scopeTestUserID, scopeTestUserID)
	if got := h.resolveDefaultLyricFormat(context.Background(), scope); got != "qrc" {
		t.Fatalf("private chat default = %q, want %q (the user's saved setting)", got, "qrc")
	}
}

func TestResolveDefaultLyricFormatUsesGroupDefaultInGroups(t *testing.T) {
	h := &LyricHandler{Repo: newLyricScopeRepo()}

	scope := lyricScopeFor(string(telego.ChatTypeSupergroup), scopeTestGroupID, scopeTestUserID)
	if got := h.resolveDefaultLyricFormat(context.Background(), scope); got != "ass" {
		t.Fatalf("group default = %q, want %q (the group's saved setting)", got, "ass")
	}
}

// TestPrivateLyricScopeUsesRequesterSettings covers inline (guest) mode, which
// has no chat of its own.
func TestPrivateLyricScopeUsesRequesterSettings(t *testing.T) {
	h := &LyricHandler{Repo: newLyricScopeRepo()}

	if got := h.resolveDefaultLyricFormat(context.Background(), privateLyricScope(scopeTestUserID)); got != "qrc" {
		t.Fatalf("inline default = %q, want %q", got, "qrc")
	}
}

func TestLyricScopeFromMessage(t *testing.T) {
	tests := []struct {
		name    string
		message *telego.Message
		want    lyricScope
	}{
		{
			name: "private chat resolves to the sender",
			message: &telego.Message{
				Chat: telego.Chat{ID: scopeTestUserID, Type: telego.ChatTypePrivate},
				From: &telego.User{ID: scopeTestUserID},
			},
			want: lyricScope{isPrivate: true, chatID: scopeTestUserID, userID: scopeTestUserID},
		},
		{
			name: "supergroup resolves to the chat",
			message: &telego.Message{
				Chat: telego.Chat{ID: scopeTestGroupID, Type: telego.ChatTypeSupergroup},
				From: &telego.User{ID: scopeTestUserID},
			},
			want: lyricScope{isPrivate: false, chatID: scopeTestGroupID, userID: scopeTestUserID},
		},
		{
			name:    "nil message yields the zero scope",
			message: nil,
			want:    lyricScope{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := lyricScopeFromMessage(tt.message); got != tt.want {
				t.Fatalf("lyricScopeFromMessage() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestLyricScopeFromCallbackFallsBackToRequester covers the inline-callback
// path, where query.Message carries no accessible chat.
func TestLyricScopeFromCallbackFallsBackToRequester(t *testing.T) {
	got := lyricScopeFromCallback(nil, 0, scopeTestUserID)
	want := lyricScope{isPrivate: true, chatID: scopeTestUserID, userID: scopeTestUserID}
	if got != want {
		t.Fatalf("lyricScopeFromCallback() = %+v, want %+v", got, want)
	}
}

// TestResolveDefaultLyricFormatNeverProbesGroupSettingsForPrivateChats guards
// the second half of the bug: the bad lookup also minted a spurious
// group-settings row for every private chat that requested lyrics.
func TestResolveDefaultLyricFormatNeverProbesGroupSettingsForPrivateChats(t *testing.T) {
	repo := newLyricScopeRepo()
	probe := &groupSettingsProbeRepo{stubSongRepository: repo}
	h := &LyricHandler{Repo: probe}

	scope := lyricScopeFor(string(telego.ChatTypePrivate), scopeTestUserID, scopeTestUserID)
	_ = h.resolveDefaultLyricFormat(context.Background(), scope)

	if probe.groupLookups != 0 {
		t.Fatalf("private-chat lyric resolution performed %d group-settings lookups, want 0", probe.groupLookups)
	}
}

type groupSettingsProbeRepo struct {
	*stubSongRepository
	groupLookups int
}

func (r *groupSettingsProbeRepo) GetGroupSettings(ctx context.Context, chatID int64) (*botpkg.GroupSettings, error) {
	r.groupLookups++
	return r.stubSongRepository.GetGroupSettings(ctx, chatID)
}
