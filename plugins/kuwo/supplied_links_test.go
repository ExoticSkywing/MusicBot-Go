package kuwo

import "testing"

func TestSuppliedMobileShareRoutes(t *testing.T) {
	cases := []struct{ url, want string }{
		{"http://m.kuwo.cn/newh5/artist/artistDetail?id=233", "artist:233"},
		{"https://m.kuwo.cn/newh5app/singers/233?id=233", "artist:233"},
		{"https://m.kuwo.cn/?albumid=3985139&from=ar", "album:3985139"},
		{"https://m.kuwo.cn/newh5app/playlist_detail/3381506302", "playlist:3381506302"},
	}
	p := NewURLMatcher()
	for _, tc := range cases {
		t.Run(tc.url, func(t *testing.T) {
			if id, ok := p.MatchArtistURL(tc.url); ok && tc.want[:7] == "artist:" {
				if id != tc.want[7:] {
					t.Fatalf("artist = %q", id)
				}
				return
			}
			if id, ok := p.MatchPlaylistURL(tc.url); ok {
				if tc.want[:9] == "playlist:" && id == tc.want[9:] {
					return
				}
				if tc.want[:6] == "album:" && id == encodeAlbumCollectionID(tc.want[6:]) {
					return
				}
			}
			t.Fatalf("route %s not matched", tc.want)
		})
	}
}
