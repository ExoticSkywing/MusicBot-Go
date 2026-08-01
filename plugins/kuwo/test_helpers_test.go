package kuwo

import (
	"context"
	"crypto/md5"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"
)

func makeTestFLAC(
	t *testing.T,
	size int,
	sampleRate int,
	bitsPerSample int,
	channels int,
	duration time.Duration,
) []byte {
	t.Helper()
	if size < 42 {
		t.Fatalf("FLAC fixture size %d is too small", size)
	}
	data := make([]byte, size)
	copy(data[:4], "fLaC")
	data[4] = 0x80
	data[7] = 34
	binary.BigEndian.PutUint16(data[8:10], 4096)
	binary.BigEndian.PutUint16(data[10:12], 4096)
	totalSamples := uint64(float64(sampleRate) * duration.Seconds())
	packed := uint64(sampleRate)<<44 |
		uint64(channels-1)<<41 |
		uint64(bitsPerSample-1)<<36 |
		totalSamples
	binary.BigEndian.PutUint64(data[18:26], packed)
	if size > 42 {
		if totalSamples == 0 || bitsPerSample%8 != 0 {
			t.Fatalf(
				"unsupported full FLAC fixture: samples=%d bits=%d",
				totalSamples,
				bitsPerSample,
			)
		}
		var (
			frames       [][]byte
			frameBytes   int
			minFrameSize int
			maxFrameSize int
		)
		for frameNumber, remaining := uint64(0), totalSamples; remaining > 0; frameNumber++ {
			blockSize := uint64(4096)
			if remaining < blockSize {
				blockSize = remaining
			}
			frame := makeTestFLACFrame(
				t,
				frameNumber,
				blockSize,
				bitsPerSample,
				channels,
			)
			frames = append(frames, frame)
			frameBytes += len(frame)
			if minFrameSize == 0 || len(frame) < minFrameSize {
				minFrameSize = len(frame)
			}
			if len(frame) > maxFrameSize {
				maxFrameSize = len(frame)
			}
			remaining -= blockSize
		}
		const paddingHeaderSize = 4
		paddingSize := size - 42 - paddingHeaderSize - frameBytes
		if paddingSize < 0 {
			t.Fatalf(
				"FLAC fixture size %d cannot hold %d bytes of frames",
				size,
				frameBytes,
			)
		}
		data[4] = 0 // STREAMINFO is followed by a final PADDING block.
		data[42] = 0x80 | 1
		putTestFLACUint24(data[43:46], paddingSize)
		putTestFLACUint24(data[12:15], minFrameSize)
		putTestFLACUint24(data[15:18], maxFrameSize)
		offset := 46 + paddingSize
		for _, frame := range frames {
			offset += copy(data[offset:], frame)
		}

		pcmHash := md5.New()
		var zeroes [4096]byte
		pcmBytes := totalSamples * uint64(channels) * uint64(bitsPerSample/8)
		for pcmBytes > 0 {
			chunkSize := min(pcmBytes, uint64(len(zeroes)))
			_, _ = pcmHash.Write(zeroes[:int(chunkSize)])
			pcmBytes -= chunkSize
		}
		copy(data[26:42], pcmHash.Sum(nil))
	}
	return data
}

func makeTestFLACFrame(
	t *testing.T,
	frameNumber uint64,
	blockSize uint64,
	bitsPerSample int,
	channels int,
) []byte {
	t.Helper()
	if blockSize == 0 || blockSize > 4096 || bitsPerSample%8 != 0 ||
		bitsPerSample <= 0 || channels <= 0 || channels > 8 {
		t.Fatalf(
			"unsupported FLAC frame fixture: number=%d block=%d bits=%d channels=%d",
			frameNumber,
			blockSize,
			bitsPerSample,
			channels,
		)
	}
	frame := []byte{
		0xff,
		0xf8,                  // fixed-block stream
		0x70,                  // 16-bit block-size extension, sample rate from STREAMINFO
		byte(channels-1) << 4, // independent channels, bits from STREAMINFO
	}
	frame = append(frame, encodeTestFLACUTF8Number(t, frameNumber)...)
	frame = append(
		frame,
		byte((blockSize-1)>>8),
		byte(blockSize-1),
	)
	frame = append(frame, testFLACCRC8(frame))
	for range channels {
		frame = append(frame, 0x00) // constant subframe, no wasted bits
		frame = append(frame, make([]byte, bitsPerSample/8)...)
	}
	crc := testFLACCRC16(frame)
	return append(frame, byte(crc>>8), byte(crc))
}

func encodeTestFLACUTF8Number(t *testing.T, value uint64) []byte {
	t.Helper()
	switch {
	case value <= 0x7f:
		return []byte{byte(value)}
	case value <= 0x7ff:
		return []byte{
			0xc0 | byte(value>>6),
			0x80 | byte(value&0x3f),
		}
	case value <= 0xffff:
		return []byte{
			0xe0 | byte(value>>12),
			0x80 | byte(value>>6&0x3f),
			0x80 | byte(value&0x3f),
		}
	case value <= 0x1fffff:
		return []byte{
			0xf0 | byte(value>>18),
			0x80 | byte(value>>12&0x3f),
			0x80 | byte(value>>6&0x3f),
			0x80 | byte(value&0x3f),
		}
	case value <= 0x3ffffff:
		return []byte{
			0xf8 | byte(value>>24),
			0x80 | byte(value>>18&0x3f),
			0x80 | byte(value>>12&0x3f),
			0x80 | byte(value>>6&0x3f),
			0x80 | byte(value&0x3f),
		}
	case value <= 0x7fffffff:
		return []byte{
			0xfc | byte(value>>30),
			0x80 | byte(value>>24&0x3f),
			0x80 | byte(value>>18&0x3f),
			0x80 | byte(value>>12&0x3f),
			0x80 | byte(value>>6&0x3f),
			0x80 | byte(value&0x3f),
		}
	default:
		t.Fatalf("FLAC frame number %d exceeds fixed-block range", value)
		return nil
	}
}

func putTestFLACUint24(destination []byte, value int) {
	destination[0] = byte(value >> 16)
	destination[1] = byte(value >> 8)
	destination[2] = byte(value)
}

func testFLACCRC8(data []byte) byte {
	var crc byte
	for _, value := range data {
		crc ^= value
		for range 8 {
			if crc&0x80 != 0 {
				crc = crc<<1 ^ 0x07
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

func testFLACCRC16(data []byte) uint16 {
	var crc uint16
	for _, value := range data {
		crc ^= uint16(value) << 8
		for range 8 {
			if crc&0x8000 != 0 {
				crc = crc<<1 ^ 0x8005
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

func directFLACTestTailResponse(
	t *testing.T,
	req *http.Request,
	stream []byte,
) *http.Response {
	t.Helper()
	if len(stream) <= 42 {
		t.Fatalf("direct FLAC fixture size = %d, want more than 42", len(stream))
	}
	var header [42]byte
	copy(header[:], stream[:42])
	start, err := directFLACProbeRangeStart(header, int64(len(stream)))
	if err != nil {
		t.Fatalf("derive direct FLAC test range: %v", err)
	}
	end := int64(len(stream) - 1)
	wantRange := fmt.Sprintf("bytes=%d-%d", start, end)
	if got := req.Header.Get("Range"); got != wantRange {
		t.Fatalf("direct FLAC Range = %q, want %q", got, wantRange)
	}
	body := append([]byte(nil), stream[start:]...)
	result := response(
		http.StatusPartialContent,
		map[string]string{
			"Content-Range": fmt.Sprintf(
				"bytes %d-%d/%d",
				start,
				end,
				len(stream),
			),
			"Content-Type": "audio/flac",
		},
		body,
	)
	result.ContentLength = int64(len(body))
	return result
}

type chunkReader struct {
	data      []byte
	chunkSize int
}

func (reader *chunkReader) Read(buffer []byte) (int, error) {
	if len(reader.data) == 0 {
		return 0, io.EOF
	}
	size := reader.chunkSize
	if size <= 0 || size > len(reader.data) {
		size = len(reader.data)
	}
	if size > len(buffer) {
		size = len(buffer)
	}
	copy(buffer, reader.data[:size])
	reader.data = reader.data[size:]
	return size, nil
}

type unexpectedEOFReader struct {
	data []byte
	done bool
}

type cancelAfterFirstReader struct {
	data   []byte
	cancel context.CancelFunc
}

func (reader *cancelAfterFirstReader) Read(buffer []byte) (int, error) {
	if len(reader.data) == 0 {
		return 0, io.EOF
	}
	size := copy(buffer, reader.data)
	reader.data = reader.data[size:]
	if reader.cancel != nil {
		reader.cancel()
		reader.cancel = nil
	}
	return size, nil
}

func (reader *unexpectedEOFReader) Read(buffer []byte) (int, error) {
	if len(reader.data) > 0 {
		size := copy(buffer, reader.data)
		reader.data = reader.data[size:]
		return size, nil
	}
	if !reader.done {
		reader.done = true
		return 0, io.ErrUnexpectedEOF
	}
	return 0, io.EOF
}
