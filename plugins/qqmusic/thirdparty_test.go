package qqmusic

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/liuran001/MusicBot-Go/bot/platform"
	"github.com/liuran001/MusicBot-Go/plugins/thirdparty"
)

func TestResolveQQMusicDownloadThirdPartyFirst(t *testing.T) {
	calls := []string{}
	want := &platform.DownloadInfo{URL: "https://example.test/track.mp3", Size: 1, Format: "mp3"}
	official := func(context.Context, string, platform.Quality) (*platform.DownloadInfo, error) {
		calls = append(calls, "official")
		return nil, errors.New("official unavailable")
	}
	alternative := func(context.Context, string, platform.Quality) (*platform.DownloadInfo, error) {
		calls = append(calls, "third-party")
		return want, nil
	}
	got, err := resolveQQMusicDownload(t.Context(), thirdparty.ModeThirdPartyFirst, official, alternative, "track", platform.QualityHigh)
	if err != nil {
		t.Fatalf("resolveQQMusicDownload: %v", err)
	}
	if got != want {
		t.Fatalf("info = %#v, want %#v", got, want)
	}
	if len(calls) != 1 || calls[0] != "third-party" {
		t.Fatalf("calls = %#v, want [third-party]", calls)
	}
}

func TestResolveQQMusicDownloadThirdPartyFailureFallsBackOfficial(t *testing.T) {
	calls := []string{}
	want := &platform.DownloadInfo{URL: "https://example.test/official.mp3", Size: 1, Format: "mp3"}
	official := func(context.Context, string, platform.Quality) (*platform.DownloadInfo, error) {
		calls = append(calls, "official")
		return want, nil
	}
	alternative := func(context.Context, string, platform.Quality) (*platform.DownloadInfo, error) {
		calls = append(calls, "third-party")
		return nil, errors.New("third-party unavailable")
	}
	got, err := resolveQQMusicDownload(t.Context(), thirdparty.ModeThirdPartyFirst, official, alternative, "track", platform.QualityHigh)
	if err != nil {
		t.Fatalf("resolveQQMusicDownload: %v", err)
	}
	if got != want {
		t.Fatalf("info = %#v, want %#v", got, want)
	}
	if len(calls) != 2 || calls[0] != "third-party" || calls[1] != "official" {
		t.Fatalf("calls = %#v, want [third-party official]", calls)
	}
}

func TestResolveQQMusicDownloadOfficialFirstPreservesOfficialError(t *testing.T) {
	officialErr := errors.New("official unavailable")
	calls := []string{}
	official := func(context.Context, string, platform.Quality) (*platform.DownloadInfo, error) {
		calls = append(calls, "official")
		return nil, officialErr
	}
	alternative := func(context.Context, string, platform.Quality) (*platform.DownloadInfo, error) {
		calls = append(calls, "third-party")
		return nil, errors.New("provider internals")
	}
	_, err := resolveQQMusicDownload(t.Context(), thirdparty.ModeOfficialFirst, official, alternative, "track", platform.QualityHigh)
	if !errors.Is(err, officialErr) {
		t.Fatalf("error = %v, want official error", err)
	}
	if len(calls) != 2 || calls[0] != "official" || calls[1] != "third-party" {
		t.Fatalf("calls = %#v, want [official third-party]", calls)
	}
}

func TestResolveQQMusicDownloadDisabledSkipsThirdParty(t *testing.T) {
	calls := []string{}
	want := &platform.DownloadInfo{URL: "https://example.test/official.mp3", Size: 1, Format: "mp3"}
	official := func(context.Context, string, platform.Quality) (*platform.DownloadInfo, error) {
		calls = append(calls, "official")
		return want, nil
	}
	alternative := func(context.Context, string, platform.Quality) (*platform.DownloadInfo, error) {
		calls = append(calls, "third-party")
		return nil, nil
	}
	got, err := resolveQQMusicDownload(t.Context(), thirdparty.ModeDisabled, official, alternative, "track", platform.QualityHigh)
	if err != nil || got != want {
		t.Fatalf("info=%#v error=%v, want official success", got, err)
	}
	if len(calls) != 1 || calls[0] != "official" {
		t.Fatalf("calls = %#v, want [official]", calls)
	}
}

func TestThirdPartyStatusLines(t *testing.T) {
	client := NewClient("uin=12345; qqmusic_key=test-key", 0, nil, false, 0, nil)
	qq := NewPlatform(client)
	resolver, err := thirdparty.NewChain([]string{"jbsou"}, 0, nil)
	if err != nil {
		t.Fatalf("NewChain: %v", err)
	}
	qq.ConfigureThirdPartyAudio(thirdparty.ModeThirdPartyFirst, resolver)

	status, err := qq.AccountStatus(t.Context())
	if err != nil {
		t.Fatalf("AccountStatus: %v", err)
	}
	highlights := strings.Join(status.Highlights, "\n")
	if !strings.Contains(highlights, "音源策略：第三方优先") {
		t.Fatalf("highlights missing source strategy: %q", highlights)
	}
	if !strings.Contains(highlights, "调用顺序：jbsou → QQ 官方") {
		t.Fatalf("highlights missing source order: %q", highlights)
	}
}
