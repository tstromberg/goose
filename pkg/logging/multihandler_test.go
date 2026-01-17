package logging

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestMultiHandler_Enabled(t *testing.T) {
	tests := []struct {
		name     string
		handlers []slog.Handler
		level    slog.Level
		want     bool
	}{
		{
			name: "all handlers disabled",
			handlers: []slog.Handler{
				slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelError}),
				slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelError}),
			},
			level: slog.LevelInfo,
			want:  false,
		},
		{
			name: "one handler enabled",
			handlers: []slog.Handler{
				slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelInfo}),
				slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelError}),
			},
			level: slog.LevelInfo,
			want:  true,
		},
		{
			name: "all handlers enabled",
			handlers: []slog.Handler{
				slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelDebug}),
				slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelDebug}),
			},
			level: slog.LevelInfo,
			want:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewMultiHandler(tt.handlers...)
			ctx := context.Background()
			got := h.Enabled(ctx, tt.level)
			if got != tt.want {
				t.Errorf("Enabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMultiHandler_Handle(t *testing.T) {
	var buf1, buf2 bytes.Buffer

	handler1 := slog.NewTextHandler(&buf1, &slog.HandlerOptions{Level: slog.LevelInfo})
	handler2 := slog.NewTextHandler(&buf2, &slog.HandlerOptions{Level: slog.LevelInfo})

	multi := NewMultiHandler(handler1, handler2)

	logger := slog.New(multi)
	logger.Info("test message", "key", "value")

	// Both buffers should contain the log message
	output1 := buf1.String()
	output2 := buf2.String()

	if !strings.Contains(output1, "test message") {
		t.Errorf("handler1 output missing 'test message': %s", output1)
	}
	if !strings.Contains(output2, "test message") {
		t.Errorf("handler2 output missing 'test message': %s", output2)
	}

	if !strings.Contains(output1, "key=value") {
		t.Errorf("handler1 output missing 'key=value': %s", output1)
	}
	if !strings.Contains(output2, "key=value") {
		t.Errorf("handler2 output missing 'key=value': %s", output2)
	}
}

func TestMultiHandler_WithAttrs(t *testing.T) {
	var buf1, buf2 bytes.Buffer

	handler1 := slog.NewTextHandler(&buf1, &slog.HandlerOptions{Level: slog.LevelInfo})
	handler2 := slog.NewTextHandler(&buf2, &slog.HandlerOptions{Level: slog.LevelInfo})

	multi := NewMultiHandler(handler1, handler2)

	// Add attributes
	multiWithAttrs := multi.WithAttrs([]slog.Attr{
		slog.String("source", "test"),
	})

	logger := slog.New(multiWithAttrs)
	logger.Info("test message")

	// Both buffers should contain the attribute
	output1 := buf1.String()
	output2 := buf2.String()

	if !strings.Contains(output1, "source=test") {
		t.Errorf("handler1 output missing attribute: %s", output1)
	}
	if !strings.Contains(output2, "source=test") {
		t.Errorf("handler2 output missing attribute: %s", output2)
	}
}

func TestMultiHandler_WithGroup(t *testing.T) {
	var buf1, buf2 bytes.Buffer

	handler1 := slog.NewTextHandler(&buf1, &slog.HandlerOptions{Level: slog.LevelInfo})
	handler2 := slog.NewTextHandler(&buf2, &slog.HandlerOptions{Level: slog.LevelInfo})

	multi := NewMultiHandler(handler1, handler2)

	// Add group
	multiWithGroup := multi.WithGroup("metrics")

	logger := slog.New(multiWithGroup)
	logger.Info("test message", "count", 42)

	// Both buffers should contain the group
	output1 := buf1.String()
	output2 := buf2.String()

	if !strings.Contains(output1, "metrics.count=42") {
		t.Errorf("handler1 output missing grouped attribute: %s", output1)
	}
	if !strings.Contains(output2, "metrics.count=42") {
		t.Errorf("handler2 output missing grouped attribute: %s", output2)
	}
}

func TestMultiHandler_OneHandlerDisabled(t *testing.T) {
	var buf1, buf2 bytes.Buffer

	// handler1 accepts Info, handler2 only accepts Error
	handler1 := slog.NewTextHandler(&buf1, &slog.HandlerOptions{Level: slog.LevelInfo})
	handler2 := slog.NewTextHandler(&buf2, &slog.HandlerOptions{Level: slog.LevelError})

	multi := NewMultiHandler(handler1, handler2)

	logger := slog.New(multi)
	logger.Info("test message")

	// Only buf1 should have output
	output1 := buf1.String()
	output2 := buf2.String()

	if !strings.Contains(output1, "test message") {
		t.Errorf("handler1 should have logged: %s", output1)
	}
	if output2 != "" {
		t.Errorf("handler2 should not have logged: %s", output2)
	}
}

func TestMultiHandler_Empty(t *testing.T) {
	// Test with no handlers
	multi := NewMultiHandler()

	ctx := context.Background()
	if multi.Enabled(ctx, slog.LevelInfo) {
		t.Error("Enabled() should return false with no handlers")
	}

	// Should not panic
	logger := slog.New(multi)
	logger.Info("test message")
}
