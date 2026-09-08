package model

import "testing"

func TestExplicitPreviewURLUsesExplicitSignals(t *testing.T) {
	for _, raw := range []string{
		"https://cdn.example/preview/song.mp3",
		"https://cdn.example/audition/song.mp3",
		"https://cdn.example/song.mp3?node_is_preview=true",
	} {
		if !ExplicitPreviewURL(raw) {
			t.Errorf("explicit preview URL accepted: %s", raw)
		}
	}
	for _, raw := range []string{
		"https://cdn.example/music/my-preview-song.mp3",
		"https://cdn.example/song.mp3?preview=false",
		"https://cdn.example/song.mp3?preview=0",
	} {
		if ExplicitPreviewURL(raw) {
			t.Errorf("ordinary full URL rejected: %s", raw)
		}
	}
}
