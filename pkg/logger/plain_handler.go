package logger

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
)

// plainHandler is a minimal slog.Handler that prints only the message
// (already prefixed by any emoji/icon) and appends key=value pairs, without
// time/level decorations. Intended for clean console output.
type plainHandler struct {
	w       io.Writer
	attrs   []slog.Attr
	mu      sync.Mutex
	leveler slog.Leveler
}

func newPlainHandler(w io.Writer, leveler slog.Leveler) slog.Handler {
	return &plainHandler{w: w, leveler: leveler}
}

// Enabled implements slog.Handler by checking level
func (h *plainHandler) Enabled(_ context.Context, lvl slog.Level) bool {
	if h.leveler == nil {
		return true
	}
	return lvl >= h.leveler.Level()
}

// Handle prints the message and key=value pairs without time/level prefixes
func (h *plainHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Extract intention (from bound attrs or record attrs)
	intention := ""
	// helper to probe attrs
	probe := func(a slog.Attr) {
		if a.Key == "intention" {
			if s, ok := a.Value.Any().(string); ok {
				intention = s
			} else {
				intention = a.Value.String()
			}
		}
	}
	for _, a := range h.attrs {
		if a.Value.Kind() == slog.KindGroup {
			for _, ga := range a.Value.Group() {
				probe(ga)
			}
		} else {
			probe(a)
		}
	}
	r.Attrs(func(a slog.Attr) bool {
		if a.Value.Kind() == slog.KindGroup {
			for _, ga := range a.Value.Group() {
				probe(ga)
			}
		} else {
			probe(a)
		}
		return true
	})

	// Build a single line: icon (from intention) + message plus key=val pairs
	icon := ""
	if intention != "" {
		icon = iconFor(Intention(intention)) + " "
	}
	line := icon + r.Message

	// Include any bound attributes first
	for _, a := range h.attrs {
		if a.Value.Kind() == slog.KindGroup {
			// Flatten group attributes
			for _, ga := range a.Value.Group() {
				// Skip meta fields from console output
				if ga.Key == "intention" || ga.Key == "time" || ga.Key == "level" || ga.Key == "msg" || ga.Key == "component" || ga.Key == "session" {
					continue
				}
				line += fmt.Sprintf(" %s=%v", ga.Key, ga.Value)
			}
		} else if a.Key != "time" && a.Key != "level" && a.Key != "msg" && a.Key != "intention" && a.Key != "component" && a.Key != "session" {
			line += fmt.Sprintf(" %s=%v", a.Key, a.Value)
		}
	}

	// Then append record attributes
	r.Attrs(func(a slog.Attr) bool {
		if a.Value.Kind() == slog.KindGroup {
			for _, ga := range a.Value.Group() {
				if ga.Key == "intention" || ga.Key == "time" || ga.Key == "level" || ga.Key == "msg" || ga.Key == "component" || ga.Key == "session" {
					continue
				}
				line += fmt.Sprintf(" %s=%v", ga.Key, ga.Value)
			}
		} else if a.Key != "time" && a.Key != "level" && a.Key != "msg" && a.Key != "intention" && a.Key != "component" && a.Key != "session" {
			line += fmt.Sprintf(" %s=%v", a.Key, a.Value)
		}
		return true
	})

	if _, err := fmt.Fprintln(h.w, line); err != nil {
		return err
	}
	return nil
}

// WithAttrs returns a new handler with additional attributes bound
func (h *plainHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	nh := *h
	nh.attrs = append(append([]slog.Attr{}, h.attrs...), attrs...)
	return &nh
}

// WithGroup groups attributes; for plain output we encode as a group attr
func (h *plainHandler) WithGroup(name string) slog.Handler {
	nh := *h
	nh.attrs = append(nh.attrs, slog.Group(name))
	return &nh
}
