package qqmusic

import "testing"

func TestLegacyAndToplistRoutes(t *testing.T) {
	p := NewURLMatcher()
	if id, ok := p.MatchURL("https://y.qq.com/n/yqq/song/002qU5aY3Qu24y.html"); !ok || id != "002qU5aY3Qu24y" {
		t.Fatalf("song = %q, %v", id, ok)
	}
	if id, ok := p.MatchPlaylistURL("https://y.qq.com/n/yqq/playlist/7256912512.html"); !ok || id != "7256912512" {
		t.Fatalf("playlist = %q, %v", id, ok)
	}
	if id, ok := p.MatchPlaylistURL("https://y.qq.com/n/ryqq_v2/toplist/26"); !ok || id != "top:26" {
		t.Fatalf("toplist = %q, %v", id, ok)
	}
}
