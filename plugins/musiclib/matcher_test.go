package musiclib

import (
	"strings"
	"testing"
)

func TestPlatformLinks(t *testing.T) {
	cases := []struct {
		name, link, id string
		collection     bool
	}{
		{"migu", "https://music.migu.cn/v3/music/song/600123?from=share", "600123", false},
		{"migu", "https://music.migu.cn/v5/music/album/123", "album:123", true},
		{"migu", "https://y.migu.cn/share?playlistId=123", "123", true},
		{"migu", "https://music.migu.cn/v5/#/playlist?playlistId=123&from=share", "123", true},
		{"migu", "https://music.migu.cn/v5/#/album?resourceId=123", "album:123", true},
		{"migu", "https://y.migu.cn/share?resourceType=2003&resourceId=123", "album:123", true},
		{"qianqian", "https://music.91q.com/song/T123", "T123", false},
		{"qianqian", "https://music.91q.com/album/P123", "album:P123", true},
		{"qianqian", "https://music.91q.com/songlist/123", "123", true},
		{"fivesing", "http://5sing.kugou.com/yc/123.html", "yc_123", false},
		{"fivesing", "https://5sing.kugou.com/fc/123.html", "fc_123", false},
		{"fivesing", "https://5sing.kugou.com/bz/123.html", "bz_123", false},
		{"fivesing", "http://5sing.kugou.com/456/dj/ABC123.html", "ABC123", true},
		{"jamendo", "https://www.jamendo.com/track/123/title", "123", false},
		{"jamendo", "https://jamendo.com/album/123/album-title", "album:123", true},
		{"jamendo", "https://www.jamendo.com/playlist/123", "123", true},
		{"joox", "https://www.joox.com/hk/single/abc+DEF123==", "abc+DEF123==", false},
		{"joox", "https://www.joox.com/sg/single/abc%2BDEF123%3D%3D", "abc+DEF123==", false},
		{"joox", "https://www.joox.com/share?songid=abc+DEF123==", "abc+DEF123==", false},
		{"joox", "https://www.joox.com/share?songid=abc%2BDEF123%3D%3D", "abc+DEF123==", false},
		{"joox", "https://www.joox.com/sg/album/abc+DEF123==", "album:abc+DEF123==", true},
		{"joox", "https://www.joox.com/hk/playlist/abc+DEF123==", "abc+DEF123==", true},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/"+tc.id, func(t *testing.T) {
			p := NewPlatform(tc.name, "", nil, 0)
			var id string
			var ok bool
			if tc.collection {
				id, ok = p.MatchPlaylistURL(tc.link)
				if wrongID, wrong := p.MatchURL(tc.link); wrong || wrongID != "" {
					t.Fatal("collection mistaken for track")
				}
			} else {
				id, ok = p.MatchURL(tc.link)
				if wrongID, wrong := p.MatchPlaylistURL(tc.link); wrong || wrongID != "" {
					t.Fatal("track mistaken for collection")
				}
			}
			if !ok || id != tc.id {
				t.Fatalf("%s -> %q, %t; want %q", tc.link, id, ok, tc.id)
			}
		})
	}
}

func TestRejectForeignAndMalformedLinks(t *testing.T) {
	for _, name := range []string{"migu", "qianqian", "fivesing", "jamendo", "joox"} {
		p := NewPlatform(name, "", nil, 0)
		id := "123"
		if name == "fivesing" {
			id = "yc_123"
		}
		link := p.trackURL(id)
		for _, bad := range []string{
			"123", "https://evil.example/" + link, "https://evil.example/?next=" + link,
			strings.Replace(link, ".com/", ".com.evil.example/", 1),
			strings.Replace(link, ".cn/", ".cn.evil.example/", 1),
			strings.Replace(link, "https://", "ftp://", 1),
			strings.Replace(link, "https://", "https://account@", 1),
		} {
			if bad == link {
				continue
			}
			if id, ok := p.MatchURL(bad); ok || id != "" {
				t.Errorf("%s accepted %s", name, bad)
			}
		}
	}
	p := NewPlatform("joox", "", nil, 0)
	for _, bad := range []string{"https://www.joox.com/hk/single/abc%2Fdef", "https://www.joox.com/hk/single/%zz", "https://www.joox.com/share?songid=abc&albumid=def", "https://www.joox.com/share?songid=abc%20DEF123==", "https://www.joox.com/hk/single/abc%20DEF123=="} {
		if _, ok := p.MatchURL(bad); ok {
			t.Errorf("accepted ambiguous JOOX link %s", bad)
		}
	}
	if _, ok := NewPlatform("migu", "", nil, 0).MatchPlaylistURL("https://y.migu.cn/share?resourceType=2&resourceId=123"); ok {
		t.Fatal("ambiguous Migu resource mistaken for album")
	}
}

func TestSourceIDAndCollectionRoundTrips(t *testing.T) {
	for _, tc := range []struct{ name, sourceID, want string }{
		{"migu", "123|2|PQ", "123"}, {"fivesing", "123|yc", "yc_123"}, {"joox", "abc+def==", "abc+def=="},
	} {
		p := NewPlatform(tc.name, "", nil, 0)
		got, ok := p.MatchURL(p.trackURL(tc.sourceID))
		if !ok || got != tc.want {
			t.Errorf("%s round-trip = %q, %t", tc.name, got, ok)
		}
	}
	for _, name := range []string{"migu", "qianqian", "fivesing", "jamendo", "joox"} {
		p := NewPlatform(name, "", nil, 0)
		for _, album := range []bool{false, true} {
			if name == "fivesing" && album {
				continue
			}
			want := "123"
			if album {
				want = "album:123"
			}
			got, ok := p.MatchPlaylistURL(p.collectionURL("123", album))
			if !ok || got != want {
				t.Errorf("%s collection round-trip = %q, %t", name, got, ok)
			}
		}
	}
}
