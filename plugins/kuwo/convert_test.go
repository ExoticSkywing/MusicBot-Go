package kuwo

import (
	"encoding/json"
	"testing"
)

// wireFromJSON decodes an upstream payload fragment exactly as the client does,
// so the fixtures below stay faithful to the live field casing.
func wireFromJSON(t *testing.T, payload string) trackWire {
	t.Helper()
	var wire trackWire
	if err := json.Unmarshal([]byte(payload), &wire); err != nil {
		t.Fatalf("unmarshal %s: %v", payload, err)
	}
	return wire
}

// TestConvertTrackLinksArtistsAndAlbum covers the three shapes the live API
// returns: search results (upper-case keys plus allartistid), and detail /
// playlist results (lower-case keys with only the lead artistid).
func TestConvertTrackLinksArtistsAndAlbum(t *testing.T) {
	tests := []struct {
		name     string
		payload  string
		artists  []platform2Artist
		albumID  string
		albumURL string
	}{
		{
			name: "search pairs every artist with allartistid",
			// Live shape: /search/searchMusicBykeyWord abslist entry.
			payload: `{"MUSICRID":"MUSIC_1234","NAME":"因为爱情","ARTIST":"陈奕迅&王菲","ARTISTID":"47","allartistid":"47&385","ALBUM":"Stranger Under My Skin","ALBUMID":"61488"}`,
			artists: []platform2Artist{
				{"陈奕迅", "47", "https://www.kuwo.cn/singer_detail/47"},
				{"王菲", "385", "https://www.kuwo.cn/singer_detail/385"},
			},
			albumID:  "61488",
			albumURL: "https://www.kuwo.cn/album_detail/61488",
		},
		{
			name: "detail links the single artist from numeric artistid",
			// Live shape: /api/www/music/musicInfo data object.
			payload: `{"rid":228908,"name":"晴天","artist":"周杰伦","artistid":336,"album":"叶惠美","albumid":1293}`,
			artists: []platform2Artist{
				{"周杰伦", "336", "https://www.kuwo.cn/singer_detail/336"},
			},
			albumID:  "1293",
			albumURL: "https://www.kuwo.cn/album_detail/1293",
		},
		{
			name:    "multiple artists without allartistid link only the lead",
			payload: `{"rid":1,"name":"Song","artist":"Alice&Bob","artistid":47,"album":"Album","albumid":9}`,
			artists: []platform2Artist{
				{"Alice", "47", "https://www.kuwo.cn/singer_detail/47"},
				{"Bob", "", ""},
			},
			albumID:  "9",
			albumURL: "https://www.kuwo.cn/album_detail/9",
		},
		{
			name:    "mismatched allartistid length falls back to the lead only",
			payload: `{"rid":1,"name":"Song","artist":"Alice&Bob&Carol","artistid":47,"allartistid":"47&385","album":"Album","albumid":9}`,
			artists: []platform2Artist{
				{"Alice", "47", "https://www.kuwo.cn/singer_detail/47"},
				{"Bob", "", ""},
				{"Carol", "", ""},
			},
			albumID:  "9",
			albumURL: "https://www.kuwo.cn/album_detail/9",
		},
		{
			name:    "zero albumid keeps the title but stays unlinked",
			payload: `{"rid":1168366,"name":"絵空事","artist":"nano.RIPE","artistid":10016,"album":"Some Album","albumid":0}`,
			artists: []platform2Artist{
				{"nano.RIPE", "10016", "https://www.kuwo.cn/singer_detail/10016"},
			},
			albumID:  "",
			albumURL: "",
		},
		{
			name:    "non-numeric ids are rejected rather than linked",
			payload: `{"rid":1,"name":"Song","artist":"Alice","artistid":"../evil","album":"Album","albumid":"x1"}`,
			artists: []platform2Artist{
				{"Alice", "", ""},
			},
			albumID:  "",
			albumURL: "",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			detail, _, ok := convertTrack(wireFromJSON(t, test.payload))
			if !ok {
				t.Fatalf("convertTrack() rejected %s", test.payload)
			}
			track := detail.Track
			if len(track.Artists) != len(test.artists) {
				t.Fatalf("Artists = %#v, want %d entries", track.Artists, len(test.artists))
			}
			for i, want := range test.artists {
				got := track.Artists[i]
				if got.Name != want.name || got.ID != want.id || got.URL != want.url {
					t.Errorf("Artists[%d] = {Name:%q ID:%q URL:%q}, want {Name:%q ID:%q URL:%q}",
						i, got.Name, got.ID, got.URL, want.name, want.id, want.url)
				}
				if got.Platform != "kuwo" {
					t.Errorf("Artists[%d].Platform = %q, want kuwo", i, got.Platform)
				}
			}
			if track.Album == nil {
				t.Fatalf("Album = nil, want a populated album")
			}
			if track.Album.ID != test.albumID || track.Album.URL != test.albumURL {
				t.Errorf("Album = {ID:%q URL:%q}, want {ID:%q URL:%q}",
					track.Album.ID, track.Album.URL, test.albumID, test.albumURL)
			}
		})
	}
}

// platform2Artist is the expected artist projection for the table above.
type platform2Artist struct {
	name string
	id   string
	url  string
}

// TestConvertTrackOmitsAlbumWhenUpstreamHasNoTitle guards the existing contract
// that a missing album name yields no album rather than a bare link.
func TestConvertTrackOmitsAlbumWhenUpstreamHasNoTitle(t *testing.T) {
	detail, _, ok := convertTrack(wireFromJSON(t, `{"rid":1,"name":"Song","artist":"Alice","artistid":47,"album":"","albumid":9}`))
	if !ok {
		t.Fatal("convertTrack() rejected a titleless-album payload")
	}
	if detail.Track.Album != nil {
		t.Fatalf("Album = %#v, want nil", detail.Track.Album)
	}
}

func TestNormalizeEntityIDRejectsUnlinkableValues(t *testing.T) {
	for _, value := range []string{"", " ", "0", "00", "x", "1a", "-1", "1.5", "../7", "9999999999999999999999"} {
		if got := normalizeEntityID(value); got != "" {
			t.Errorf("normalizeEntityID(%q) = %q, want empty", value, got)
		}
	}
	for _, value := range []string{"1", "336", " 61488 "} {
		if got := normalizeEntityID(value); got == "" {
			t.Errorf("normalizeEntityID(%q) = empty, want the id preserved", value)
		}
	}
}
