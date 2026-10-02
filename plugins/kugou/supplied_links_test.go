package kugou

import "testing"

func TestSuppliedShareQueryRoutes(t *testing.T) {
	p := NewURLMatcher()
	if id, ok := p.MatchArtistURL("https://activity.kugou.com/share/index.html?share_type=singer&singerId=83837"); !ok || id != "83837" {
		t.Fatalf("singerId = %q, %v", id, ok)
	}
	if id, ok := p.MatchPlaylistURL("http://www.kugou.com/share/c_58miXwKVmklD.html?id=c_58miXwKVmklD"); !ok || id != "album:c_58miXwKVmklD" {
		t.Fatalf("album share = %q, %v", id, ok)
	}
}
