package kuwo

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/platform"
)

// legacyTierTransport serves a legacy play response declaring the given bitrate,
// backed by a FLAC stream of the given shape.
func legacyTierTransport(
	t *testing.T,
	declaredBitrate string,
	stream []byte,
) http.RoundTripper {
	t.Helper()
	return roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Host {
		case "mobi.kuwo.cn":
			return response(http.StatusOK, nil, []byte(
				"url=https://kw-er.kuwo.cn/a.flac\nformat=flac\nbitrate="+
					declaredBitrate+"\nrid=41378936\nduration=213\ntype=0\n",
			)), nil
		case "kw-er.kuwo.cn":
			if req.Header.Get("Range") == "bytes=0-41" {
				return response(
					http.StatusPartialContent,
					map[string]string{"Content-Range": "bytes 0-41/1048576"},
					stream[:42],
				), nil
			}
			return directFLACTestTailResponse(t, req, stream), nil
		default:
			t.Fatalf("unexpected host %q", req.URL.Host)
			return nil, nil
		}
	})
}

func resolveLegacyTier(t *testing.T, rt http.RoundTripper) (*platform.DownloadInfo, error) {
	t.Helper()
	client := NewClient(time.Second, nil)
	client.apiHTTPClient.Transport = rt
	client.mediaHTTPClient.Transport = rt
	client.downloadHTTPClient = &http.Client{Transport: rt}
	return client.resolvePlayableLossless(context.Background(), &trackDetail{
		Track: platform.Track{ID: "41378936", Duration: 213 * time.Second},
	})
}

// TestLegacyPlayAcceptsHiResTier is the regression for tracks kuwo serves at its
// 4000 tier. The resolver used to require exactly 2000, so a track kuwo would
// hand over at 24-bit/96kHz was rejected as a "selector bitrate mismatch"; with
// every other resolver also failing, the request then surfaced as "paid track".
// Jay Chou's 告白气球 (rid 7149583) behaves exactly this way and had never once
// downloaded from kuwo.
func TestLegacyPlayAcceptsHiResTier(t *testing.T) {
	const rawSize = 1 << 20
	cleartext := makeTestFLAC(t, rawSize-len(knownDirectFLACTrailer), 96000, 24, 2, 213*time.Second)
	stream := append(append([]byte(nil), cleartext...), knownDirectFLACTrailer...)

	info, err := resolveLegacyTier(t, legacyTierTransport(t, "4000", stream))
	if err != nil {
		t.Fatalf("resolvePlayableLossless() = %v, want the 4000 tier accepted", err)
	}
	if info.Quality != platform.QualityHiRes {
		t.Fatalf("quality = %v, want %v", info.Quality, platform.QualityHiRes)
	}
	if info.Format != "flac" {
		t.Fatalf("format = %q, want flac", info.Format)
	}
}

// TestLegacyPlayStillAcceptsLosslessTier guards the tier that already worked.
func TestLegacyPlayStillAcceptsLosslessTier(t *testing.T) {
	const rawSize = 1 << 20
	cleartext := makeTestFLAC(t, rawSize-len(knownDirectFLACTrailer), 44100, 16, 2, 213*time.Second)
	stream := append(append([]byte(nil), cleartext...), knownDirectFLACTrailer...)

	info, err := resolveLegacyTier(t, legacyTierTransport(t, "2000", stream))
	if err != nil {
		t.Fatalf("resolvePlayableLossless() = %v, want the 2000 tier accepted", err)
	}
	if info.Quality != platform.QualityLossless {
		t.Fatalf("quality = %v, want %v", info.Quality, platform.QualityLossless)
	}
}

// TestLegacyPlayTierValidationStaysStrict is the other half of the change: a
// declared tier must still match what the stream actually is. Widening the set
// of accepted tiers must not let a lesser stream through under a Hi-Res label.
func TestLegacyPlayTierValidationStaysStrict(t *testing.T) {
	const rawSize = 1 << 20
	for _, tt := range []struct {
		name       string
		declared   string
		sampleRate int
		bits       int
	}{
		{"4000 tier serving 48kHz", "4000", 48000, 24},
		{"4000 tier serving 16-bit", "4000", 96000, 16},
		{"2000 tier serving 96kHz", "2000", 96000, 24},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cleartext := makeTestFLAC(
				t, rawSize-len(knownDirectFLACTrailer),
				tt.sampleRate, tt.bits, 2, 213*time.Second,
			)
			stream := append(append([]byte(nil), cleartext...), knownDirectFLACTrailer...)
			if _, err := resolveLegacyTier(t, legacyTierTransport(t, tt.declared, stream)); err == nil {
				t.Fatal("resolvePlayableLossless() accepted a stream that does not match its declared tier")
			}
		})
	}
}

func TestLegacyLosslessProfileFor(t *testing.T) {
	for _, tt := range []struct {
		bitrate   int64
		wantOK    bool
		wantLevel string
	}{
		{directLosslessBitrate, true, directLosslessSelectorLevel},
		{directHiResBitrate, true, directHiResSelectorLevel},
		{3000, false, ""},
		{320, false, ""},
		{0, false, ""},
	} {
		profile, ok := legacyLosslessProfileFor(tt.bitrate)
		if ok != tt.wantOK {
			t.Errorf("legacyLosslessProfileFor(%d) ok = %v, want %v", tt.bitrate, ok, tt.wantOK)
			continue
		}
		if ok && profile.level != tt.wantLevel {
			t.Errorf("legacyLosslessProfileFor(%d) level = %q, want %q", tt.bitrate, profile.level, tt.wantLevel)
		}
	}
}
