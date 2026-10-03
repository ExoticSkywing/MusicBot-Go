package kuwo

import "github.com/liuran001/MusicBot-Go/bot/platform"

// Kuwo publishes each track at a small set of tiers. These describe them and
// the STREAMINFO each one must actually deliver, which is what keeps a lesser
// stream from being accepted under a better tier's label.
const (
	directHiResBitrate          = int64(4000)
	directLosslessBitrate       = int64(2000)
	directHiResSelectorLevel    = "hires"
	directLosslessSelectorLevel = "lossless"
)

type directQualityResolverProfile struct {
	level   string
	bitrate int64
	format  string
}

// verifiedQuality is used only after acceptsProbe validates the actual audio.
// Kuwo's 2000 tier includes 24-bit / 44.1 and 48 kHz FLAC; its product label
// remains lossless rather than the separate 4000 Hi-Res tier.
func (profile directQualityResolverProfile) verifiedQuality() platform.Quality {
	if profile.level == directHiResSelectorLevel {
		return platform.QualityHiRes
	}
	return platform.QualityLossless
}

// acceptsProbe reports whether a probed stream matches this tier.
func (profile directQualityResolverProfile) acceptsProbe(probe mediaProbe) bool {
	switch profile.level {
	case directHiResSelectorLevel:
		return probe.format == "flac" &&
			probe.channels == 2 &&
			probe.sampleRate >= 96000 &&
			probe.bitsPerSample >= 24 &&
			probe.quality == platform.QualityHiRes
	case directLosslessSelectorLevel:
		if probe.format != "flac" ||
			probe.channels != 2 ||
			(probe.sampleRate != 44100 && probe.sampleRate != 48000) {
			return false
		}
		return (probe.bitsPerSample == 16 &&
			probe.quality == platform.QualityLossless) ||
			(probe.bitsPerSample == 24 &&
				probe.quality == platform.QualityHiRes)
	default:
		return false
	}
}
