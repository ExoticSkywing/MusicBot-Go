package kuwo

import "strings"

// albumCollectionPrefix marks a playlist ID that actually addresses an album,
// so an album link can be expanded into its track list through the same
// playlist plumbing. Kuwo album and playlist IDs are both bare integers and
// would otherwise be indistinguishable.
const albumCollectionPrefix = "album:"

func encodeAlbumCollectionID(albumID string) string {
	albumID = strings.TrimSpace(albumID)
	if albumID == "" {
		return ""
	}
	return albumCollectionPrefix + albumID
}

// parseCollectionID splits a collection ID into its kind and bare upstream ID.
// Anything without a recognised prefix stays a playlist, preserving the IDs
// that were already in circulation before albums were supported.
func parseCollectionID(rawID string) (kind, id string) {
	rawID = strings.TrimSpace(rawID)
	if rawID == "" {
		return "", ""
	}
	if strings.HasPrefix(rawID, albumCollectionPrefix) {
		return "album", strings.TrimSpace(strings.TrimPrefix(rawID, albumCollectionPrefix))
	}
	return "playlist", rawID
}
