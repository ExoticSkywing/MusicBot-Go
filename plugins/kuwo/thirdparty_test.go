package kuwo

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/liuran001/MusicBot-Go/bot/platform"
	"github.com/liuran001/MusicBot-Go/plugins/thirdparty"
)

func TestResolveKuwoDownloadThirdPartyFirst(t *testing.T) {
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
	got, err := resolveKuwoDownload(t.Context(), thirdparty.ModeThirdPartyFirst, official, alternative, "track", platform.QualityHigh)
	if err != nil || got != want {
		t.Fatalf("info=%#v error=%v, want third-party success", got, err)
	}
	if strings.Join(calls, ",") != "third-party" {
		t.Fatalf("calls = %#v, want [third-party]", calls)
	}
}

func TestKuwoThirdPartyStatusLines(t *testing.T) {
	kuwo := NewPlatform(NewClient(0, nil))
	resolver, err := thirdparty.NewChain([]string{"jbsou"}, 0, nil)
	if err != nil {
		t.Fatalf("NewChain: %v", err)
	}
	kuwo.ConfigureThirdPartyAudio(thirdparty.ModeThirdPartyFirst, resolver)

	status, err := kuwo.AccountStatus(t.Context())
	if err != nil {
		t.Fatalf("AccountStatus: %v", err)
	}
	if !status.ThirdPartyAudioAvailable || !status.Available || !status.NoLoginRequired {
		t.Fatalf("unexpected availability flags: %+v", status)
	}
	highlights := strings.Join(status.Highlights, "\n")
	if !strings.Contains(highlights, "音源策略：第三方优先") || !strings.Contains(highlights, "调用顺序：jbsou → 酷我官方") {
		t.Fatalf("unexpected highlights: %q", highlights)
	}
}
