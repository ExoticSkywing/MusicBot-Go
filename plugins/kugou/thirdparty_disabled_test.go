package kugou

import (
	"context"
	"errors"
	"testing"

	"github.com/liuran001/MusicBot-Go/bot/platform"
	"github.com/liuran001/MusicBot-Go/plugins/thirdparty"
)

func TestDisabledThirdPartyPreservesKugouOfficialResult(t *testing.T) {
	for _, failed := range []bool{false, true} {
		want := &platform.DownloadInfo{URL: "https://example.test/official.flac"}
		var wantErr error
		if failed {
			want, wantErr = nil, errors.New("official unavailable")
		}
		officialCalls := 0
		official := func(context.Context, string, platform.Quality) (*platform.DownloadInfo, error) {
			officialCalls++
			return want, wantErr
		}
		thirdParty := func(context.Context, string, platform.Quality) (*platform.DownloadInfo, error) {
			t.Error("disabled mode called a third-party source")
			return nil, nil
		}
		got, err := resolveKugouDownload(t.Context(), thirdparty.ModeDisabled, official, thirdParty, "track", platform.QualityLossless)
		if officialCalls != 1 || got != want || err != wantErr {
			t.Fatalf("official result changed: calls=%d err=%v", officialCalls, err)
		}
	}
}
