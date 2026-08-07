package handler

import (
	"testing"

	botpkg "github.com/liuran001/MusicBot-Go/bot"
)

func TestClassifyPreparedAppleMusicQuality(t *testing.T) {
	tests := []struct {
		name    string
		probe   preparedAudioStreamProbe
		current string
		want    string
		ok      bool
	}{
		{name: "CD lossless", probe: preparedAudioStreamProbe{Codec: "alac", SampleRate: 44100, BitsPerRaw: 16}, want: "lossless", ok: true},
		{name: "24 bit 44.1 remains lossless", probe: preparedAudioStreamProbe{Codec: "alac", SampleRate: 44100, BitsPerRaw: 24}, want: "lossless", ok: true},
		{name: "24 bit 48 remains lossless", probe: preparedAudioStreamProbe{Codec: "alac", SampleRate: 48000, BitsPerRaw: 24}, want: "lossless", ok: true},
		{name: "24 bit 96 is hires", probe: preparedAudioStreamProbe{Codec: "alac", SampleRate: 96000, BitsPerRaw: 24}, want: "hires", ok: true},
		{name: "selected JOC verifies Atmos", probe: preparedAudioStreamProbe{Codec: "eac3", SampleRate: 48000}, current: "atmos", want: "atmos", ok: true},
		{name: "plain eac3 is not assumed Atmos", probe: preparedAudioStreamProbe{Codec: "eac3", SampleRate: 48000}, current: "hires", ok: false},
		{name: "missing ALAC rate is unverifiable", probe: preparedAudioStreamProbe{Codec: "alac"}, ok: false},
		{name: "AAC keeps selected tier", probe: preparedAudioStreamProbe{Codec: "aac", SampleRate: 44100}, current: "high", ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := classifyPreparedAppleMusicQuality(tt.probe, tt.current)
			if ok != tt.ok || got != tt.want {
				t.Fatalf("classifyPreparedAppleMusicQuality(%+v, %q) = (%q, %v), want (%q, %v)", tt.probe, tt.current, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestIsReusableCachedSongInvalidatesLegacyAppleEnhancedCache(t *testing.T) {
	legacy := &botpkg.SongInfo{Platform: "applemusic", Quality: "hires", QualityVerified: true}
	if isReusableCachedSong(legacy, "applemusic", "hires") {
		t.Fatal("legacy Apple Music enhanced cache should be bypassed")
	}

	current := &botpkg.SongInfo{
		Platform:        "applemusic",
		Quality:         "hires",
		QualityVerified: true,
		QualityRevision: botpkg.AppleMusicQualityRevision,
	}
	if !isReusableCachedSong(current, "applemusic", "hires") {
		t.Fatal("current verified Apple Music cache should be reusable")
	}

	if !isReusableCachedSong(legacy, "netease", "hires") {
		t.Fatal("Apple Music classifier revision must not invalidate other platforms")
	}
	if !isReusableCachedSong(legacy, "applemusic", "high") {
		t.Fatal("Apple Music AAC cache should not require enhanced classifier revision")
	}
}
