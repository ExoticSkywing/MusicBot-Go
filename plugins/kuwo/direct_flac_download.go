package kuwo

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/platform"
	"github.com/mewkiz/flac"
	flacframe "github.com/mewkiz/flac/frame"
)

const (
	directFLACDownloadBufferSize = 64 << 10
	directFLACTrailerSearchSize  = 4 << 10
	directFLACMaxFrameSize       = 4 << 20
	directFLACMaxCandidates      = 8
	directFLACProbeTimeout       = 30 * time.Second
	directFLACDownloadTimeout    = 20 * time.Minute
)

var (
	// Retained as a production-observed fixture for compatibility tests. The
	// parser never trusts these payload bytes; it proves the preceding FLAC
	// frame and accepts any uniquely bounded Kuwo envelope.
	knownDirectFLACTrailer = []byte{
		0xf0, 0x00, 0xff, 0x0f, 0x44,
		0x44, 0x40, 0x48, 0x46, 0x3c,
		0x36, 0x0e, 0x55, 0xff, 0xf0,
	}
	errDirectFLACIntegrity = errors.New("kuwo: direct FLAC integrity check failed")
)

type directFLACTrailerProbe struct {
	rawSize    int64
	outputSize int64
	rangeStart int64
	trailerLen int64
	tailHash   [sha256.Size]byte
	header     [42]byte
}

type directFLACTailAnalysis struct {
	outputSize int64
	trailerLen int64
}

type directFLACContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r directFLACContextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}

func analyzeDirectFLACTail(
	header [42]byte,
	rawSize int64,
	rangeStart int64,
	tail []byte,
) (directFLACTailAnalysis, error) {
	if rawSize < int64(len(header)) ||
		rangeStart < 0 ||
		rangeStart > rawSize ||
		int64(len(tail)) != rawSize-rangeStart {
		return directFLACTailAnalysis{}, fmt.Errorf(
			"%w: invalid tail window",
			errDirectFLACIntegrity,
		)
	}
	streamInfo, err := parseFLACStreamInfo(header[:])
	if err != nil {
		return directFLACTailAnalysis{}, fmt.Errorf(
			"%w: invalid STREAMINFO",
			errDirectFLACIntegrity,
		)
	}
	if streamInfo.maxFrameSize > directFLACMaxFrameSize {
		return directFLACTailAnalysis{}, fmt.Errorf(
			"%w: declared frame size exceeds bound",
			errDirectFLACIntegrity,
		)
	}

	eofProofs := countDirectFLACFinalFrameProofs(
		streamInfo,
		tail,
		rangeStart,
		rawSize,
	)
	switch {
	case eofProofs == 1:
		return directFLACTailAnalysis{outputSize: rawSize}, nil
	case eofProofs > 1:
		return directFLACTailAnalysis{}, fmt.Errorf(
			"%w: ambiguous final FLAC frame",
			errDirectFLACIntegrity,
		)
	}

	prefix := []byte{0xf0, 0x00, 0xff, 0x0f}
	suffix := []byte{0x0e, 0x55, 0xff, 0xf0}
	if !bytes.HasSuffix(tail, suffix) {
		return directFLACTailAnalysis{}, fmt.Errorf(
			"%w: unknown trailing data",
			errDirectFLACIntegrity,
		)
	}
	searchStart := max(rangeStart, rawSize-directFLACTrailerSearchSize)
	searchStartIndex := int(searchStart - rangeStart)
	searchEndIndex := len(tail) - len(suffix)
	var provenBoundary int64 = -1
	envelopeCandidates := 0
	for index := searchStartIndex; index+len(prefix) <= searchEndIndex; index++ {
		if !bytes.Equal(tail[index:index+len(prefix)], prefix) {
			continue
		}
		envelopeCandidates++
		if envelopeCandidates > directFLACMaxCandidates {
			return directFLACTailAnalysis{}, fmt.Errorf(
				"%w: too many trailer candidates",
				errDirectFLACIntegrity,
			)
		}
		boundary := rangeStart + int64(index)
		proofs := countDirectFLACFinalFrameProofs(
			streamInfo,
			tail,
			rangeStart,
			boundary,
		)
		if proofs > 1 {
			return directFLACTailAnalysis{}, fmt.Errorf(
				"%w: ambiguous final FLAC frame",
				errDirectFLACIntegrity,
			)
		}
		if proofs != 1 {
			continue
		}
		if provenBoundary >= 0 {
			return directFLACTailAnalysis{}, fmt.Errorf(
				"%w: ambiguous trailer boundary",
				errDirectFLACIntegrity,
			)
		}
		provenBoundary = boundary
	}
	if provenBoundary < 0 {
		return directFLACTailAnalysis{}, fmt.Errorf(
			"%w: unproven trailer boundary",
			errDirectFLACIntegrity,
		)
	}
	return directFLACTailAnalysis{
		outputSize: provenBoundary,
		trailerLen: rawSize - provenBoundary,
	}, nil
}

func countDirectFLACFinalFrameProofs(
	streamInfo flacStreamInfo,
	tail []byte,
	rangeStart int64,
	boundary int64,
) int {
	boundaryIndex := int(boundary - rangeStart)
	if boundaryIndex < 0 || boundaryIndex > len(tail) {
		return 0
	}
	frameLimit := streamInfo.maxFrameSize
	if frameLimit == 0 {
		frameLimit = directFLACMaxFrameSize
	}
	startIndex := max(0, boundaryIndex-frameLimit)
	proofs := 0
	candidates := 0
	for index := startIndex; index+6 <= boundaryIndex; index++ {
		if tail[index] != 0xff || tail[index+1]&0xfe != 0xf8 {
			continue
		}
		parsedFrame, reader, candidate := newDirectFLACFinalFrameCandidate(
			streamInfo,
			tail[index:boundaryIndex],
		)
		if !candidate {
			continue
		}
		candidates++
		if candidates > directFLACMaxCandidates {
			return directFLACMaxCandidates + 1
		}
		if err := parsedFrame.Parse(); err == nil && reader.Len() == 0 {
			proofs++
		}
	}
	return proofs
}

// newDirectFLACFinalFrameCandidate performs only the cheap header and
// STREAMINFO checks. The caller applies its candidate budget before the
// potentially expensive subframe and CRC-16 parse, so raw sync-like bytes
// inside compressed payloads do not consume that budget.
func newDirectFLACFinalFrameCandidate(
	streamInfo flacStreamInfo,
	frame []byte,
) (*flacframe.Frame, *bytes.Reader, bool) {
	if len(frame) < 8 {
		return nil, nil, false
	}
	if streamInfo.minFrameSize > 0 && len(frame) < streamInfo.minFrameSize {
		return nil, nil, false
	}
	if streamInfo.maxFrameSize > 0 && len(frame) > streamInfo.maxFrameSize {
		return nil, nil, false
	}
	if len(frame) > directFLACMaxFrameSize {
		return nil, nil, false
	}

	reader := bytes.NewReader(frame)
	parsedFrame, err := flacframe.New(reader)
	if err != nil {
		return nil, nil, false
	}
	if parsedFrame.SampleRate == 0 {
		parsedFrame.SampleRate = uint32(streamInfo.sampleRate)
	}
	if parsedFrame.BitsPerSample == 0 {
		parsedFrame.BitsPerSample = uint8(streamInfo.bitsPerSample)
	}
	if parsedFrame.BlockSize == 0 ||
		int(parsedFrame.BlockSize) > streamInfo.maxBlockSize ||
		parsedFrame.SampleRate != uint32(streamInfo.sampleRate) ||
		int(parsedFrame.Channels.Count()) != streamInfo.channels ||
		parsedFrame.BitsPerSample != uint8(streamInfo.bitsPerSample) {
		return nil, nil, false
	}

	var endSample uint64
	if parsedFrame.HasFixedBlockSize {
		if streamInfo.minBlockSize != streamInfo.maxBlockSize ||
			parsedFrame.Num > 0x7fffffff {
			return nil, nil, false
		}
		endSample = parsedFrame.Num*uint64(streamInfo.maxBlockSize) +
			uint64(parsedFrame.BlockSize)
	} else {
		if parsedFrame.Num > 0xfffffffff {
			return nil, nil, false
		}
		endSample = parsedFrame.Num + uint64(parsedFrame.BlockSize)
	}
	if endSample != streamInfo.totalSamples {
		return nil, nil, false
	}
	return parsedFrame, reader, true
}

// probeDirectFLACTrailer captures a bounded final-frame window. A Kuwo marker
// is only a candidate boundary: the preceding FLAC frame must independently
// parse, pass CRC checks, and end at STREAMINFO's exact total-sample count.
func (c *Client) probeDirectFLACTrailer(
	ctx context.Context,
	rawURL string,
	rawSize int64,
	header [42]byte,
) (directFLACTrailerProbe, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateDirectFLACExpectation(rawURL, rawSize); err != nil {
		return directFLACTrailerProbe{}, err
	}
	rangeStart, err := directFLACProbeRangeStart(header, rawSize)
	if err != nil {
		return directFLACTrailerProbe{}, err
	}
	client, attempts := c.downloadClientSnapshot()
	if client == nil {
		return directFLACTrailerProbe{}, errors.New("kuwo: direct FLAC download client unavailable")
	}
	if attempts <= 0 {
		attempts = 3
	}

	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		result, retryable, err := probeDirectFLACTrailerOnce(
			ctx,
			client,
			rawURL,
			rawSize,
			header,
			rangeStart,
		)
		if err == nil {
			return result, nil
		}
		lastErr = err
		if !retryable ||
			errors.Is(err, errDirectFLACIntegrity) ||
			errors.Is(err, errUnsafeMediaURL) ||
			errors.Is(err, context.Canceled) ||
			errors.Is(err, context.DeadlineExceeded) ||
			attempt == attempts-1 {
			return directFLACTrailerProbe{}, err
		}
		if err := waitDirectFLACRetry(ctx, attempt); err != nil {
			return directFLACTrailerProbe{}, err
		}
	}
	return directFLACTrailerProbe{}, lastErr
}

func probeDirectFLACTrailerOnce(
	ctx context.Context,
	baseClient *http.Client,
	rawURL string,
	rawSize int64,
	header [42]byte,
	rangeStart int64,
) (result directFLACTrailerProbe, retryable bool, returnErr error) {
	client := directFLACHTTPClientWithTimeout(
		baseClient,
		directFLACProbeTimeout,
	)
	end := rawSize - 1
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return directFLACTrailerProbe{}, false, errors.New("kuwo: create direct FLAC trailer request")
	}
	applyMediaHeaders(req)
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", rangeStart, end))
	resp, err := client.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return directFLACTrailerProbe{}, false, ctxErr
		}
		if errors.Is(err, errUnsafeMediaURL) {
			return directFLACTrailerProbe{}, false, redactDirectFLACRequestError(err)
		}
		return directFLACTrailerProbe{}, true, redactDirectFLACRequestError(err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return directFLACTrailerProbe{}, false, platform.NewRateLimitedError("kuwo")
	case resp.StatusCode == http.StatusRequestTimeout,
		resp.StatusCode >= 500:
		return directFLACTrailerProbe{}, true, fmt.Errorf(
			"kuwo: direct FLAC trailer returned HTTP %d",
			resp.StatusCode,
		)
	case resp.StatusCode != http.StatusPartialContent:
		return directFLACTrailerProbe{}, false, fmt.Errorf(
			"%w: trailer returned HTTP %d",
			errDirectFLACIntegrity,
			resp.StatusCode,
		)
	}
	if !isIdentityResponse(resp) {
		return directFLACTrailerProbe{}, false, fmt.Errorf(
			"%w: encoded trailer response",
			errDirectFLACIntegrity,
		)
	}
	windowSize := rawSize - rangeStart
	if resp.ContentLength != windowSize {
		return directFLACTrailerProbe{}, false, fmt.Errorf(
			"%w: trailer content length mismatch",
			errDirectFLACIntegrity,
		)
	}
	wantRange := fmt.Sprintf("bytes %d-%d/%d", rangeStart, end, rawSize)
	if strings.TrimSpace(resp.Header.Get("Content-Range")) != wantRange {
		return directFLACTrailerProbe{}, false, fmt.Errorf(
			"%w: trailer content range mismatch",
			errDirectFLACIntegrity,
		)
	}
	body, err := io.ReadAll(io.LimitReader(
		resp.Body,
		windowSize+1,
	))
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return directFLACTrailerProbe{}, false, ctxErr
		}
		return directFLACTrailerProbe{}, true, errors.New("kuwo: read direct FLAC trailer failed")
	}
	if int64(len(body)) != windowSize {
		return directFLACTrailerProbe{}, false, fmt.Errorf(
			"%w: incomplete trailer response",
			errDirectFLACIntegrity,
		)
	}
	analysis, err := analyzeDirectFLACTail(header, rawSize, rangeStart, body)
	if err != nil {
		return directFLACTrailerProbe{}, false, err
	}
	result.rawSize = rawSize
	result.outputSize = analysis.outputSize
	result.rangeStart = rangeStart
	result.trailerLen = analysis.trailerLen
	result.tailHash = sha256.Sum256(body)
	result.header = header
	return result, false, nil
}

func directFLACProbeRangeStart(header [42]byte, rawSize int64) (int64, error) {
	streamInfo, err := parseFLACStreamInfo(header[:])
	if err != nil {
		return 0, fmt.Errorf(
			"%w: invalid STREAMINFO",
			errDirectFLACIntegrity,
		)
	}
	if streamInfo.maxFrameSize > directFLACMaxFrameSize {
		return 0, fmt.Errorf(
			"%w: declared frame size exceeds bound",
			errDirectFLACIntegrity,
		)
	}
	frameWindow := int64(streamInfo.maxFrameSize)
	if frameWindow == 0 {
		frameWindow = directFLACMaxFrameSize
	}
	windowSize := frameWindow + directFLACTrailerSearchSize
	if windowSize > rawSize {
		windowSize = rawSize
	}
	return rawSize - windowSize, nil
}

// directFLACDownloader captures every property verified during resolution.
// The full GET must still match the raw size, trailer state, and STREAMINFO
// exactly; otherwise no destination file is published.
func (c *Client) directFLACDownloader(
	expectedURL string,
	rawSize int64,
	expectedHeader [42]byte,
	expectedProbe directFLACTrailerProbe,
) platform.DownloadFunc {
	return func(
		ctx context.Context,
		info *platform.DownloadInfo,
		destination string,
		progress func(written, total int64),
	) (int64, error) {
		return c.downloadDirectFLAC(
			ctx,
			info,
			expectedURL,
			rawSize,
			expectedHeader,
			expectedProbe,
			destination,
			progress,
		)
	}
}

func (c *Client) downloadDirectFLAC(
	ctx context.Context,
	info *platform.DownloadInfo,
	expectedURL string,
	rawSize int64,
	expectedHeader [42]byte,
	expectedProbe directFLACTrailerProbe,
	destination string,
	progress func(written, total int64),
) (int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if info == nil || strings.TrimSpace(info.URL) == "" {
		return 0, errors.New("kuwo: direct FLAC download info missing")
	}
	if destination == "" {
		return 0, errors.New("kuwo: direct FLAC destination missing")
	}
	if info.URL != expectedURL {
		return 0, fmt.Errorf(
			"%w: source URL changed since probe",
			errDirectFLACIntegrity,
		)
	}
	if err := validateDirectFLACExpectation(info.URL, rawSize); err != nil {
		return 0, err
	}
	if err := validateDirectFLACProbe(
		expectedProbe,
		rawSize,
		expectedHeader,
	); err != nil {
		return 0, err
	}
	if info.Size > 0 && info.Size != expectedProbe.outputSize {
		return 0, fmt.Errorf(
			"%w: expected output size mismatch",
			errDirectFLACIntegrity,
		)
	}
	if err := ensureDirectFLACDestinationAbsent(destination); err != nil {
		return 0, err
	}
	client, attempts := c.downloadClientSnapshot()
	if client == nil {
		return 0, errors.New("kuwo: direct FLAC download client unavailable")
	}
	if attempts <= 0 {
		attempts = 3
	}

	var lastErr error
	reportProgress := progress
	if progress != nil {
		var highWater int64
		reportProgress = func(written, total int64) {
			if written <= highWater {
				return
			}
			highWater = written
			progress(written, total)
		}
	}
	for attempt := 0; attempt < attempts; attempt++ {
		written, retryable, err := downloadDirectFLACOnce(
			ctx,
			client,
			info,
			expectedProbe,
			destination,
			reportProgress,
		)
		if err == nil {
			return written, nil
		}
		lastErr = err
		if !retryable ||
			errors.Is(err, errDirectFLACIntegrity) ||
			errors.Is(err, errUnsafeMediaURL) ||
			errors.Is(err, os.ErrExist) ||
			errors.Is(err, context.Canceled) ||
			errors.Is(err, context.DeadlineExceeded) ||
			attempt == attempts-1 {
			return 0, err
		}
		if err := waitDirectFLACRetry(ctx, attempt); err != nil {
			return 0, err
		}
	}
	return 0, lastErr
}

func downloadDirectFLACOnce(
	ctx context.Context,
	baseClient *http.Client,
	info *platform.DownloadInfo,
	expectedProbe directFLACTrailerProbe,
	destination string,
	progress func(written, total int64),
) (written int64, retryable bool, returnErr error) {
	if err := validateDirectFLACURL(info.URL); err != nil {
		return 0, false, err
	}
	rawSize := expectedProbe.rawSize
	expectedOutputSize := expectedProbe.outputSize
	client := directFLACHTTPClient(baseClient)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, info.URL, nil)
	if err != nil {
		return 0, false, errors.New("kuwo: create direct FLAC request")
	}
	applyMediaHeaders(req)
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := client.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return 0, false, ctxErr
		}
		if errors.Is(err, errUnsafeMediaURL) {
			return 0, false, redactDirectFLACRequestError(err)
		}
		return 0, true, redactDirectFLACRequestError(err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return 0, false, platform.NewRateLimitedError("kuwo")
	case resp.StatusCode == http.StatusRequestTimeout,
		resp.StatusCode >= 500:
		return 0, true, fmt.Errorf(
			"kuwo: direct FLAC returned HTTP %d",
			resp.StatusCode,
		)
	case resp.StatusCode != http.StatusOK:
		return 0, false, fmt.Errorf(
			"kuwo: direct FLAC returned HTTP %d",
			resp.StatusCode,
		)
	}
	if !isIdentityResponse(resp) {
		return 0, false, fmt.Errorf(
			"%w: encoded response body",
			errDirectFLACIntegrity,
		)
	}
	if resp.ContentLength != rawSize {
		return 0, false, fmt.Errorf(
			"%w: content length mismatch",
			errDirectFLACIntegrity,
		)
	}

	directory := filepath.Dir(destination)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return 0, false, fmt.Errorf("kuwo: create direct FLAC directory: %w", err)
	}
	temporary, err := os.CreateTemp(
		directory,
		"."+filepath.Base(destination)+".direct-flac-part-*",
	)
	if err != nil {
		return 0, false, fmt.Errorf("kuwo: create direct FLAC temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		if !committed {
			_ = temporary.Close()
			_ = os.Remove(temporaryPath)
		}
	}()

	reader := io.LimitReader(resp.Body, rawSize+1)
	buffer := make([]byte, directFLACDownloadBufferSize)
	header := make([]byte, 0, 42)
	var rawRead int64
	for {
		if err := ctx.Err(); err != nil {
			return 0, false, err
		}
		readSize, readErr := reader.Read(buffer)
		if readSize > 0 {
			rawRead += int64(readSize)
			if rawRead > rawSize {
				return 0, false, fmt.Errorf(
					"%w: response exceeds expected size",
					errDirectFLACIntegrity,
				)
			}
			chunk := buffer[:readSize]
			if len(header) < cap(header) {
				headerBytes := min(readSize, cap(header)-len(header))
				header = append(header, chunk[:headerBytes]...)
			}
			writeSize, writeErr := temporary.Write(chunk)
			if writeErr != nil {
				return 0, false, fmt.Errorf(
					"kuwo: write direct FLAC: %w",
					writeErr,
				)
			}
			if writeSize != len(chunk) {
				return 0, false, io.ErrShortWrite
			}
			visibleWritten := min(rawRead, expectedOutputSize-1)
			if progress != nil {
				progress(visibleWritten, expectedOutputSize)
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			if ctxErr := ctx.Err(); ctxErr != nil {
				return 0, false, ctxErr
			}
			return 0, true, errors.New("kuwo: read direct FLAC failed")
		}
	}
	if rawRead != rawSize {
		return 0, false, fmt.Errorf(
			"%w: final raw size mismatch",
			errDirectFLACIntegrity,
		)
	}
	if len(header) != 42 {
		return 0, false, fmt.Errorf(
			"%w: incomplete FLAC STREAMINFO",
			errDirectFLACIntegrity,
		)
	}
	if _, err := parseFLACStreamInfo(header); err != nil {
		return 0, false, fmt.Errorf("%w: %v", errDirectFLACIntegrity, err)
	}
	if !bytes.Equal(header, expectedProbe.header[:]) {
		return 0, false, fmt.Errorf(
			"%w: STREAMINFO changed since probe",
			errDirectFLACIntegrity,
		)
	}

	tailSize := rawSize - expectedProbe.rangeStart
	if tailSize <= 0 ||
		tailSize > directFLACMaxFrameSize+directFLACTrailerSearchSize {
		return 0, false, fmt.Errorf(
			"%w: invalid verified tail window",
			errDirectFLACIntegrity,
		)
	}
	tail := make([]byte, int(tailSize))
	readSize, readErr := temporary.ReadAt(tail, expectedProbe.rangeStart)
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return 0, false, fmt.Errorf(
			"kuwo: reread direct FLAC tail: %w",
			readErr,
		)
	}
	if readSize != len(tail) {
		return 0, false, fmt.Errorf(
			"%w: incomplete verified tail window",
			errDirectFLACIntegrity,
		)
	}
	if sha256.Sum256(tail) != expectedProbe.tailHash {
		return 0, false, fmt.Errorf(
			"%w: tail changed since probe",
			errDirectFLACIntegrity,
		)
	}
	analysis, err := analyzeDirectFLACTail(
		expectedProbe.header,
		rawSize,
		expectedProbe.rangeStart,
		tail,
	)
	if err != nil {
		return 0, false, err
	}
	if analysis.outputSize != expectedProbe.outputSize ||
		analysis.trailerLen != expectedProbe.trailerLen {
		return 0, false, fmt.Errorf(
			"%w: tail analysis changed since probe",
			errDirectFLACIntegrity,
		)
	}
	if err := temporary.Truncate(expectedOutputSize); err != nil {
		return 0, false, fmt.Errorf("kuwo: truncate direct FLAC: %w", err)
	}
	if err := validateDirectFLACFile(
		ctx,
		temporary,
		expectedOutputSize,
		expectedProbe.header,
	); err != nil {
		return 0, false, err
	}
	written = expectedOutputSize
	if err := temporary.Sync(); err != nil {
		return 0, false, fmt.Errorf("kuwo: sync direct FLAC: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return 0, false, fmt.Errorf("kuwo: close direct FLAC: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return 0, false, err
	}
	if err := ensureDirectFLACDestinationAbsent(destination); err != nil {
		return 0, false, err
	}
	// The hard link is the no-replace commit primitive: because the temporary
	// file is in the destination directory, this publishes the already-synced
	// inode atomically and fails with EEXIST if another writer wins the race
	// after the checks above. os.Rename would overwrite on Unix.
	if err := os.Link(temporaryPath, destination); err != nil {
		return 0, false, fmt.Errorf("kuwo: commit direct FLAC: %w", err)
	}
	committed = true
	_ = os.Remove(temporaryPath)
	info.Size = written
	if progress != nil {
		progress(written, written)
	}
	return written, false, nil
}

func validateDirectFLACFile(
	ctx context.Context,
	file *os.File,
	outputSize int64,
	expectedHeader [42]byte,
) error {
	if file == nil || outputSize <= 42 {
		return fmt.Errorf(
			"%w: invalid full-stream input",
			errDirectFLACIntegrity,
		)
	}
	expectedInfo, err := parseFLACStreamInfo(expectedHeader[:])
	if err != nil {
		return fmt.Errorf(
			"%w: invalid expected STREAMINFO",
			errDirectFLACIntegrity,
		)
	}
	stream, err := flac.New(directFLACContextReader{
		ctx:    ctx,
		reader: io.NewSectionReader(file, 0, outputSize),
	})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf(
			"%w: parse full FLAC metadata: %v",
			errDirectFLACIntegrity,
			err,
		)
	}
	if stream.Info == nil ||
		stream.Info.BlockSizeMin != uint16(expectedInfo.minBlockSize) ||
		stream.Info.BlockSizeMax != uint16(expectedInfo.maxBlockSize) ||
		stream.Info.FrameSizeMin != uint32(expectedInfo.minFrameSize) ||
		stream.Info.FrameSizeMax != uint32(expectedInfo.maxFrameSize) ||
		stream.Info.SampleRate != uint32(expectedInfo.sampleRate) ||
		stream.Info.NChannels != uint8(expectedInfo.channels) ||
		stream.Info.BitsPerSample != uint8(expectedInfo.bitsPerSample) ||
		stream.Info.NSamples != expectedInfo.totalSamples {
		return fmt.Errorf(
			"%w: full-stream STREAMINFO mismatch",
			errDirectFLACIntegrity,
		)
	}
	if stream.Info.MD5sum == ([md5.Size]byte{}) {
		return fmt.Errorf(
			"%w: missing PCM MD5",
			errDirectFLACIntegrity,
		)
	}

	pcmHash := md5.New()
	var (
		decodedSamples uint64
		frameCount     int
		fixedStrategy  bool
		strategySet    bool
	)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		parsedFrame, frameErr := stream.Next()
		if errors.Is(frameErr, io.EOF) {
			break
		}
		if frameErr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			return fmt.Errorf(
				"%w: parse FLAC frame header: %v",
				errDirectFLACIntegrity,
				frameErr,
			)
		}
		if parsedFrame.SampleRate == 0 {
			parsedFrame.SampleRate = stream.Info.SampleRate
		}
		if parsedFrame.BitsPerSample == 0 {
			parsedFrame.BitsPerSample = stream.Info.BitsPerSample
		}
		if parsedFrame.BlockSize == 0 ||
			int(parsedFrame.BlockSize) > expectedInfo.maxBlockSize ||
			parsedFrame.SampleRate != stream.Info.SampleRate ||
			int(parsedFrame.Channels.Count()) != expectedInfo.channels ||
			parsedFrame.BitsPerSample != stream.Info.BitsPerSample {
			return fmt.Errorf(
				"%w: FLAC frame properties mismatch",
				errDirectFLACIntegrity,
			)
		}
		if !strategySet {
			fixedStrategy = parsedFrame.HasFixedBlockSize
			strategySet = true
		} else if parsedFrame.HasFixedBlockSize != fixedStrategy {
			return fmt.Errorf(
				"%w: mixed FLAC blocking strategies",
				errDirectFLACIntegrity,
			)
		}
		if parsedFrame.HasFixedBlockSize {
			if expectedInfo.minBlockSize != expectedInfo.maxBlockSize ||
				parsedFrame.Num*uint64(expectedInfo.maxBlockSize) != decodedSamples {
				return fmt.Errorf(
					"%w: discontinuous fixed-block FLAC frame",
					errDirectFLACIntegrity,
				)
			}
		} else if parsedFrame.Num != decodedSamples {
			return fmt.Errorf(
				"%w: discontinuous variable-block FLAC frame",
				errDirectFLACIntegrity,
			)
		}
		if err := parsedFrame.Parse(); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			return fmt.Errorf(
				"%w: decode full FLAC frame: %v",
				errDirectFLACIntegrity,
				err,
			)
		}
		parsedFrame.Hash(pcmHash)
		decodedSamples += uint64(parsedFrame.BlockSize)
		if decodedSamples > expectedInfo.totalSamples {
			return fmt.Errorf(
				"%w: FLAC samples exceed STREAMINFO",
				errDirectFLACIntegrity,
			)
		}
		frameCount++
	}
	if frameCount == 0 || decodedSamples != expectedInfo.totalSamples {
		return fmt.Errorf(
			"%w: FLAC sample count mismatch",
			errDirectFLACIntegrity,
		)
	}
	if !bytes.Equal(pcmHash.Sum(nil), stream.Info.MD5sum[:]) {
		return fmt.Errorf(
			"%w: PCM MD5 mismatch",
			errDirectFLACIntegrity,
		)
	}
	return nil
}

func validateDirectFLACExpectation(
	rawURL string,
	rawSize int64,
) error {
	if err := validateDirectFLACURL(rawURL); err != nil {
		return err
	}
	if rawSize <= 42 || rawSize > maxMediaSize {
		return fmt.Errorf(
			"%w: invalid expected raw size",
			errDirectFLACIntegrity,
		)
	}
	return nil
}

func validateDirectFLACProbe(
	probe directFLACTrailerProbe,
	rawSize int64,
	header [42]byte,
) error {
	if probe.rawSize != rawSize || probe.header != header {
		return fmt.Errorf(
			"%w: probe source changed",
			errDirectFLACIntegrity,
		)
	}
	rangeStart, err := directFLACProbeRangeStart(header, rawSize)
	if err != nil {
		return err
	}
	if probe.rangeStart != rangeStart ||
		probe.outputSize < 42 ||
		probe.outputSize > rawSize ||
		probe.outputSize < probe.rangeStart ||
		probe.trailerLen != rawSize-probe.outputSize ||
		(probe.trailerLen != 0 &&
			(probe.trailerLen < 8 ||
				probe.trailerLen > directFLACTrailerSearchSize)) ||
		probe.tailHash == ([sha256.Size]byte{}) {
		return fmt.Errorf(
			"%w: invalid probe snapshot",
			errDirectFLACIntegrity,
		)
	}
	return nil
}

func validateDirectFLACURL(rawURL string) error {
	if err := validateMediaURL(rawURL, "flac"); err != nil {
		return errors.Join(errUnsafeMediaURL, err)
	}
	return nil
}

func isIdentityResponse(resp *http.Response) bool {
	if resp == nil {
		return false
	}
	encoding := strings.TrimSpace(resp.Header.Get("Content-Encoding"))
	return encoding == "" || strings.EqualFold(encoding, "identity")
}

func directFLACHTTPClient(baseClient *http.Client) http.Client {
	return directFLACHTTPClientWithTimeout(
		baseClient,
		directFLACDownloadTimeout,
	)
}

func directFLACHTTPClientWithTimeout(
	baseClient *http.Client,
	maximumTimeout time.Duration,
) http.Client {
	client := *baseClient
	if maximumTimeout <= 0 {
		maximumTimeout = directFLACDownloadTimeout
	}
	if client.Timeout <= 0 || client.Timeout > maximumTimeout {
		client.Timeout = maximumTimeout
	}
	previousRedirectPolicy := client.CheckRedirect
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if err := validateDirectFLACURL(req.URL.String()); err != nil {
			return err
		}
		applyMediaHeaders(req)
		req.Header.Set("Accept-Encoding", "identity")
		if len(via) >= 10 {
			return errors.Join(
				errUnsafeMediaURL,
				errors.New("kuwo: too many direct FLAC redirects"),
			)
		}
		if previousRedirectPolicy != nil {
			return previousRedirectPolicy(req, via)
		}
		return nil
	}
	return client
}

type redactedDirectFLACError struct {
	cause error
}

func (err *redactedDirectFLACError) Error() string {
	return "kuwo: direct FLAC request failed"
}

func (err *redactedDirectFLACError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.cause
}

func redactDirectFLACRequestError(err error) error {
	return &redactedDirectFLACError{cause: err}
}

func waitDirectFLACRetry(ctx context.Context, attempt int) error {
	delay := 200 * time.Millisecond * time.Duration(1<<min(attempt, 4))
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func ensureDirectFLACDestinationAbsent(destination string) error {
	_, err := os.Lstat(destination)
	switch {
	case err == nil:
		return fmt.Errorf(
			"kuwo: direct FLAC destination already exists: %w",
			os.ErrExist,
		)
	case errors.Is(err, os.ErrNotExist):
		return nil
	default:
		return fmt.Errorf("kuwo: inspect direct FLAC destination: %w", err)
	}
}
