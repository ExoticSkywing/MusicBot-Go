package applemusic

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// mp4Box builds a minimal box with a 32-bit size header.
func mp4Box(typ string, payload []byte) []byte {
	b := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(b[0:4], uint32(8+len(payload)))
	copy(b[4:8], typ)
	copy(b[8:], payload)
	return b
}

// mp4LargeBox builds a box using the 64-bit largesize form (size field == 1).
func mp4LargeBox(typ string, payload []byte) []byte {
	b := make([]byte, 16+len(payload))
	binary.BigEndian.PutUint32(b[0:4], 1)
	copy(b[4:8], typ)
	binary.BigEndian.PutUint64(b[8:16], uint64(16+len(payload)))
	copy(b[16:], payload)
	return b
}

// TestReadMoovBox covers the top-level box walk that replaced reading the whole
// file: it must locate moov past a preceding box, handle both header forms, and
// reject a malformed size rather than allocating from it.
func TestReadMoovBox(t *testing.T) {
	moovPayload := []byte("moov-body-contents")

	t.Run("finds moov after ftyp", func(t *testing.T) {
		wantMoov := mp4Box("moov", moovPayload)
		file := mp4Box("ftyp", []byte("M4A isom"))
		file = append(file, wantMoov...)
		file = append(file, mp4Box("mdat", make([]byte, 4096))...)

		got, err := readMoovBox(bytes.NewReader(file), int64(len(file)))
		if err != nil {
			t.Fatalf("readMoovBox: %v", err)
		}
		if !bytes.Equal(got, wantMoov) {
			t.Errorf("moov mismatch: got %d bytes, want %d", len(got), len(wantMoov))
		}
	})

	t.Run("skips a large mdat without reading it", func(t *testing.T) {
		// mdat first and big: a correct walk seeks past it via the size field.
		wantMoov := mp4Box("moov", moovPayload)
		file := mp4Box("ftyp", []byte("M4A isom"))
		file = append(file, mp4Box("mdat", make([]byte, 1<<20))...)
		file = append(file, wantMoov...)

		got, err := readMoovBox(bytes.NewReader(file), int64(len(file)))
		if err != nil {
			t.Fatalf("readMoovBox: %v", err)
		}
		if !bytes.Equal(got, wantMoov) {
			t.Errorf("moov mismatch after large mdat")
		}
	})

	t.Run("handles 64-bit largesize boxes", func(t *testing.T) {
		wantMoov := mp4Box("moov", moovPayload)
		file := mp4LargeBox("mdat", make([]byte, 512))
		file = append(file, wantMoov...)

		got, err := readMoovBox(bytes.NewReader(file), int64(len(file)))
		if err != nil {
			t.Fatalf("readMoovBox: %v", err)
		}
		if !bytes.Equal(got, wantMoov) {
			t.Errorf("moov mismatch after largesize box")
		}
	})

	t.Run("returns nil when there is no moov", func(t *testing.T) {
		file := mp4Box("ftyp", []byte("M4A isom"))
		file = append(file, mp4Box("mdat", make([]byte, 64))...)

		got, err := readMoovBox(bytes.NewReader(file), int64(len(file)))
		if err != nil {
			t.Fatalf("readMoovBox: %v", err)
		}
		if got != nil {
			t.Errorf("got %d bytes, want nil for a file with no moov", len(got))
		}
	})

	t.Run("rejects a box size past EOF", func(t *testing.T) {
		// Declares 1 GiB inside a tiny file — must error, not try to allocate.
		file := make([]byte, 16)
		binary.BigEndian.PutUint32(file[0:4], 1<<30)
		copy(file[4:8], "mdat")

		if _, err := readMoovBox(bytes.NewReader(file), int64(len(file))); err == nil {
			t.Errorf("want error for a box extending past EOF, got nil")
		}
	})

	t.Run("rejects a size smaller than its header", func(t *testing.T) {
		file := make([]byte, 16)
		binary.BigEndian.PutUint32(file[0:4], 4) // < 8-byte header
		copy(file[4:8], "mdat")

		if _, err := readMoovBox(bytes.NewReader(file), int64(len(file))); err == nil {
			t.Errorf("want error for size smaller than the header, got nil")
		}
	})
}

// TestFixALACFileLeavesNonALACUntouched guards the rewrite of fixALACFile:
// it now writes back only patched packets instead of rewriting the file
// wholesale, so a file with no ALAC track must come out byte-identical.
func TestFixALACFileLeavesNonALACUntouched(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "aac.m4a")
	// An AAC (not ALAC) track, remuxed to progressive so it has a real moov
	// with sample tables — the shape fixALACFile sees in production.
	buildFragmentedTestFile(t, path, 2, 4, 32)
	if err := remuxToProgressive(context.Background(), path); err != nil {
		t.Fatalf("remuxToProgressive: %v", err)
	}

	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read before: %v", err)
	}

	if err := fixALACFile(path); err != nil {
		t.Fatalf("fixALACFile on a non-ALAC file: %v", err)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("fixALACFile modified a file with no ALAC track (%d -> %d bytes)",
			len(before), len(after))
	}
}

// TestFixALACFileRejectsNonMP4 verifies the error path when the input has no
// moov at all, which previously surfaced from findAlacTracks.
func TestFixALACFileRejectsNonMP4(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "garbage.m4a")
	if err := os.WriteFile(path, mp4Box("ftyp", []byte("M4A isom")), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	if err := fixALACFile(path); err == nil {
		t.Errorf("want an error for a file with no moov, got nil")
	}
}
