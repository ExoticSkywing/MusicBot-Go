package id3

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"go.senan.xyz/taglib"
)

// RewriteLocalizedNames updates only the localized display-name tags in an
// existing audio file. Passing an empty name leaves that tag unchanged.
func (s *ID3Service) RewriteLocalizedNames(audioPath, title, artist, album string) error {
	if !isSupportedTagExtension(strings.ToLower(filepath.Ext(audioPath))) {
		return errors.New("unsupported audio format for tags")
	}

	tags := make(map[string][]string, 3)
	addTaglibValue(tags, taglib.Title, title)
	addTaglibValue(tags, taglib.Artist, artist)
	addTaglibValue(tags, taglib.Album, album)
	if len(tags) == 0 {
		return nil
	}

	// A zero WriteOption performs a partial update. In particular, do not use
	// taglib.Clear here: it would remove lyrics, artwork-adjacent metadata and
	// every other cached tag not present in this small map.
	if err := taglib.WriteTags(audioPath, tags, 0); err != nil {
		return fmt.Errorf("rewrite localized audio tags: %w", err)
	}
	written, err := taglib.ReadTags(audioPath)
	if err != nil {
		return fmt.Errorf("verify localized audio tags: %w", err)
	}
	for key, want := range tags {
		if !slices.Equal(written[key], want) {
			return fmt.Errorf("verify localized audio tag %s: got %q, want %q", key, written[key], want)
		}
	}
	return nil
}
