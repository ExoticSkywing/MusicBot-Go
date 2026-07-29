package kuwo

import "testing"

func TestURLMatcher(t *testing.T) {
	m := NewURLMatcher()
	tests := []struct {
		input, want string
		ok          bool
	}{
		{"https://www.kuwo.cn/play_detail/41378936", "41378936", true},
		{"https://WWW.KUWO.CN./play_detail/41378936", "41378936", true},
		{"https://m.kuwo.cn/newh5/singles/songinfoandlrc?musicId=41378936", "41378936", true},
		{"https://www.kuwo.cn.evil.example/play_detail/41378936", "", false},
		{"https://www.kuwo.cn/play_detail/not-a-track", "", false},
	}
	for _, tt := range tests {
		got, ok := m.MatchURL(tt.input)
		if got != tt.want || ok != tt.ok {
			t.Errorf("MatchURL(%q) = %q, %v; want %q, %v", tt.input, got, ok, tt.want, tt.ok)
		}
	}
}

func TestPlaylistAndTextMatcher(t *testing.T) {
	m := NewURLMatcher()
	for input, want := range map[string]string{
		"https://www.kuwo.cn/playlist_detail/2952464073":                   "2952464073",
		"https://m.kuwo.cn/h5app/playlist/2952464073":                      "2952464073",
		"https://www.kuwo.cn/web/inventory/share?pid=2952464073&type=2016": "2952464073",
	} {
		if got, ok := m.MatchPlaylistURL(input); !ok || got != want {
			t.Errorf("MatchPlaylistURL(%q) = %q, %v", input, got, ok)
		}
	}
	text := NewTextMatcher()
	if got, ok := text.MatchText("酷我:41378936"); !ok || got != "41378936" {
		t.Fatalf("prefixed text = %q, %v", got, ok)
	}
	if _, ok := text.MatchText("41378936"); ok {
		t.Fatal("bare numeric ID must not match")
	}
	if got, ok := text.MatchText("分享 https://www.kuwo.cn/play_detail/41378936"); !ok || got != "41378936" {
		t.Fatalf("embedded URL = %q, %v", got, ok)
	}
}
