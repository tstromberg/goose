// Package logging provides logging utilities for the Goose application.
package logging

import (
	"context"
	"log/slog"
)

// MultiHandler implements slog.Handler to write logs to multiple destinations.
type MultiHandler struct {
	handlers []slog.Handler
}

// NewMultiHandler creates a new MultiHandler that writes to multiple destinations.
func NewMultiHandler(handlers ...slog.Handler) *MultiHandler {
	return &MultiHandler{handlers: handlers}
}

// Enabled returns true if at least one handler is enabled.
func (h *MultiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, handler := range h.handlers {
		if handler.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

// Handle writes the record to all handlers.
// Errors from individual handlers are silently ignored to ensure all handlers execute.
//
//nolint:gocritic // record is an interface parameter, cannot change to pointer
func (h *MultiHandler) Handle(ctx context.Context, record slog.Record) error {
	for _, handler := range h.handlers {
		if handler.Enabled(ctx, record.Level) {
			// Intentionally ignore handler errors to ensure all handlers run
			_ = handler.Handle(ctx, record) //nolint:errcheck // Error intentionally ignored
		}
	}
	return nil
}

// WithAttrs returns a new handler with additional attributes.
func (h *MultiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	handlers := make([]slog.Handler, len(h.handlers))
	for i, handler := range h.handlers {
		handlers[i] = handler.WithAttrs(attrs)
	}
	return &MultiHandler{handlers: handlers}
}

// WithGroup returns a new handler with a group name.
func (h *MultiHandler) WithGroup(name string) slog.Handler {
	handlers := make([]slog.Handler, len(h.handlers))
	for i, handler := range h.handlers {
		handlers[i] = handler.WithGroup(name)
	}
	return &MultiHandler{handlers: handlers}
}
