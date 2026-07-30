package kuwo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/liuran001/MusicBot-Go/bot/platform"
)

const directFLACDownloadBufferSize = 64 << 10

var (
	knownDirectFLACTrailer = []byte{
		0xf0, 0x00, 0xff, 0x0f, 0x44,
		0x44, 0x40, 0x48, 0x46, 0x3c,
		0x36, 0x0e, 0x55, 0xff, 0xf0,
	}
	errDirectFLACIntegrity = errors.New("kuwo: direct FLAC integrity check failed")
)

type directFLACTrailerProbe struct {
	outputSize int64
	strip      bool
	tail       [15]byte
}

// probeDirectFLACTrailer captures the source's final bytes and checks whether
// they match the known non-FLAC trailer seen on Kuwo direct FLAC streams.
// It returns the exact output size and whether the downloader must strip that
// trailer. The downloader rechecks the decision before publishing the file.
func (c *Client) probeDirectFLACTrailer(
	ctx context.Context,
	rawURL string,
	rawSize int64,
) (directFLACTrailerProbe, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateDirectFLACExpectation(rawURL, rawSize); err != nil {
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
) (result directFLACTrailerProbe, retryable bool, returnErr error) {
	client := directFLACHTTPClient(baseClient)
	start := rawSize - int64(len(knownDirectFLACTrailer))
	end := rawSize - 1
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return directFLACTrailerProbe{}, false, errors.New("kuwo: create direct FLAC trailer request")
	}
	applyMediaHeaders(req)
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
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
	if resp.ContentLength != int64(len(knownDirectFLACTrailer)) {
		return directFLACTrailerProbe{}, false, fmt.Errorf(
			"%w: trailer content length mismatch",
			errDirectFLACIntegrity,
		)
	}
	wantRange := fmt.Sprintf("bytes %d-%d/%d", start, end, rawSize)
	if strings.TrimSpace(resp.Header.Get("Content-Range")) != wantRange {
		return directFLACTrailerProbe{}, false, fmt.Errorf(
			"%w: trailer content range mismatch",
			errDirectFLACIntegrity,
		)
	}
	body, err := io.ReadAll(io.LimitReader(
		resp.Body,
		int64(len(knownDirectFLACTrailer))+1,
	))
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return directFLACTrailerProbe{}, false, ctxErr
		}
		return directFLACTrailerProbe{}, true, errors.New("kuwo: read direct FLAC trailer failed")
	}
	if len(body) != len(knownDirectFLACTrailer) {
		return directFLACTrailerProbe{}, false, fmt.Errorf(
			"%w: incomplete trailer response",
			errDirectFLACIntegrity,
		)
	}
	copy(result.tail[:], body)
	result.strip = bytes.Equal(body, knownDirectFLACTrailer)
	result.outputSize = rawSize
	if result.strip {
		result.outputSize -= int64(len(knownDirectFLACTrailer))
	}
	return result, false, nil
}

// directFLACDownloader captures every property verified during resolution.
// The full GET must still match the raw size, trailer state, and STREAMINFO
// exactly; otherwise no destination file is published.
func (c *Client) directFLACDownloader(
	expectedURL string,
	rawSize int64,
	expectedHeader [42]byte,
	expectedTail [15]byte,
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
			expectedTail,
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
	expectedTail [15]byte,
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
	if _, err := parseFLACStreamInfo(expectedHeader[:]); err != nil {
		return 0, fmt.Errorf(
			"%w: invalid expected STREAMINFO",
			errDirectFLACIntegrity,
		)
	}
	expectedStrip := bytes.Equal(expectedTail[:], knownDirectFLACTrailer)
	expectedOutputSize := rawSize
	if expectedStrip {
		expectedOutputSize -= int64(len(knownDirectFLACTrailer))
	}
	if info.Size > 0 && info.Size != expectedOutputSize {
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
	for attempt := 0; attempt < attempts; attempt++ {
		written, retryable, err := downloadDirectFLACOnce(
			ctx,
			client,
			info,
			rawSize,
			expectedHeader,
			expectedTail,
			destination,
			progress,
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
	rawSize int64,
	expectedHeader [42]byte,
	expectedTail [15]byte,
	destination string,
	progress func(written, total int64),
) (written int64, retryable bool, returnErr error) {
	if err := validateDirectFLACURL(info.URL); err != nil {
		return 0, false, err
	}
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

	expectedStrip := bytes.Equal(expectedTail[:], knownDirectFLACTrailer)
	expectedOutputSize := rawSize
	if expectedStrip {
		expectedOutputSize -= int64(len(knownDirectFLACTrailer))
	}
	reader := io.LimitReader(resp.Body, rawSize+1)
	buffer := make([]byte, directFLACDownloadBufferSize)
	header := make([]byte, 0, 42)
	pendingTrailer := make([]byte, 0, len(knownDirectFLACTrailer))
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
			combined := make([]byte, 0, len(pendingTrailer)+readSize)
			combined = append(combined, pendingTrailer...)
			combined = append(combined, chunk...)
			flushSize := len(combined) - len(knownDirectFLACTrailer)
			if flushSize > 0 {
				writeSize, writeErr := temporary.Write(combined[:flushSize])
				if writeErr != nil {
					return 0, false, fmt.Errorf(
						"kuwo: write direct FLAC: %w",
						writeErr,
					)
				}
				if writeSize != flushSize {
					return 0, false, io.ErrShortWrite
				}
				written += int64(writeSize)
				if progress != nil {
					progress(written, expectedOutputSize)
				}
				combined = combined[flushSize:]
			}
			pendingTrailer = append(pendingTrailer[:0], combined...)
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
	if !bytes.Equal(header, expectedHeader[:]) {
		return 0, false, fmt.Errorf(
			"%w: STREAMINFO changed since probe",
			errDirectFLACIntegrity,
		)
	}
	if !bytes.Equal(pendingTrailer, expectedTail[:]) {
		return 0, false, fmt.Errorf(
			"%w: trailer changed since probe",
			errDirectFLACIntegrity,
		)
	}
	actualStrip := bytes.Equal(pendingTrailer, knownDirectFLACTrailer)
	if !actualStrip {
		writeSize, writeErr := temporary.Write(pendingTrailer)
		if writeErr != nil {
			return 0, false, fmt.Errorf(
				"kuwo: write direct FLAC trailer: %w",
				writeErr,
			)
		}
		if writeSize != len(pendingTrailer) {
			return 0, false, io.ErrShortWrite
		}
		written += int64(writeSize)
	}
	if written != expectedOutputSize {
		return 0, false, fmt.Errorf(
			"%w: final output size mismatch",
			errDirectFLACIntegrity,
		)
	}
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

func validateDirectFLACExpectation(
	rawURL string,
	rawSize int64,
) error {
	if err := validateDirectFLACURL(rawURL); err != nil {
		return err
	}
	if rawSize < 42 || rawSize > maxMediaSize ||
		rawSize <= int64(len(knownDirectFLACTrailer)) {
		return fmt.Errorf(
			"%w: invalid expected raw size",
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
	client := *baseClient
	client.Timeout = 0
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
