package handler

import (
	"testing"

	"github.com/liuran001/MusicBot-Go/bot/platform"
)

func TestInlineSearchCacheKeySeparatesScopes(t *testing.T) {
	base := inlineSearchCacheKey("qqmusic", "netease", "melody", true)

	for _, tt := range []struct {
		name string
		key  string
	}{
		{"platform", inlineSearchCacheKey("kugou", "netease", "melody", true)},
		{"fallback", inlineSearchCacheKey("qqmusic", "kugou", "melody", true)},
		{"keyword", inlineSearchCacheKey("qqmusic", "netease", "other", true)},
		{"bilibili filter", inlineSearchCacheKey("qqmusic", "netease", "melody", false)},
	} {
		if tt.key == base {
			t.Errorf("%s does not affect the cache key: both are %q", tt.name, base)
		}
	}

	// The separator must not let two different tuples collide.
	if inlineSearchCacheKey("a", "b", "c", true) == inlineSearchCacheKey("a", "bc", "", true) {
		t.Error("cache key is ambiguous across field boundaries")
	}
}

// TestInlineSearchCacheClonesTracks guards the cache against a caller that
// reslices or reorders the returned batch: the stored copy must stay intact.
func TestInlineSearchCacheClonesTracks(t *testing.T) {
	key := inlineSearchCacheKey("test", "", "clone", true)
	inlineSearchCache.Delete(key)

	original := []platform.Track{{ID: "1"}, {ID: "2"}, {ID: "3"}}
	inlineSearchCache.Store(key, inlineSearchResult{tracks: original, platformName: "test"})

	cached, ok := inlineSearchCache.Load(key)
	if !ok {
		t.Fatal("stored entry not readable")
	}
	// Simulate a consumer mutating what it was handed.
	handed := cloneTracksForTest(cached.tracks)
	handed[0].ID = "mutated"

	again, ok := inlineSearchCache.Load(key)
	if !ok {
		t.Fatal("entry vanished")
	}
	if again.tracks[0].ID != "1" {
		t.Fatalf("cached track was mutated through the returned slice: got %q, want %q", again.tracks[0].ID, "1")
	}
	inlineSearchCache.Delete(key)
}

func cloneTracksForTest(in []platform.Track) []platform.Track {
	out := make([]platform.Track, len(in))
	copy(out, in)
	return out
}
