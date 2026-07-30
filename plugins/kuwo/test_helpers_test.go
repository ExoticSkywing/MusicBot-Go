package kuwo

import (
	"context"
	"encoding/binary"
	"io"
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
	for index := 42; index < len(data); index++ {
		data[index] = byte(index*31 + 7)
	}
	return data
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
