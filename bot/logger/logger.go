package logger

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/liuran001/MusicBot-Go/bot"
)

// Logger wraps slog.Logger to satisfy bot.Logger.
type Logger struct {
	logger  *slog.Logger
	logFile *os.File // Keep reference to close on shutdown
}

const redactionPlaceholder = "[REDACTED]"

// New creates a new Logger with configurable output format.
func New(level, format string, addSource bool) (*Logger, error) {
	return NewWithSecrets(level, format, addSource)
}

// NewWithSecrets creates a Logger that replaces exact secret values before
// records reach the configured output handler.
func NewWithSecrets(level, format string, addSource bool, secrets ...string) (*Logger, error) {
	logFile, output, err := logOutput()
	if err != nil {
		return nil, err
	}

	options := &slog.HandlerOptions{
		Level:     parseLevel(level),
		AddSource: addSource,
	}

	format = strings.ToLower(strings.TrimSpace(format))
	var handler slog.Handler
	if format == "json" {
		handler = slog.NewJSONHandler(output, options)
	} else {
		handler = slog.NewTextHandler(output, options)
	}
	handler = newRedactingHandler(handler, secrets)

	return &Logger{logger: slog.New(handler), logFile: logFile}, nil
}

// With returns a child logger with additional fields.
func (l *Logger) With(args ...any) bot.Logger {
	return &Logger{logger: l.logger.With(args...)}
}

func (l *Logger) Debug(msg string, args ...any) { l.logger.Debug(msg, args...) }
func (l *Logger) Info(msg string, args ...any)  { l.logger.Info(msg, args...) }
func (l *Logger) Warn(msg string, args ...any)  { l.logger.Warn(msg, args...) }
func (l *Logger) Error(msg string, args ...any) { l.logger.Error(msg, args...) }

type redactingHandler struct {
	handler slog.Handler
	secrets []string
}

func newRedactingHandler(handler slog.Handler, secrets []string) slog.Handler {
	normalized := normalizedSecrets(secrets)
	if len(normalized) == 0 {
		return handler
	}
	return &redactingHandler{
		handler: handler,
		secrets: normalized,
	}
}

func (h *redactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.handler.Enabled(ctx, level)
}

func (h *redactingHandler) Handle(ctx context.Context, record slog.Record) error {
	redacted := slog.NewRecord(
		record.Time,
		record.Level,
		h.redactString(record.Message),
		record.PC,
	)
	record.Attrs(func(attr slog.Attr) bool {
		redacted.AddAttrs(h.redactAttr(attr))
		return true
	})
	return h.handler.Handle(ctx, redacted)
}

func (h *redactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	redacted := make([]slog.Attr, len(attrs))
	for i, attr := range attrs {
		redacted[i] = h.redactAttr(attr)
	}
	return &redactingHandler{
		handler: h.handler.WithAttrs(redacted),
		secrets: h.secrets,
	}
}

func (h *redactingHandler) WithGroup(name string) slog.Handler {
	return &redactingHandler{
		handler: h.handler.WithGroup(h.redactString(name)),
		secrets: h.secrets,
	}
}

func (h *redactingHandler) redactAttr(attr slog.Attr) slog.Attr {
	attr.Key = h.redactString(attr.Key)
	attr.Value = h.redactValue(attr.Value)
	return attr
}

func (h *redactingHandler) redactValue(value slog.Value) slog.Value {
	value = value.Resolve()
	switch value.Kind() {
	case slog.KindString:
		return slog.StringValue(h.redactString(value.String()))
	case slog.KindGroup:
		source := value.Group()
		attrs := make([]slog.Attr, len(source))
		for i, attr := range source {
			attrs[i] = h.redactAttr(attr)
		}
		return slog.GroupValue(attrs...)
	case slog.KindAny:
		switch typed := value.Any().(type) {
		case string:
			return slog.StringValue(h.redactString(typed))
		case error:
			return slog.AnyValue(errors.New(h.redactString(typed.Error())))
		case slog.Attr:
			return slog.AnyValue(h.redactAttr(typed))
		default:
			return slog.StringValue(h.redactString(fmt.Sprint(typed)))
		}
	}
	return value
}

func (h *redactingHandler) redactString(value string) string {
	for _, secret := range h.secrets {
		value = strings.ReplaceAll(value, secret, redactionPlaceholder)
	}
	return value
}

func normalizedSecrets(secrets []string) []string {
	unique := make(map[string]struct{}, len(secrets))
	result := make([]string, 0, len(secrets))
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		if _, ok := unique[secret]; ok {
			continue
		}
		unique[secret] = struct{}{}
		result = append(result, secret)
	}
	sort.SliceStable(result, func(i, j int) bool {
		return len(result[i]) > len(result[j])
	})
	return result
}

// Slog returns the underlying slog.Logger.
func (l *Logger) Slog() *slog.Logger {
	return l.logger
}

func parseLevel(level string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	case "info":
		fallthrough
	default:
		return slog.LevelInfo
	}
}

func logOutput() (*os.File, io.Writer, error) {
	if err := os.MkdirAll("./log", 0755); err != nil {
		return nil, nil, err
	}

	fileName := time.Now().Local().Format("2006-01-02") + ".log"
	filePath := filepath.Join("./log", fileName)

	file, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		return nil, nil, err
	}

	return file, io.MultiWriter(os.Stdout, file), nil
}

// Close closes the log file handle.
func (l *Logger) Close() error {
	if l == nil || l.logFile == nil {
		return nil
	}
	return l.logFile.Close()
}
