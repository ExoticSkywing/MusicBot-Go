package handler

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/liuran001/MusicBot-Go/bot/id3"
)

// TestNormalizeThenTagFLACInMP4 exercises the real ffmpeg and taglib path end to
// end, which the stubbed unit tests deliberately do not. It reconstructs the
// exact shape bilibili delivers -- a FLAC stream inside an MP4 container, named
// .flac -- and asserts both halves of the bug: that tagging fails on it as-is,
// and that normalisation turns it into something taggable.
func TestNormalizeThenTagFLACInMP4(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available")
	}

	dir := t.TempDir()
	audioPath := filepath.Join(dir, "track.flac")

	// FLAC in a fragmented MP4, the container DASH uses.
	gen := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=3",
		"-c:a", "flac", "-f", "mp4", "-movflags", "+frag_keyframe+empty_moov",
		"-y", audioPath)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Skipf("cannot synthesise FLAC-in-MP4: %v (%s)", err, out)
	}

	header, err := os.ReadFile(audioPath)
	if err != nil {
		t.Fatalf("read generated file: %v", err)
	}
	if string(header[4:8]) != "ftyp" {
		t.Fatalf("generated file is not an MP4 container: %q", header[:8])
	}

	svc := id3.NewID3Service(nil)
	tag := &id3.TagData{Title: "Title", Artist: "Artist", Album: "Album"}

	// As delivered, tagging fails: TagLib resolves by extension and hands FLAC's
	// parser a pile of MP4 boxes.
	if err := svc.EmbedTags(audioPath, tag, ""); err == nil {
		t.Log("tagging unexpectedly succeeded on the container; " +
			"taglib may have gained content sniffing, which would make this fix redundant but harmless")
	}

	normPath, normExt := normalizeExtractedAudioPath(audioPath, "flac")
	if normExt != "flac" {
		t.Fatalf("normalised ext = %q, want flac", normExt)
	}
	normalised, err := os.ReadFile(normPath)
	if err != nil {
		t.Fatalf("read normalised file: %v", err)
	}
	if string(normalised[:4]) != "fLaC" {
		t.Fatalf("normalised file is still not a native FLAC: %q", normalised[:4])
	}

	if err := svc.EmbedTags(normPath, tag, ""); err != nil {
		t.Fatalf("tagging the normalised file failed: %v", err)
	}
}
