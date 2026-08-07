package applemusic

import (
	"encoding/json"
	"testing"
)

func TestConvertSongAtmosAvailability(t *testing.T) {
	tests := []struct {
		name      string
		resource  string
		wantKnown bool
		want      bool
	}{
		{
			name:      "metadata omitted stays unknown",
			resource:  `{"id":"unknown","attributes":{}}`,
			wantKnown: false,
		},
		{
			name:      "audio variants advertise Atmos",
			resource:  `{"id":"variants-atmos","attributes":{"audioVariants":["lossless","DOLBY_ATMOS"]}}`,
			wantKnown: true,
			want:      true,
		},
		{
			name:      "audio variants explicitly omit Atmos",
			resource:  `{"id":"variants-stereo","attributes":{"audioVariants":["lossless","dolby-audio"]}}`,
			wantKnown: true,
			want:      false,
		},
		{
			name:      "legacy Atmos token is not an audio variant",
			resource:  `{"id":"variant-legacy-token","attributes":{"audioVariants":["atmos"],"audioTraits":["atmos"]}}`,
			wantKnown: true,
			want:      false,
		},
		{
			name:      "empty audio variants override legacy traits",
			resource:  `{"id":"empty-variants","attributes":{"audioVariants":[],"audioTraits":["atmos"]}}`,
			wantKnown: true,
			want:      false,
		},
		{
			name:      "audio variants override contradictory legacy traits",
			resource:  `{"id":"variants-win","attributes":{"audioVariants":["lossless"],"audioTraits":["atmos"]}}`,
			wantKnown: true,
			want:      false,
		},
		{
			name:      "legacy traits are fallback when variants omitted",
			resource:  `{"id":"traits-atmos","attributes":{"audioTraits":["Atmos"]}}`,
			wantKnown: true,
			want:      true,
		},
		{
			name:      "legacy traits can explicitly report no Atmos",
			resource:  `{"id":"traits-stereo","attributes":{"audioTraits":["lossless","spatial"]}}`,
			wantKnown: true,
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var resource appleMusicResource
			if err := json.Unmarshal([]byte(tt.resource), &resource); err != nil {
				t.Fatalf("unmarshal fixture: %v", err)
			}

			got := convertSong(resource).AtmosAvailable
			if !tt.wantKnown {
				if got != nil {
					t.Fatalf("AtmosAvailable = %v, want nil", *got)
				}
				return
			}
			if got == nil {
				t.Fatal("AtmosAvailable = nil, want catalog decision")
			}
			if *got != tt.want {
				t.Fatalf("AtmosAvailable = %v, want %v", *got, tt.want)
			}
		})
	}
}
