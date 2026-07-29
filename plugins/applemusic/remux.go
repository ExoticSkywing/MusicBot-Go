package applemusic

import (
	"context"
	"fmt"
	"io"
	"os"

	mp4 "github.com/Eyevinn/mp4ff/mp4"
)

// sampleRef locates one sample inside the source file without holding its
// bytes. Remuxing only needs each sample's position, length and duration to
// rebuild the sample table; the media payload is streamed straight from the
// source file to the output, so a whole track never sits in memory.
type sampleRef struct {
	offset int64 // absolute offset in the source file
	size   uint32
	dur    uint32
}

// remuxToProgressive rewrites a fragmented MP4 (the form produced by both the
// native Widevine decrypt and the wrapper cbcs pipeline) into a progressive
// MP4 with a complete moov sample table.
//
// Why this matters: Apple Music streams are fragmented MP4 (ftyp + moov[mvex]
// + repeating moof/mdat). Telegram's inline audio player and many desktop
// players (e.g. Windows) cannot show a duration/progress bar or seek in a
// fragmented MP4 — playback appears broken even though the audio is intact.
//
// We must NOT use ffmpeg `-c copy` for this: ffmpeg's mp4 demuxer only reads
// the first fragment of these files (the moov advertises 0 samples and ffmpeg
// won't walk the moof chain), silently producing a file with just the first
// ~15s. Instead we use the mp4ff library, which correctly reads every fragment,
// and rebuild a flat sample table (stts/stsc/stsz/stco) + single mdat.
//
// Memory: the file is decoded with DecModeLazyMdat so only the box structure
// (moof headers, sample tables) is parsed into memory — mdat payloads stay on
// disk. Sample data is then copied source→output in bounded chunks. Peak usage
// is therefore proportional to the sample *count*, not the track size, which
// matters because lossless Apple Music tracks run to tens of megabytes and this
// runs on small VPS instances.
//
// The operation is in-place: on success the file at path is replaced with the
// progressive version. The ctx is accepted for signature symmetry; the work is
// CPU/IO-bound and not cancelled mid-write.
func remuxToProgressive(_ context.Context, path string) error {
	in, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open input: %w", err)
	}
	defer in.Close()

	parsed, err := mp4.DecodeFile(in, mp4.WithDecodeMode(mp4.DecModeLazyMdat))
	if err != nil {
		return fmt.Errorf("decode mp4: %w", err)
	}
	if !parsed.IsFragmented() {
		return nil // already progressive
	}
	if parsed.Init == nil || parsed.Init.Moov == nil {
		return fmt.Errorf("fragmented file has no init/moov")
	}

	refs, totalDur, err := collectSampleRefs(parsed)
	if err != nil {
		return fmt.Errorf("collect samples: %w", err)
	}
	if len(refs) == 0 {
		return fmt.Errorf("no samples found in fragments")
	}

	moov, mdat, err := buildProgressiveBoxes(parsed, refs, totalDur)
	if err != nil {
		return fmt.Errorf("build progressive: %w", err)
	}

	tmp := path + ".prog.m4a"
	out, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	if err := writeProgressive(out, in, parsed, moov, mdat, refs); err != nil {
		out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace original: %w", err)
	}
	return nil
}

// collectSampleRefs walks every fragment and records where each sample lives in
// the source file, in decode order, together with the summed duration.
//
// The offset arithmetic mirrors mp4ff's Fragment.GetFullSamples, which we
// cannot use here: it slices the samples out of mdat.Data, and under
// DecModeLazyMdat that buffer is deliberately empty.
func collectSampleRefs(frag *mp4.File) ([]sampleRef, uint64, error) {
	moov := frag.Init.Moov
	if len(moov.Traks) != 1 {
		return nil, 0, fmt.Errorf("expected 1 track, got %d", len(moov.Traks))
	}
	var trex *mp4.TrexBox
	if moov.Mvex != nil && len(moov.Mvex.Trexs) > 0 {
		trex = moov.Mvex.Trexs[0]
	}

	var (
		refs     []sampleRef
		totalDur uint64
	)
	for _, seg := range frag.Segments {
		for _, f := range seg.Fragments {
			if f.Moof == nil || f.Mdat == nil {
				continue
			}
			traf, ok := selectTraf(f.Moof, trex)
			if !ok {
				continue // this trackID is absent from this fragment
			}
			tfhd := traf.Tfhd
			for _, trun := range traf.Truns {
				trun.AddSampleDefaultValues(tfhd, trex)

				// Per ISO 14496-12 §8.8.7.1 the default base is the moof start.
				baseOffset := f.Moof.StartPos
				if tfhd.HasBaseDataOffset() {
					baseOffset = tfhd.BaseDataOffset
				} else if tfhd.DefaultBaseIfMoof() {
					baseOffset = f.Moof.StartPos
				}
				if trun.HasDataOffset() {
					baseOffset = uint64(int64(trun.DataOffset) + int64(baseOffset))
				}
				// baseOffset==0 means "no explicit offset": data starts at the
				// mdat payload itself.
				pos := baseOffset
				if pos == 0 {
					pos = f.Mdat.PayloadAbsoluteOffset()
				}

				// Reject offsets outside this fragment's mdat instead of
				// emitting a file that silently points at garbage.
				payloadStart := f.Mdat.PayloadAbsoluteOffset()
				payloadEnd := payloadStart + f.Mdat.GetLazyDataSize()
				for i := range trun.Samples {
					s := &trun.Samples[i]
					end := pos + uint64(s.Size)
					if pos < payloadStart || end > payloadEnd {
						return nil, 0, fmt.Errorf(
							"sample range [%d,%d) outside mdat payload [%d,%d)",
							pos, end, payloadStart, payloadEnd)
					}
					refs = append(refs, sampleRef{
						offset: int64(pos),
						size:   s.Size,
						dur:    s.Dur,
					})
					totalDur += uint64(s.Dur)
					pos = end
				}
			}
		}
	}
	return refs, totalDur, nil
}

// selectTraf picks the traf matching trex's track, or the first one when there
// is no trex. The bool reports whether a usable traf was found.
func selectTraf(moof *mp4.MoofBox, trex *mp4.TrexBox) (*mp4.TrafBox, bool) {
	if trex == nil {
		if moof.Traf == nil {
			return nil, false
		}
		return moof.Traf, true
	}
	for _, traf := range moof.Trafs {
		if traf.Tfhd != nil && traf.Tfhd.TrackID == trex.TrackID {
			return traf, true
		}
	}
	return nil, false
}

// buildProgressiveBoxes rebuilds the moov with a flat sample table and returns
// it alongside an mdat sized for the media payload but holding no data (the
// bytes are streamed in later by writeProgressive).
//
// Apple Music enhancedHls is single-track audio; we handle the general single
// audio-track case (the only shape these decrypt pipelines produce).
func buildProgressiveBoxes(frag *mp4.File, refs []sampleRef, totalDur uint64) (*mp4.MoovBox, *mp4.MdatBox, error) {
	moov := frag.Init.Moov
	trak := moov.Traks[0]

	sampleSize := make([]uint32, len(refs))
	var mediaSize uint64
	for i, r := range refs {
		sampleSize[i] = r.size
		mediaSize += uint64(r.size)
	}

	// Rebuild the sample table boxes from the gathered samples.
	stbl := trak.Mdia.Minf.Stbl

	// stts: run-length encode (count, delta) of per-sample durations.
	stts := &mp4.SttsBox{}
	for _, r := range refs {
		n := len(stts.SampleTimeDelta)
		if n > 0 && stts.SampleTimeDelta[n-1] == r.dur {
			stts.SampleCount[n-1]++
		} else {
			stts.SampleCount = append(stts.SampleCount, 1)
			stts.SampleTimeDelta = append(stts.SampleTimeDelta, r.dur)
		}
	}

	// stsz: explicit per-sample sizes.
	stsz := &mp4.StszBox{
		SampleNumber: uint32(len(sampleSize)),
		SampleSize:   sampleSize,
	}

	// stsc: one chunk holding all samples.
	stsc := &mp4.StscBox{}
	if err := stsc.AddEntry(1, uint32(len(sampleSize)), 1); err != nil {
		return nil, nil, fmt.Errorf("stsc entry: %w", err)
	}

	// stco: single chunk; its absolute offset is patched below, once the moov
	// size (and hence the mdat position) is known.
	stco := &mp4.StcoBox{ChunkOffset: []uint32{0}}

	// Replace the stbl's table boxes with the rebuilt progressive ones, keeping
	// stsd (codec config) intact and dropping any fragment-only boxes.
	rebuildStblChildren(stbl, stts, stsc, stsz, stco)

	// Reflect total duration in mvhd/mdhd/tkhd.
	mdhdTimescale := trak.Mdia.Mdhd.Timescale
	trak.Mdia.Mdhd.Duration = totalDur
	if moov.Mvhd != nil {
		if moov.Mvhd.Timescale == mdhdTimescale {
			moov.Mvhd.Duration = totalDur
		} else if mdhdTimescale > 0 {
			moov.Mvhd.Duration = totalDur * uint64(moov.Mvhd.Timescale) / uint64(mdhdTimescale)
		}
	}
	if trak.Tkhd != nil && moov.Mvhd != nil && mdhdTimescale > 0 {
		trak.Tkhd.Duration = totalDur * uint64(moov.Mvhd.Timescale) / uint64(mdhdTimescale)
	}

	// Drop the edit list (edts/elst). In the source fragmented files its
	// SegmentDuration is 0 (the fragmented moov has no duration), and players
	// like ffmpeg honor that 0 and report the whole stream as 0-length /
	// duration N/A. A progressive single-track audio file does not need an
	// edit list, so removing it lets the duration come from mdhd/stts.
	trak.Edts = nil
	if len(trak.Children) > 0 {
		kept := trak.Children[:0]
		for _, child := range trak.Children {
			if child.Type() == "edts" {
				continue
			}
			kept = append(kept, child)
		}
		trak.Children = kept
	}

	// Remove mvex (fragment declaration) — a progressive file must not have it.
	removeMvex(moov)

	// Size the mdat without giving it any data: Encode then writes just the
	// header and writeProgressive streams the payload in after it.
	mdat := &mp4.MdatBox{}
	mdat.SetLazyDataSize(mediaSize)

	// Compute where the mdat payload will land: ftyp + moov + mdat header.
	var pos uint64
	if ftyp := progressiveFtyp(frag); ftyp != nil {
		pos += ftyp.Size()
	}
	pos += moov.Size()
	mdat.Size() // sets LargeSize when the payload needs a 64-bit header
	payloadStart := pos + mdat.HeaderSize()

	// stco is 32-bit. Refuse rather than silently truncate — a wrapped offset
	// would produce a file that parses but plays garbage.
	if payloadStart > 0xFFFFFFFF {
		return nil, nil, fmt.Errorf("media offset %d exceeds 32-bit stco range", payloadStart)
	}
	stco.ChunkOffset[0] = uint32(payloadStart)

	return moov, mdat, nil
}

// progressiveFtyp returns the ftyp to carry over to the progressive file.
func progressiveFtyp(frag *mp4.File) *mp4.FtypBox {
	if frag.Ftyp != nil {
		return frag.Ftyp
	}
	if frag.Init != nil {
		return frag.Init.Ftyp
	}
	return nil
}

// writeProgressive emits ftyp + moov + mdat header to dst, then streams the
// sample payload from src. Samples that are already adjacent in the source are
// coalesced into a single copy, so a typical fragment costs one seek rather
// than one per sample.
func writeProgressive(dst io.Writer, src io.ReadSeeker, frag *mp4.File, moov *mp4.MoovBox, mdat *mp4.MdatBox, refs []sampleRef) error {
	if ftyp := progressiveFtyp(frag); ftyp != nil {
		if err := ftyp.Encode(dst); err != nil {
			return fmt.Errorf("encode ftyp: %w", err)
		}
	}
	if err := moov.Encode(dst); err != nil {
		return fmt.Errorf("encode moov: %w", err)
	}
	if err := mdat.Encode(dst); err != nil { // header only: data is lazy
		return fmt.Errorf("encode mdat header: %w", err)
	}

	var written uint64
	for i := 0; i < len(refs); {
		start := refs[i].offset
		size := int64(refs[i].size)
		j := i + 1
		for j < len(refs) && refs[j].offset == start+size {
			size += int64(refs[j].size)
			j++
		}
		if _, err := src.Seek(start, io.SeekStart); err != nil {
			return fmt.Errorf("seek to sample data at %d: %w", start, err)
		}
		n, err := io.CopyN(dst, src, size)
		if err != nil {
			return fmt.Errorf("copy %d bytes of sample data at %d: %w", size, start, err)
		}
		written += uint64(n)
		i = j
	}

	if want := mdat.GetLazyDataSize(); written != want {
		return fmt.Errorf("wrote %d bytes of media, expected %d", written, want)
	}
	return nil
}

// rebuildStblChildren replaces the sample-table boxes in stbl with the rebuilt
// progressive ones, preserving stsd and dropping stale/fragment boxes.
func rebuildStblChildren(stbl *mp4.StblBox, stts *mp4.SttsBox, stsc *mp4.StscBox, stsz *mp4.StszBox, stco *mp4.StcoBox) {
	var kept []mp4.Box
	for _, child := range stbl.Children {
		switch child.Type() {
		case "stsd":
			kept = append(kept, child) // keep codec config
		case "stts", "stsc", "stsz", "stco", "co64", "ctts", "stss", "sgpd", "sbgp":
			// drop: rebuilt below or fragment-only
		default:
			kept = append(kept, child)
		}
	}
	kept = append(kept, stts, stsc, stsz, stco)
	stbl.Children = kept
	stbl.Stts = stts
	stbl.Stsc = stsc
	stbl.Stsz = stsz
	stbl.Stco = stco
}

// removeMvex drops the mvex box from moov (both the typed field and Children).
func removeMvex(moov *mp4.MoovBox) {
	moov.Mvex = nil
	out := moov.Children[:0]
	for _, child := range moov.Children {
		if child.Type() == "mvex" {
			continue
		}
		out = append(out, child)
	}
	moov.Children = out
}
