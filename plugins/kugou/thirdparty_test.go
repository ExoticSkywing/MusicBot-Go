package kugou

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/liuran001/MusicBot-Go/bot/platform"
	"github.com/liuran001/MusicBot-Go/plugins/thirdparty"
)

func TestResolveKugouDownloadThirdPartyFirst(t *testing.T) {
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
	got, err := resolveKugouDownload(t.Context(), thirdparty.ModeThirdPartyFirst, official, alternative, "track", platform.QualityHigh)
	if err != nil {
		t.Fatalf("resolveKugouDownload: %v", err)
	}
	if got != want {
		t.Fatalf("info = %#v, want %#v", got, want)
	}
	if len(calls) != 1 || calls[0] != "third-party" {
		t.Fatalf("calls = %#v, want [third-party]", calls)
	}
}

func TestResolveKugouDownloadThirdPartyFailureFallsBackOfficial(t *testing.T) {
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
	got, err := resolveKugouDownload(t.Context(), thirdparty.ModeThirdPartyFirst, official, alternative, "track", platform.QualityHigh)
	if err != nil {
		t.Fatalf("resolveKugouDownload: %v", err)
	}
	if got != want {
		t.Fatalf("info = %#v, want %#v", got, want)
	}
	if len(calls) != 2 || calls[0] != "third-party" || calls[1] != "official" {
		t.Fatalf("calls = %#v, want [third-party official]", calls)
	}
}

func TestResolveKugouDownloadOfficialFirstPreservesOfficialError(t *testing.T) {
	officialErr := errors.New("official unavailable")
	official := func(context.Context, string, platform.Quality) (*platform.DownloadInfo, error) {
		return nil, officialErr
	}
	alternative := func(context.Context, string, platform.Quality) (*platform.DownloadInfo, error) {
		return nil, errors.New("provider internals")
	}
	_, err := resolveKugouDownload(t.Context(), thirdparty.ModeOfficialFirst, official, alternative, "track", platform.QualityHigh)
	if !errors.Is(err, officialErr) {
		t.Fatalf("error = %v, want official error", err)
	}
}

func TestKugouThirdPartyStatusLines(t *testing.T) {
	client := NewClient("", nil)
	client.AttachConcept(NewConceptSessionManager(nil, nil, conceptSession{}))
	kugou := NewPlatform(client)
	resolver, err := thirdparty.NewChain([]string{"jbsou"}, 0, nil)
	if err != nil {
		t.Fatalf("NewChain: %v", err)
	}
	kugou.ConfigureThirdPartyAudio(thirdparty.ModeThirdPartyFirst, resolver)

	status, err := kugou.AccountStatus(t.Context())
	if err != nil {
		t.Fatalf("AccountStatus: %v", err)
	}
	highlights := strings.Join(status.Highlights, "\n")
	if !strings.Contains(highlights, "音源策略：第三方优先") {
		t.Fatalf("highlights missing source strategy: %q", highlights)
	}
	if !strings.Contains(highlights, "调用顺序：jbsou → 酷狗官方") {
		t.Fatalf("highlights missing source order: %q", highlights)
	}
	if !status.ThirdPartyAudioAvailable {
		t.Fatal("third-party-first account status should report audio availability")
	}
}

type captureKugouResolver struct {
	platformName string
	trackID      string
}

func (r *captureKugouResolver) Resolve(_ context.Context, platformName, trackID string, _ platform.Quality) (*platform.DownloadInfo, error) {
	r.platformName = platformName
	r.trackID = trackID
	return &platform.DownloadInfo{URL: "https://example.test/track.mp3", Size: 1, Format: "mp3"}, nil
}

func TestKugouThirdPartyUsesNormalizedHash(t *testing.T) {
	resolver := &captureKugouResolver{}
	kugou := NewPlatform(nil)
	kugou.ConfigureThirdPartyAudio(thirdparty.ModeThirdPartyFirst, resolver)
	_, err := kugou.getThirdPartyDownloadInfo(t.Context(), "055A804B8A3CCC05240D08F8FF1F7DE8", platform.QualityHigh)
	if err != nil {
		t.Fatalf("getThirdPartyDownloadInfo: %v", err)
	}
	if resolver.platformName != "kugou" || resolver.trackID != "055a804b8a3ccc05240d08f8ff1f7de8" {
		t.Fatalf("resolved platform=%q track=%q", resolver.platformName, resolver.trackID)
	}
}
