package logger

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
)

// LogLevel represents the available log levels
type LogLevel string

const (
	LogLevelDebug LogLevel = "debug"
	LogLevelInfo  LogLevel = "info"
	LogLevelWarn  LogLevel = "warn"
	LogLevelError LogLevel = "error"
)

// Logger provides a structured logger instance configured for the application
type Logger struct {
	*slog.Logger
}

// NewLogger creates a new structured logger with the specified level
func NewLogger(level LogLevel) *Logger {
	return NewLoggerWithConsoleWriter(level, os.Stderr)
}

// NewLoggerWithConsoleWriter builds a logger that writes console output to the given writer
func NewLoggerWithConsoleWriter(level LogLevel, consoleWriter io.Writer) *Logger {
	var slogLevel slog.Level

	switch level {
	case LogLevelDebug:
		slogLevel = slog.LevelDebug
	case LogLevelInfo:
		slogLevel = slog.LevelInfo
	case LogLevelWarn:
		slogLevel = slog.LevelWarn
	case LogLevelError:
		slogLevel = slog.LevelError
	default:
		slogLevel = slog.LevelInfo // Default to info
	}

	// Console: plain, no time/level/msg labels
	if consoleWriter == nil {
		consoleWriter = os.Stderr
	}
	consoleHandler := newPlainHandler(consoleWriter, slogLevel)

	// File: structured text with time and level
	fileHandler := newFileTextHandler(slogLevel)

	// Fan out to both console and file
	handler := newMultiHandler(consoleHandler, fileHandler)
	logger := slog.New(handler)

	return &Logger{Logger: logger}
}

// NewDefaultLogger creates a logger with INFO level for general use
func NewDefaultLogger() *Logger {
	return NewLogger(LogLevelInfo)
}

// NewDebugLogger creates a logger with DEBUG level for development
func NewDebugLogger() *Logger {
	return NewLogger(LogLevelDebug)
}

// WithComponent creates a logger with a component context for better tracing
func (l *Logger) WithComponent(component string) *Logger {
	return &Logger{
		Logger: l.With("component", component),
	}
}

// WithSession creates a logger with session context for request tracing
func (l *Logger) WithSession(sessionID string) *Logger {
	return &Logger{
		Logger: l.With("session", sessionID),
	}
}

// LogWithIntention logs a message at the provided level with an intention tag.
// It prepends a console-friendly icon and adds the structured key "intention".
func (l *Logger) LogWithIntention(level slog.Level, intention Intention, msg string, args ...any) {
	// Do not modify message for file logs; console handler adds icon.
	// Attach intention as structured attribute for files/consumers
	kv := append([]any{"intention", string(intention)}, args...)
	l.Log(context.Background(), level, msg, kv...)
}

func (l *Logger) InfoWithIntention(intention Intention, msg string, args ...any) {
	l.LogWithIntention(slog.LevelInfo, intention, msg, args...)
}

// Warnings and errors do not carry intentions; intention is only for info/debug
func (l *Logger) WarnWithIntention(_ Intention, msg string, args ...any) {
	l.Warn(msg, args...)
}

func (l *Logger) ErrorWithIntention(_ Intention, msg string, args ...any) {
	l.Error(msg, args...)
}

func (l *Logger) DebugWithIntention(intention Intention, msg string, args ...any) {
	l.LogWithIntention(slog.LevelDebug, intention, msg, args...)
}

// InfoWithIcon logs info message with emoji for user-friendly output
func (l *Logger) InfoWithIcon(_ string, msg string, args ...any)  { l.Info(msg, args...) }
func (l *Logger) WarnWithIcon(_ string, msg string, args ...any)  { l.Warn(msg, args...) }
func (l *Logger) ErrorWithIcon(_ string, msg string, args ...any) { l.Error(msg, args...) }
func (l *Logger) DebugWithIcon(_ string, msg string, args ...any) { l.Debug(msg, args...) }

// Default logger instance - single instance for the entire application
var Default = NewDefaultLogger()

// SetGlobalLogLevel updates the global default logger with a new log level
// This affects all component loggers created after this call
func SetGlobalLogLevel(level LogLevel) {
	Default = NewLogger(level)
}

// NewComponentLogger creates a new logger for a specific component
func NewComponentLogger(component string) *Logger {
	return Default.WithComponent(component)
}

// SetGlobalLoggerWithConsoleWriter replaces the global Default logger using the provided console writer
func SetGlobalLoggerWithConsoleWriter(level LogLevel, consoleWriter io.Writer) {
	Default = NewLoggerWithConsoleWriter(level, consoleWriter)
}

// newFileTextHandler opens ~/.klein/logs/klein.log for append and returns a slog text handler
func newFileTextHandler(level slog.Level) slog.Handler {
	// Determine log file path
	home, _ := os.UserHomeDir()
	base := filepath.Join(home, ".klein", "logs")
	_ = os.MkdirAll(base, 0o755)
	path := filepath.Join(base, "klein.log")

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		// Fallback to stderr if file cannot be opened
		return slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	}

	opts := &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				return slog.Attr{Key: "time", Value: slog.StringValue(a.Value.Time().Format("15:04:05"))}
			}
			return a
		},
	}
	return slog.NewTextHandler(f, opts)
}
