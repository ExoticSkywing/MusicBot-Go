package recognize

import "context"

// Result represents a recognition result with platform routing info.
type Result struct {
	Platform string
	TrackID  string
	URL      string
}

// Service defines audio recognition behavior.
type Service interface {
	Start(ctx context.Context) error
	Stop() error
	Recognize(ctx context.Context, audioData []byte) (*Result, error)
}

// FileService is an optional extension for recognizers that can decode a local
// media file directly. Keeping it separate from Service preserves compatibility
// with existing recognizers and test doubles while avoiding whole-file reads
// when a local Telegram Bot API exposes its downloaded file path.
type FileService interface {
	RecognizeFile(ctx context.Context, filePath string) (*Result, error)
}
