package applemusic

import (
	"testing"

	"github.com/liuran001/MusicBot-Go/bot/platform"
)

// Apple Music caps concurrent streams per account, and both the FairPlay
// wrapper and the native AAC path acquire a licence against that same account.
// Gating only the wrapper tiers let an AAC download run alongside a wrapper one
// and trip the cap, so every tier must now be serialized — including the ones
// that never touch the wrapper, and including a client with no wrapper host.
func TestNeedsSerialDownloadCoversEveryQuality(t *testing.T) {
	qualities := []platform.Quality{
		platform.QualityStandard,
		platform.QualityHigh,
		platform.QualityHiRes,
		platform.QualityLossless,
	}

	for _, wrapperHost := range []string{"", "127.0.0.1"} {
		plat := NewPlatform(&Client{wrapperHost: wrapperHost})
		for _, quality := range qualities {
			if !plat.NeedsSerialDownload("1834068107", quality) {
				t.Fatalf("quality %v with wrapperHost=%q must be serialized", quality, wrapperHost)
			}
		}
	}
}

// A platform with no client cannot download at all, so it must not claim the
// shared serial gate and stall other requests behind it.
func TestNeedsSerialDownloadFalseWithoutClient(t *testing.T) {
	if NewPlatform(nil).NeedsSerialDownload("1834068107", platform.QualityLossless) {
		t.Fatal("a platform without a client must not take the serial gate")
	}
	var nilPlatform *AppleMusicPlatform
	if nilPlatform.NeedsSerialDownload("1834068107", platform.QualityLossless) {
		t.Fatal("a nil platform must not take the serial gate")
	}
}

// The gate is only consulted through the platform.SerialDownloadGate interface;
// a signature drift would silently un-serialize every Apple Music download.
func TestAppleMusicSatisfiesSerialDownloadGate(t *testing.T) {
	var plat any = NewPlatform(&Client{})
	if _, ok := plat.(platform.SerialDownloadGate); !ok {
		t.Fatal("AppleMusicPlatform must implement platform.SerialDownloadGate")
	}
}
