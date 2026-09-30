package migu

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/liuran001/MusicBot-Go/plugins/musiclib/internal/model"
)

func TestNewAndLegacyFormatsKeepBestRendition(t *testing.T) {
	var item MiguSongItem
	// Modern and legacy groups can coexist. The modern duplicate is
	// authoritative, and a larger MP3 must not displace a lossless FLAC.
	err := json.Unmarshal([]byte(`{
		"contentId":"123", "songName":"Song", "duration":200,
		"newRateFormats":[{"formatType":" sq ","resourceType":"2","size":"4000000"}],
		"rateFormats":[
			{"formatType":"SQ","resourceType":"2","size":"1000000"},
			{"formatType":"HQ","resourceType":"2","size":"8000000"}],
		"audioFormats":[{"formatType":"PQ","resourceType":"2","size":"3200000"}]
	}`), &item)
	if err != nil {
		t.Fatal(err)
	}
	song := (&Migu{}).convertItemToSong(item)
	if song == nil || song.ID != "123|2|SQ" || song.Ext != "flac" || song.Size != 4000000 || song.Bitrate != 160 {
		t.Fatalf("inconsistent selected rendition: %#v", song)
	}
	if got := len(collectMiguFormats(item)); got != 3 {
		t.Fatalf("merged formats=%d, want 3", got)
	}
}

func TestModernOnlyAndAudioFormatFallback(t *testing.T) {
	for _, tc := range []struct {
		name              string
		item              MiguSongItem
		wantTone, wantExt string
	}{
		{"modern only", MiguSongItem{NewRateFormats: []miguRateFormat{{FormatType: "ZQ32", ResourceType: "2"}}}, "ZQ32", "wav"},
		{"audio group improves legacy", MiguSongItem{
			RateFormats:  []miguRateFormat{{FormatType: "PQ", ResourceType: "2"}},
			AudioFormats: []miguRateFormat{{FormatType: "ZQ24", ResourceType: "2"}},
		}, "ZQ24", "flac"},
		{"encrypted does not hide plain", MiguSongItem{
			NewRateFormats: []miguRateFormat{{FormatType: "Z3D", ResourceType: "2", Size: "100000000"}},
			RateFormats:    []miguRateFormat{{FormatType: "PQ", ResourceType: "2", Size: "3200000"}},
		}, "PQ", "mp3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.item.ContentID = "123"
			song := (&Migu{}).convertItemToSong(tc.item)
			if song == nil || song.Extra["format_type"] != tc.wantTone || song.Ext != tc.wantExt {
				t.Fatalf("selected rendition: %#v", song)
			}
		})
	}
}

func TestDownloadInputValidationAndPlainID(t *testing.T) {
	if _, err := (&Migu{ctx: context.Background()}).GetDownloadURL(nil); err == nil {
		t.Fatal("nil song accepted")
	}
	for _, tc := range []struct {
		song                           *model.Song
		wantID, wantResource, wantTone string
	}{
		{&model.Song{ID: " 123 "}, "123", "", ""},
		{&model.Song{ID: "123", Extra: map[string]string{"resource_type": "2", "format_type": "HQ"}}, "123", "2", "HQ"},
		{&model.Song{ID: "123|2|PQ"}, "123", "2", "PQ"},
		{&model.Song{ID: "123|2"}, "", "", ""},
	} {
		id, resource, tone := miguSongParts(tc.song)
		if id != tc.wantID || resource != tc.wantResource || tone != tc.wantTone {
			t.Fatalf("parts=%q/%q/%q, want=%q/%q/%q", id, resource, tone, tc.wantID, tc.wantResource, tc.wantTone)
		}
	}
}
