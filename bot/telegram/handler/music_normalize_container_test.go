package handler

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// isoBaseMediaBytes returns a buffer whose header identifies it as an ISO base
// media (MP4) container, which is all hasISOBaseMediaHeader inspects.
func isoBaseMediaBytes() []byte {
	return append([]byte{0x00, 0x00, 0x00, 0x20}, []byte("ftypisom....payload")...)
}

func stubNormalizeHelpers(t *testing.T, codec string, onExtract, onRemux func(src, dst string) error) {
	t.Helper()
	originalProbe := probeExtractedAudioCodec
	originalExtract := extractEmbeddedFLAC
	originalRemux := remuxExtractedAudioM4A
	t.Cleanup(func() {
		probeExtractedAudioCodec = originalProbe
		extractEmbeddedFLAC = originalExtract
		remuxExtractedAudioM4A = originalRemux
	})

	probeExtractedAudioCodec = func(string) (string, error) { return codec, nil }
	extractEmbeddedFLAC = func(_ context.Context, src, dst string) error {
		if onExtract == nil {
			return errors.New("unexpected flac extraction")
		}
		return onExtract(src, dst)
	}
	remuxExtractedAudioM4A = func(_ context.Context, src, dst string) error {
		if onRemux == nil {
			return errors.New("unexpected m4a remux")
		}
		return onRemux(src, dst)
	}
}

// TestNormalizeExtractedAudioPath_MP4NamedFlacIsConverted is the regression for
// the bilibili tagging failures. Its lossless audio is FLAC inside an MP4
// container (mimeType audio/mp4, codecs fLaC) but the plugin names the download
// .flac. Normalisation used to key off the extension, so such a file skipped
// conversion entirely and reached the tagger as an MP4 wearing a .flac name --
// TagLib picks its parser by extension, handed FLAC's parser MP4 bytes, and
// returned "can't save file" for all 117 of them.
func TestNormalizeExtractedAudioPath_MP4NamedFlacIsConverted(t *testing.T) {
	tmpDir := t.TempDir()
	srcPath := filepath.Join(tmpDir, "track.flac")
	if err := os.WriteFile(srcPath, isoBaseMediaBytes(), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	var extractCalls int
	stubNormalizeHelpers(t, "flac", func(src, dst string) error {
		extractCalls++
		if src != srcPath {
			t.Errorf("extract src = %q, want %q", src, srcPath)
		}
		if src == dst {
			t.Error("extraction would read and write the same path")
		}
		return os.WriteFile(dst, []byte("fLaC native"), 0o644)
	}, nil)

	gotPath, gotExt := normalizeExtractedAudioPath(srcPath, "flac")

	if extractCalls != 1 {
		t.Fatalf("extraction ran %d times, want 1", extractCalls)
	}
	if gotPath != srcPath {
		t.Fatalf("path = %q, want %q (the .flac name is already correct)", gotPath, srcPath)
	}
	if gotExt != "flac" {
		t.Fatalf("ext = %q, want flac", gotExt)
	}
	// The file must now hold the extracted native stream, not the MP4.
	content, err := os.ReadFile(gotPath)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if string(content) != "fLaC native" {
		t.Fatalf("result still holds the container: %q", content)
	}
	// No stray temporary file may survive.
	entries, _ := os.ReadDir(tmpDir)
	for _, entry := range entries {
		if entry.Name() != "track.flac" {
			t.Errorf("leftover file: %q", entry.Name())
		}
	}
}

// TestNormalizeExtractedAudioPath_NativeFlacUntouched guards the other half:
// a real FLAC must not be probed or rewritten, so the common case pays nothing.
func TestNormalizeExtractedAudioPath_NativeFlacUntouched(t *testing.T) {
	tmpDir := t.TempDir()
	srcPath := filepath.Join(tmpDir, "track.flac")
	native := append([]byte("fLaC"), make([]byte, 64)...)
	if err := os.WriteFile(srcPath, native, 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	probed := false
	originalProbe := probeExtractedAudioCodec
	t.Cleanup(func() { probeExtractedAudioCodec = originalProbe })
	probeExtractedAudioCodec = func(string) (string, error) {
		probed = true
		return "flac", nil
	}

	gotPath, gotExt := normalizeExtractedAudioPath(srcPath, "flac")
	if probed {
		t.Error("a native FLAC was handed to ffprobe; the header check should have short-circuited")
	}
	if gotPath != srcPath || gotExt != "flac" {
		t.Fatalf("normalize = (%q, %q), want (%q, flac)", gotPath, gotExt, srcPath)
	}
	content, _ := os.ReadFile(gotPath)
	if string(content[:4]) != "fLaC" {
		t.Fatal("native FLAC was rewritten")
	}
}

// TestNormalizeExtractedAudioPath_MP4NamedFlacFallsBackOnFailure keeps a failed
// conversion from destroying the download: the original must survive.
func TestNormalizeExtractedAudioPath_MP4NamedFlacFallsBackOnFailure(t *testing.T) {
	tmpDir := t.TempDir()
	srcPath := filepath.Join(tmpDir, "track.flac")
	if err := os.WriteFile(srcPath, isoBaseMediaBytes(), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	stubNormalizeHelpers(t, "flac", func(_, dst string) error {
		// Simulate ffmpeg writing a partial file before failing.
		_ = os.WriteFile(dst, []byte("partial"), 0o644)
		return errors.New("ffmpeg exploded")
	}, nil)

	gotPath, gotExt := normalizeExtractedAudioPath(srcPath, "flac")
	if gotPath != srcPath || gotExt != "flac" {
		t.Fatalf("normalize = (%q, %q), want the original back", gotPath, gotExt)
	}
	if _, err := os.Stat(srcPath); err != nil {
		t.Fatalf("original download was lost: %v", err)
	}
	entries, _ := os.ReadDir(tmpDir)
	for _, entry := range entries {
		if entry.Name() != "track.flac" {
			t.Errorf("partial output left behind: %q", entry.Name())
		}
	}
}

func TestHasISOBaseMediaHeader(t *testing.T) {
	dir := t.TempDir()
	for _, tt := range []struct {
		name    string
		content []byte
		want    bool
	}{
		{"mp4", isoBaseMediaBytes(), true},
		{"native flac", append([]byte("fLaC"), make([]byte, 32)...), false},
		{"mp3 id3", append([]byte("ID3\x04\x00\x00"), make([]byte, 32)...), false},
		{"too short", []byte("ftyp"), false},
		{"empty", nil, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(dir, tt.name)
			if err := os.WriteFile(path, tt.content, 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}
			if got := hasISOBaseMediaHeader(path); got != tt.want {
				t.Fatalf("hasISOBaseMediaHeader(%s) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
	if hasISOBaseMediaHeader(filepath.Join(dir, "missing")) {
		t.Error("a missing file reported as a container")
	}
}
