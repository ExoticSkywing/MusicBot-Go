package applemusic

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	mp4 "github.com/Eyevinn/mp4ff/mp4"
)

// buildFragmentedTestFile writes a fragmented MP4 (ftyp+moov[mvex] followed by
// repeating moof/mdat) to path — the same shape the Apple Music decrypt
// pipelines produce. It returns the sample payloads concatenated in decode
// order plus the per-sample sizes and durations, so tests can assert that a
// remux preserves them exactly.
//
// Sample sizes and durations deliberately vary so the rebuilt stsz (explicit
// per-sample sizes) and stts (run-length encoded durations) are exercised
// rather than collapsing to a single trivial run.
func buildFragmentedTestFile(t *testing.T, path string, nFrag, samplesPerFrag, baseSampleSize int) (wantData []byte, wantSizes, wantDurs []uint32) {
	t.Helper()

	const timescale = 44100
	init := mp4.CreateEmptyInit()
	init.AddEmptyTrack(timescale, "audio", "und")
	trak := init.Moov.Traks[0]
	if err := trak.SetAACDescriptor(2, timescale); err != nil {
		t.Fatalf("SetAACDescriptor: %v", err)
	}

	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer f.Close()

	if err := init.Encode(f); err != nil {
		t.Fatalf("encode init: %v", err)
	}

	var decodeTime uint64
	for i := range nFrag {
		frag, err := mp4.CreateFragment(uint32(i+1), trak.Tkhd.TrackID)
		if err != nil {
			t.Fatalf("CreateFragment: %v", err)
		}
		for j := range samplesPerFrag {
			size := baseSampleSize + j*3
			data := make([]byte, size)
			for k := range data {
				// Distinct, position-dependent bytes so a misordered or
				// truncated remux cannot accidentally compare equal.
				data[k] = byte((i*samplesPerFrag+j+k)%251 + 1)
			}
			dur := uint32(1024)
			if j == samplesPerFrag-1 {
				dur = 512 // break the run so stts must emit multiple entries
			}
			frag.AddFullSample(mp4.FullSample{
				Sample:     mp4.Sample{Dur: dur, Size: uint32(size)},
				DecodeTime: decodeTime,
				Data:       data,
			})
			decodeTime += uint64(dur)
			wantData = append(wantData, data...)
			wantSizes = append(wantSizes, uint32(size))
			wantDurs = append(wantDurs, dur)
		}
		seg := mp4.NewMediaSegmentWithoutStyp()
		seg.AddFragment(frag)
		if err := seg.Encode(f); err != nil {
			t.Fatalf("encode segment %d: %v", i, err)
		}
	}
	return wantData, wantSizes, wantDurs
}

// expandStts expands the run-length encoded stts into one duration per sample.
func expandStts(stts *mp4.SttsBox) []uint32 {
	var out []uint32
	for i, count := range stts.SampleCount {
		for range count {
			out = append(out, stts.SampleTimeDelta[i])
		}
	}
	return out
}

// TestRemuxToProgressive is the behavioural contract for remuxToProgressive:
// a fragmented input must become a progressive file whose sample table and
// media bytes are identical to the input's, with the fragment-only boxes gone.
//
// It asserts the media payload by reading the file at the offset stco actually
// points at, so a correct-looking sample table with a wrong chunk offset fails.
func TestRemuxToProgressive(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "track.m4a")
	wantData, wantSizes, wantDurs := buildFragmentedTestFile(t, path, 3, 5, 16)

	// Precondition: the fixture really is fragmented, otherwise remux would
	// early-return and the test would vacuously pass.
	in, err := os.Open(path)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	pre, err := mp4.DecodeFile(in)
	in.Close()
	if err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	if !pre.IsFragmented() {
		t.Fatalf("fixture is not fragmented; test would be vacuous")
	}

	if err := remuxToProgressive(context.Background(), path); err != nil {
		t.Fatalf("remuxToProgressive: %v", err)
	}

	out, err := os.Open(path)
	if err != nil {
		t.Fatalf("open remuxed: %v", err)
	}
	defer out.Close()
	got, err := mp4.DecodeFile(out)
	if err != nil {
		t.Fatalf("decode remuxed: %v", err)
	}

	if got.IsFragmented() {
		t.Errorf("remuxed file is still fragmented")
	}
	if got.Moov == nil {
		t.Fatalf("remuxed file has no moov")
	}
	if got.Moov.Mvex != nil {
		t.Errorf("mvex must be removed from a progressive file")
	}
	if len(got.Moov.Traks) != 1 {
		t.Fatalf("got %d traks, want 1", len(got.Moov.Traks))
	}
	trak := got.Moov.Traks[0]
	if trak.Edts != nil {
		t.Errorf("edts must be dropped (its zero SegmentDuration makes players report 0 length)")
	}

	stbl := trak.Mdia.Minf.Stbl

	// stsz: one explicit size per sample, in decode order.
	if stbl.Stsz == nil {
		t.Fatalf("no stsz in remuxed file")
	}
	if int(stbl.Stsz.SampleNumber) != len(wantSizes) {
		t.Errorf("stsz SampleNumber = %d, want %d", stbl.Stsz.SampleNumber, len(wantSizes))
	}
	if len(stbl.Stsz.SampleSize) != len(wantSizes) {
		t.Fatalf("stsz has %d sizes, want %d", len(stbl.Stsz.SampleSize), len(wantSizes))
	}
	for i, want := range wantSizes {
		if stbl.Stsz.SampleSize[i] != want {
			t.Errorf("stsz size[%d] = %d, want %d", i, stbl.Stsz.SampleSize[i], want)
		}
	}

	// stts: run-length encoded durations must expand back to the originals.
	if stbl.Stts == nil {
		t.Fatalf("no stts in remuxed file")
	}
	gotDurs := expandStts(stbl.Stts)
	if len(gotDurs) != len(wantDurs) {
		t.Fatalf("stts expands to %d samples, want %d", len(gotDurs), len(wantDurs))
	}
	for i, want := range wantDurs {
		if gotDurs[i] != want {
			t.Errorf("stts dur[%d] = %d, want %d", i, gotDurs[i], want)
		}
	}

	// mdhd duration must equal the summed sample durations, otherwise players
	// show the wrong length.
	var totalDur uint64
	for _, d := range wantDurs {
		totalDur += uint64(d)
	}
	if trak.Mdia.Mdhd.Duration != totalDur {
		t.Errorf("mdhd duration = %d, want %d", trak.Mdia.Mdhd.Duration, totalDur)
	}

	// stco must point at the real media bytes. Read straight from the file at
	// the advertised offset rather than trusting the parsed mdat, so a stale or
	// miscomputed chunk offset is caught.
	if stbl.Stco == nil || len(stbl.Stco.ChunkOffset) != 1 {
		t.Fatalf("expected exactly 1 stco chunk offset, got %+v", stbl.Stco)
	}
	offset := int64(stbl.Stco.ChunkOffset[0])
	payload := make([]byte, len(wantData))
	if _, err := out.ReadAt(payload, offset); err != nil {
		t.Fatalf("read %d bytes at stco offset %d: %v", len(wantData), offset, err)
	}
	if !bytes.Equal(payload, wantData) {
		t.Errorf("media bytes at stco offset differ from the source samples")
	}

	// stsc: a single chunk holding every sample.
	if stbl.Stsc == nil || len(stbl.Stsc.Entries) != 1 {
		t.Fatalf("expected exactly 1 stsc entry, got %+v", stbl.Stsc)
	}
	if n := stbl.Stsc.Entries[0].SamplesPerChunk; int(n) != len(wantSizes) {
		t.Errorf("stsc SamplesPerChunk = %d, want %d", n, len(wantSizes))
	}
}

// TestRemuxToProgressiveAlreadyProgressive verifies the no-op path: a file that
// is already progressive must be left byte-for-byte untouched.
func TestRemuxToProgressiveAlreadyProgressive(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "track.m4a")
	buildFragmentedTestFile(t, path, 2, 4, 16)

	if err := remuxToProgressive(context.Background(), path); err != nil {
		t.Fatalf("first remux: %v", err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after first remux: %v", err)
	}

	// Second pass sees a progressive file and must not touch it.
	if err := remuxToProgressive(context.Background(), path); err != nil {
		t.Fatalf("second remux: %v", err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after second remux: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Errorf("remuxing an already-progressive file changed it (%d -> %d bytes)", len(first), len(second))
	}
}

// TestRemuxMemoryFootprint pins down the property that motivated the streaming
// rewrite: remuxing must allocate an amount proportional to the sample *count*,
// not to the track size.
//
// The previous implementation decoded the whole file into memory and then
// concatenated every sample into one []byte, so it allocated roughly twice the
// file size (the parsed mdat plus the growing destination slice, which holds
// both the old and new backing arrays while it doubles). On a 1 GB VPS that is
// what pushed a lossless track past the container memory limit and got the
// process OOM-killed.
//
// A lossless Apple Music track is tens of megabytes; the fixture below is
// deliberately large enough that a non-streaming implementation cannot slip
// under the threshold.
func TestRemuxMemoryFootprint(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "large.m4a")
	// 8 fragments x 256 samples x ~8 KiB ≈ 17 MiB of media.
	buildFragmentedTestFile(t, path, 8, 256, 8192)

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat fixture: %v", err)
	}
	fileSize := info.Size()

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)

	if err := remuxToProgressive(context.Background(), path); err != nil {
		t.Fatalf("remuxToProgressive: %v", err)
	}

	runtime.ReadMemStats(&after)
	allocated := after.TotalAlloc - before.TotalAlloc

	pct := float64(allocated) / float64(fileSize) * 100
	t.Logf("file=%.1f MiB, allocated=%.1f MiB (%.1f%% of file size)",
		float64(fileSize)/(1<<20), float64(allocated)/(1<<20), pct)

	// Streaming should sit far below this; the bound is loose enough not to be
	// flaky but tight enough that reverting to a whole-file buffer (>=100%)
	// fails immediately.
	const maxRatio = 0.25
	if float64(allocated) > float64(fileSize)*maxRatio {
		t.Errorf("remux allocated %d bytes for a %d byte file (%.1f%%), want < %.0f%%; "+
			"this suggests the media is being buffered in memory rather than streamed",
			allocated, fileSize, pct, maxRatio*100)
	}
}
