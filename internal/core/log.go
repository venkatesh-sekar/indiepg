package core

import (
	"context"
	"log/slog"
	"os"
	"strings"
)

// Logger is the panel's small structured-logging helper. It is a thin wrapper
// over log/slog so packages depend on one concrete type rather than passing
// *slog.Logger everywhere, while still allowing the underlying handler to be
// swapped (text for the console, JSON for production, a buffer for tests).
type Logger struct {
	sl *slog.Logger
}

// LogLevel mirrors slog levels with simple string parsing.
type LogLevel string

const (
	LevelDebug LogLevel = "debug"
	LevelInfo  LogLevel = "info"
	LevelWarn  LogLevel = "warn"
	LevelError LogLevel = "error"
)

func (l LogLevel) slog() slog.Level {
	switch LogLevel(strings.ToLower(string(l))) {
	case LevelDebug:
		return slog.LevelDebug
	case LevelWarn:
		return slog.LevelWarn
	case LevelError:
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// NewLogger builds a Logger writing text to stderr at the given level.
func NewLogger(level LogLevel) *Logger {
	h := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level.slog()})
	return &Logger{sl: slog.New(h)}
}

// NewJSONLogger builds a Logger writing JSON to stderr at the given level.
func NewJSONLogger(level LogLevel) *Logger {
	h := slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level.slog()})
	return &Logger{sl: slog.New(h)}
}

// FromSlog wraps an existing *slog.Logger (e.g. one with a test handler).
func FromSlog(sl *slog.Logger) *Logger { return &Logger{sl: sl} }

// Slog returns the underlying *slog.Logger.
func (l *Logger) Slog() *slog.Logger { return l.sl }

// With returns a child Logger with the given key/value attributes attached.
func (l *Logger) With(args ...any) *Logger { return &Logger{sl: l.sl.With(args...)} }

// Debug logs at debug level.
func (l *Logger) Debug(msg string, args ...any) { l.sl.Debug(msg, args...) }

// Info logs at info level.
func (l *Logger) Info(msg string, args ...any) { l.sl.Info(msg, args...) }

// Warn logs at warn level.
func (l *Logger) Warn(msg string, args ...any) { l.sl.Warn(msg, args...) }

// Error logs at error level.
func (l *Logger) Error(msg string, args ...any) { l.sl.Error(msg, args...) }

// DebugCtx logs at debug level with a context (for trace correlation).
func (l *Logger) DebugCtx(ctx context.Context, msg string, args ...any) {
	l.sl.DebugContext(ctx, msg, args...)
}

// InfoCtx logs at info level with a context.
func (l *Logger) InfoCtx(ctx context.Context, msg string, args ...any) {
	l.sl.InfoContext(ctx, msg, args...)
}

// Discard returns a Logger that drops all records (useful in tests).
func Discard() *Logger {
	h := slog.NewTextHandler(discardWriter{}, &slog.HandlerOptions{Level: slog.LevelError + 1})
	return &Logger{sl: slog.New(h)}
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
