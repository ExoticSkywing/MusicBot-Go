package applemusic

import (
	"testing"

	"github.com/liuran001/MusicBot-Go/bot/platform"
)

// Real enhancedHls master playlist excerpt captured from the live API
// (track 1450695739, "bad guy"). Trimmed to the STREAM-INF section.
const testMasterPlaylist = `#EXTM3U
#EXT-X-VERSION:7
#EXT-X-INDEPENDENT-SEGMENTS
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="audio-alac-stereo-44100-24",AUTOSELECT=YES,CHANNELS="2",NAME="songEnhanced",SAMPLE-RATE=44100,BIT-DEPTH=24
#EXT-X-STREAM-INF:AVERAGE-BANDWIDTH=260342,_AVG-BANDWIDTH=260342,BANDWIDTH=274168,CODECS="mp4a.40.2",STABLE-VARIANT-ID="a",AUDIO="audio-stereo-256"
P1249856578_A1450695739_audio_en_gr256_mp4a-40-2.m3u8
#EXT-X-STREAM-INF:AVERAGE-BANDWIDTH=770225,_AVG-BANDWIDTH=770225,BANDWIDTH=771866,CODECS="ec-3",STABLE-VARIANT-ID="b",AUDIO="audio-atmos-2768"
P1249856578_A1450695739_audio_en_gr2768_mp4a-A6.m3u8
#EXT-X-STREAM-INF:AVERAGE-BANDWIDTH=1448364,_AVG-BANDWIDTH=1448364,BANDWIDTH=1554084,CODECS="alac",STABLE-VARIANT-ID="c",AUDIO="audio-alac-stereo-44100-24"
P1249856578_A1450695739_audio_en_gr2116_alac.m3u8
#EXT-X-STREAM-INF:AVERAGE-BANDWIDTH=132924,_AVG-BANDWIDTH=132924,BANDWIDTH=137748,CODECS="mp4a.40.2",STABLE-VARIANT-ID="d",AUDIO="audio-stereo-128"
P1249856578_A1450695739_audio_en_gr128_mp4a-40-2.m3u8
#EXT-X-STREAM-INF:AVERAGE-BANDWIDTH=74092,_AVG-BANDWIDTH=74092,BANDWIDTH=78264,CODECS="mp4a.40.5",STABLE-VARIANT-ID="e",AUDIO="audio-HE-stereo-64"
P1249856578_A1450695739_audio_en_gr64_mp4a-40-2.m3u8
`

func TestParseEnhancedHLSMaster(t *testing.T) {
	variants, err := parseEnhancedHLSMaster(testMasterPlaylist)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(variants) != 5 {
		t.Fatalf("expected 5 variants, got %d", len(variants))
	}
	// Sorted by AvgBW desc -> first must be the ALAC (1448364).
	if !variants[0].isALAC() {
		t.Errorf("expected ALAC first (highest bw), got codecs=%q", variants[0].Codecs)
	}
	if variants[0].SampleRate != 44100 || variants[0].BitDepth != 24 {
		t.Errorf("ALAC details wrong: rate=%d depth=%d", variants[0].SampleRate, variants[0].BitDepth)
	}
	if variants[0].Channels != "2" {
		t.Errorf("ALAC channels wrong: got %q want %q", variants[0].Channels, "2")
	}
	if variants[0].URI != "P1249856578_A1450695739_audio_en_gr2116_alac.m3u8" {
		t.Errorf("ALAC URI wrong: %q", variants[0].URI)
	}
	// Atmos second (770225).
	if !variants[1].isAtmos() {
		t.Errorf("expected Atmos second, got %q", variants[1].Codecs)
	}
}

func TestParseEnhancedHLSMasterPrefersMediaAttributes(t *testing.T) {
	master := `#EXTM3U
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="audio-alac-stereo-44100-16",CHANNELS="2",SAMPLE-RATE=96000,BIT-DEPTH=24
#EXT-X-STREAM-INF:AVERAGE-BANDWIDTH=2500000,BANDWIDTH=2600000,CODECS="alac",AUDIO="audio-alac-stereo-44100-16"
hires.m3u8
#EXT-X-STREAM-INF:AVERAGE-BANDWIDTH=1500000,BANDWIDTH=1600000,CODECS="alac",AUDIO="audio-alac-stereo-48000-24"
fallback.m3u8
`
	variants, err := parseEnhancedHLSMaster(master)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(variants) != 2 {
		t.Fatalf("expected 2 variants, got %d", len(variants))
	}

	if got := variants[0]; got.SampleRate != 96000 || got.BitDepth != 24 || got.Channels != "2" {
		t.Fatalf("EXT-X-MEDIA attributes not preferred: rate=%d depth=%d channels=%q", got.SampleRate, got.BitDepth, got.Channels)
	}
	if got := variants[1]; got.SampleRate != 48000 || got.BitDepth != 24 || got.Channels != "2" {
		t.Fatalf("group-id fallback not applied: rate=%d depth=%d channels=%q", got.SampleRate, got.BitDepth, got.Channels)
	}
}

func TestParseEnhancedHLSMasterPreservesJOCChannels(t *testing.T) {
	master := `#EXTM3U
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="opaque-surround",CHANNELS="16/JOC",SAMPLE-RATE=48000
#EXT-X-STREAM-INF:AVERAGE-BANDWIDTH=768000,BANDWIDTH=800000,CODECS="ec-3",AUDIO="opaque-surround"
atmos.m3u8
`
	variants, err := parseEnhancedHLSMaster(master)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(variants) != 1 {
		t.Fatalf("expected 1 variant, got %d", len(variants))
	}
	if variants[0].Channels != "16/JOC" || !variants[0].isAtmos() {
		t.Fatalf("JOC metadata not preserved: channels=%q codecs=%q", variants[0].Channels, variants[0].Codecs)
	}
}

func TestSelectVariantForQuality(t *testing.T) {
	variants, err := parseEnhancedHLSMaster(testMasterPlaylist)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	tests := []struct {
		name        string
		quality     platform.Quality
		wantOK      bool
		wantCodec   string
		wantURIsub  string
		wantQuality platform.Quality
	}{
		{"hires falls back on lossless-only master", platform.QualityHiRes, true, "alac", "alac", platform.QualityLossless},
		{"lossless->alac", platform.QualityLossless, true, "alac", "alac", platform.QualityLossless},
		{"high->aac256", platform.QualityHigh, true, "mp4a.40.2", "gr256", platform.QualityHigh},
		{"standard->aac128", platform.QualityStandard, true, "mp4a.40.2", "gr128", platform.QualityStandard},
		{"atmos-explicit", platform.QualityAtmos, true, "ec-3", "gr2768", platform.QualityAtmos},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v, ok := selectVariantForQuality(variants, tc.quality)
			if ok != tc.wantOK {
				t.Fatalf("selected=%v, want %v (variant=%+v)", ok, tc.wantOK, v)
			}
			if !ok {
				return
			}
			if v.Codecs != tc.wantCodec {
				t.Errorf("codec: got %q want %q", v.Codecs, tc.wantCodec)
			}
			if tc.wantURIsub != "" && !contains(v.URI, tc.wantURIsub) {
				t.Errorf("URI %q does not contain %q", v.URI, tc.wantURIsub)
			}
			if got := variantQuality(v); got != tc.wantQuality {
				t.Errorf("resolved quality: got %v want %v", got, tc.wantQuality)
			}
		})
	}
}

func TestSelectVariantALACBoundaries(t *testing.T) {
	variants := []enhancedHLSVariant{
		{Codecs: "alac", SampleRate: 44100, BitDepth: 24, AvgBW: 1400000, URI: "lossless-44.m3u8"},
		{Codecs: "alac", SampleRate: 48000, BitDepth: 24, AvgBW: 1600000, URI: "lossless-48.m3u8"},
		{Codecs: "alac", SampleRate: 48001, BitDepth: 16, AvgBW: 1700000, URI: "hires-boundary.m3u8"},
		{Codecs: "alac", SampleRate: 96000, BitDepth: 24, AvgBW: 2800000, URI: "hires-96.m3u8"},
		{Codecs: "mp4a.40.2", AvgBW: 256000, URI: "aac.m3u8"},
	}

	lossless, ok := selectVariantForQuality(variants, platform.QualityLossless)
	if !ok || lossless.URI != "lossless-48.m3u8" {
		t.Fatalf("lossless selected %+v, want 48k ALAC", lossless)
	}
	hires, ok := selectVariantForQuality(variants, platform.QualityHiRes)
	if !ok || hires.URI != "hires-96.m3u8" {
		t.Fatalf("hires selected %+v, want 96k ALAC", hires)
	}
}

func TestSelectVariantEnhancedTierFallbacks(t *testing.T) {
	t.Run("hires falls back to lossless but not AAC", func(t *testing.T) {
		variants := []enhancedHLSVariant{
			{Codecs: "alac", SampleRate: 48000, BitDepth: 24, AvgBW: 1600000, URI: "lossless.m3u8"},
			{Codecs: "mp4a.40.2", AvgBW: 256000, URI: "aac.m3u8"},
		}
		got, ok := selectVariantForQuality(variants, platform.QualityHiRes)
		if !ok || got.URI != "lossless.m3u8" {
			t.Fatalf("Hi-Res fallback = %+v, %v; want lossless ALAC", got, ok)
		}

		aacOnly := []enhancedHLSVariant{
			{Codecs: "mp4a.40.2", AvgBW: 256000, URI: "aac.m3u8"},
		}
		if got, ok := selectVariantForQuality(aacOnly, platform.QualityHiRes); ok {
			t.Fatalf("unexpected Hi-Res AAC fallback: %+v", got)
		}
	})

	t.Run("lossless rejects hires and AAC", func(t *testing.T) {
		variants := []enhancedHLSVariant{
			{Codecs: "alac", SampleRate: 96000, BitDepth: 24, AvgBW: 2800000, URI: "hires.m3u8"},
			{Codecs: "mp4a.40.2", AvgBW: 256000, URI: "aac.m3u8"},
		}
		if got, ok := selectVariantForQuality(variants, platform.QualityLossless); ok {
			t.Fatalf("unexpected lossless fallback: %+v", got)
		}
	})

	t.Run("atmos never falls back", func(t *testing.T) {
		variants := []enhancedHLSVariant{
			{Codecs: "ec-3", Channels: "6", AvgBW: 768000, URI: "dolby-audio.m3u8"},
			{Codecs: "alac", SampleRate: 96000, BitDepth: 24, AvgBW: 2800000, URI: "hires.m3u8"},
			{Codecs: "mp4a.40.2", AvgBW: 256000, URI: "aac.m3u8"},
		}
		if got, ok := selectVariantForQuality(variants, platform.QualityAtmos); ok {
			t.Fatalf("unexpected Atmos fallback: %+v", got)
		}
	})

	t.Run("atmos skips plain Dolby Audio", func(t *testing.T) {
		variants := []enhancedHLSVariant{
			{Codecs: "ec-3", Channels: "6", AvgBW: 900000, URI: "dolby-audio.m3u8"},
			{Codecs: "ec-3", Channels: "16/JOC", AvgBW: 768000, URI: "atmos.m3u8"},
		}
		got, ok := selectVariantForQuality(variants, platform.QualityAtmos)
		if !ok || got.URI != "atmos.m3u8" {
			t.Fatalf("got %+v, want JOC Atmos variant", got)
		}
	})
}

func TestVariantQualityBoundaries(t *testing.T) {
	tests := []struct {
		name string
		v    enhancedHLSVariant
		want platform.Quality
	}{
		{"24-bit 44.1k remains lossless", enhancedHLSVariant{Codecs: "alac", SampleRate: 44100, BitDepth: 24}, platform.QualityLossless},
		{"48k remains lossless", enhancedHLSVariant{Codecs: "alac", SampleRate: 48000, BitDepth: 24}, platform.QualityLossless},
		{"above 48k is hires", enhancedHLSVariant{Codecs: "alac", SampleRate: 48001, BitDepth: 16}, platform.QualityHiRes},
		{"plain EC-3 is Dolby Audio", enhancedHLSVariant{Codecs: "ec-3", Channels: "6"}, platform.QualityHigh},
		{"JOC is Atmos", enhancedHLSVariant{Codecs: "ec-3", Channels: "16/JOC"}, platform.QualityAtmos},
		{"ec+3 is Atmos", enhancedHLSVariant{Codecs: "ec+3", Channels: "6"}, platform.QualityAtmos},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := variantQuality(tc.v); got != tc.want {
				t.Fatalf("variantQuality(%+v)=%v, want %v", tc.v, got, tc.want)
			}
		})
	}
}

// Enhanced-quality requests are strict: an AAC-only master is not suitable.
func TestSelectEnhancedQualityRejectsAACOnlyMaster(t *testing.T) {
	aacOnly := `#EXTM3U
#EXT-X-STREAM-INF:AVERAGE-BANDWIDTH=260342,BANDWIDTH=274168,CODECS="mp4a.40.2",AUDIO="audio-stereo-256"
a.m3u8
#EXT-X-STREAM-INF:AVERAGE-BANDWIDTH=132924,BANDWIDTH=137748,CODECS="mp4a.40.2",AUDIO="audio-stereo-128"
b.m3u8
`
	variants, err := parseEnhancedHLSMaster(aacOnly)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, quality := range []platform.Quality{platform.QualityLossless, platform.QualityHiRes, platform.QualityAtmos} {
		if v, ok := selectVariantForQuality(variants, quality); ok {
			t.Errorf("quality %v unexpectedly selected AAC variant %+v", quality, v)
		}
	}
}
