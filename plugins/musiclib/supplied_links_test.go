package musiclib

import "testing"

func TestSuppliedMiguShareRoutes(t *testing.T) {
	p := NewPlatform("migu", "", nil, 0)
	if id, ok := p.MatchURL("https://h5.nf.migu.cn/app/v4/p/share/song-new/index.html?id=600929000002612864"); !ok || id != "600929000002612864" {
		t.Fatalf("song = %q, %v", id, ok)
	}
	if id, ok := p.MatchArtistURL("https://h5.nf.migu.cn/app/v4/p/share/singer/index.html?id=1000000538"); !ok || id != "1000000538" {
		t.Fatalf("singer = %q, %v", id, ok)
	}
	if id, ok := p.MatchArtistURL("https://music.migu.cn/v5/#/singerDetail?id=112"); !ok || id != "112" {
		t.Fatalf("web singer = %q, %v", id, ok)
	}
	if id, ok := p.MatchPlaylistURL("https://h5.nf.migu.cn/app/v4/p/share/album/index.html?id=1142245189"); !ok || id != "album:1142245189" {
		t.Fatalf("album = %q, %v", id, ok)
	}
	if id, ok := p.MatchPlaylistURL("https://h5.nf.migu.cn/app/v4/p/share/playlist/index.html?id=231827572"); !ok || id != "231827572" {
		t.Fatalf("playlist = %q, %v", id, ok)
	}
	if got := p.ShortLinkHosts(); len(got) != 1 || got[0] != "c.migu.cn" {
		t.Fatalf("short hosts = %#v", got)
	}
}
