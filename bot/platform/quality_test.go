package platform

import "testing"

func TestQualityStableValuesAndStrings(t *testing.T) {
	tests := []struct {
		quality Quality
		value   int
		name    string
	}{
		{QualityStandard, 0, "standard"},
		{QualityHigh, 1, "high"},
		{QualityLossless, 2, "lossless"},
		{QualityHiRes, 3, "hires"},
		{QualityAtmos, 4, "atmos"},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if got := int(testCase.quality); got != testCase.value {
				t.Fatalf("numeric value = %d, want %d", got, testCase.value)
			}
			if got := testCase.quality.String(); got != testCase.name {
				t.Fatalf("String() = %q, want %q", got, testCase.name)
			}
			parsed, err := ParseQuality(testCase.name)
			if err != nil {
				t.Fatalf("ParseQuality(%q): %v", testCase.name, err)
			}
			if parsed != testCase.quality {
				t.Fatalf("ParseQuality(%q) = %v, want %v", testCase.name, parsed, testCase.quality)
			}
		})
	}
}

func TestQualityAtmosBitrate(t *testing.T) {
	if got := QualityAtmos.Bitrate(); got != 768 {
		t.Fatalf("QualityAtmos.Bitrate() = %d, want 768", got)
	}
}
