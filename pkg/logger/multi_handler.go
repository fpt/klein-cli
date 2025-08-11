package logger

import (
	"context"
	"log/slog"
)

// multiHandler fan-outs records to multiple handlers
type multiHandler struct {
	handlers []slog.Handler
}

func newMultiHandler(handlers ...slog.Handler) slog.Handler {
	return &multiHandler{handlers: handlers}
}

func (m *multiHandler) Enabled(ctx context.Context, lvl slog.Level) bool {
	for _, h := range m.handlers {
		if h.Enabled(ctx, lvl) {
			return true
		}
	}
	return false
}

func (m *multiHandler) Handle(ctx context.Context, r slog.Record) error {
	for _, h := range m.handlers {
		_ = h.Handle(ctx, r)
	}
	return nil
}

func (m *multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	children := make([]slog.Handler, 0, len(m.handlers))
	for _, h := range m.handlers {
		children = append(children, h.WithAttrs(attrs))
	}
	return &multiHandler{handlers: children}
}

func (m *multiHandler) WithGroup(name string) slog.Handler {
	children := make([]slog.Handler, 0, len(m.handlers))
	for _, h := range m.handlers {
		children = append(children, h.WithGroup(name))
	}
	return &multiHandler{handlers: children}
}
