package handler

import (
	"context"
	"strings"
)

// isLyricResultAction reports whether a numbered result selects lyrics rather
// than sending the track. Empty and unknown actions keep the normal music
// behaviour used by existing search states.
func isLyricResultAction(action string) bool {
	return strings.EqualFold(strings.TrimSpace(action), "lyric")
}

// appendNumberedResultIntro writes the short instruction block shown before a
// numbered result list. The prefix selects the catalog shard (srch, guest or
// cb), while action keeps music and lyric flows honest about what a button
// does.
func appendNumberedResultIntro(builder *strings.Builder, ctx context.Context, prefix, action string) {
	if builder == nil {
		return
	}
	builder.WriteString(trMd(ctx, prefix+"_action_label"))
	builder.WriteString("\n")
	hintKey := prefix + "_download_hint"
	if isLyricResultAction(action) {
		hintKey = prefix + "_lyric_hint"
	}
	builder.WriteString(trMd(ctx, hintKey))
	builder.WriteString("\n\n")
}

// appendNumberedResultSourceHint keeps the source-link explanation directly
// above the song list, while the action instruction remains in the header.
func appendNumberedResultSourceHint(builder *strings.Builder, ctx context.Context, prefix string) {
	if builder == nil {
		return
	}
	builder.WriteString(trMd(ctx, prefix+"_source_hint"))
	builder.WriteString("\n")
}

// appendNumberedResultFooter writes the instruction immediately before the
// inline number buttons. It intentionally does not change the button labels or
// row layout, so the existing compact 8-result interaction is preserved.
func appendNumberedResultFooter(builder *strings.Builder, ctx context.Context, prefix, action string) {
	if builder == nil {
		return
	}
	footerKey := prefix + "_download_footer"
	buttonHintKey := prefix + "_download_button_hint"
	if isLyricResultAction(action) {
		footerKey = prefix + "_lyric_footer"
		buttonHintKey = prefix + "_lyric_button_hint"
	}
	builder.WriteString("\n")
	builder.WriteString(trMd(ctx, footerKey))
	builder.WriteString("\n")
	builder.WriteString(trMd(ctx, buttonHintKey))
	builder.WriteString("\n")
}
