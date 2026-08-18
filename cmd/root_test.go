package cmd

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
)

func TestParseLogLevel(t *testing.T) {
	tests := []struct {
		input   string
		want    slog.Level
		wantErr bool
	}{
		{"error", slog.LevelError, false},
		{"warn", slog.LevelWarn, false},
		{"info", slog.LevelInfo, false},
		{"debug", slog.LevelDebug, false},
		{"ERROR", slog.LevelError, false},
		{"WARN", slog.LevelWarn, false},
		{"INFO", slog.LevelInfo, false},
		{"DEBUG", slog.LevelDebug, false},
		{"Debug", slog.LevelDebug, false},
		{"Info", slog.LevelInfo, false},
		{"invalid", 0, true},
		{"", 0, true},
	}
	for _, tt := range tests {
		got, err := parseLogLevel(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("parseLogLevel(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			continue
		}

		if !tt.wantErr && got != tt.want {
			t.Errorf("parseLogLevel(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestMessageOnlyHandlerEnabled(t *testing.T) {
	h := &messageOnlyHandler{}

	logLevel.Set(slog.LevelInfo)

	if h.Enabled(context.Background(), slog.LevelDebug) {
		t.Error("expected Debug disabled at Info level")
	}

	if !h.Enabled(context.Background(), slog.LevelInfo) {
		t.Error("expected Info enabled at Info level")
	}

	if !h.Enabled(context.Background(), slog.LevelWarn) {
		t.Error("expected Warn enabled at Info level")
	}

	logLevel.Set(slog.LevelDebug)

	if !h.Enabled(context.Background(), slog.LevelDebug) {
		t.Error("expected Debug enabled at Debug level")
	}

	logLevel.Set(slog.LevelError)

	if h.Enabled(context.Background(), slog.LevelWarn) {
		t.Error("expected Warn disabled at Error level")
	}

	if !h.Enabled(context.Background(), slog.LevelError) {
		t.Error("expected Error enabled at Error level")
	}
}

func TestMessageOnlyHandlerHandle(t *testing.T) {
	tests := []struct {
		name    string
		level   slog.Level
		message string
		attrs   []slog.Attr
		want    string
	}{
		{
			name:    "info message without attrs",
			level:   slog.LevelInfo,
			message: "hello world",
			want:    "hello world\n",
		},
		{
			name:    "debug message with attrs",
			level:   slog.LevelDebug,
			message: "  Layer skipped",
			attrs:   []slog.Attr{slog.String("digest", "abc123"), slog.String("reason", "not in delta")},
			want:    "  Layer skipped digest=abc123 reason=not in delta\n",
		},
		{
			name:    "warn message gets prefix",
			level:   slog.LevelWarn,
			message: "something wrong",
			want:    "WARNING: something wrong\n",
		},
		{
			name:    "error message gets prefix",
			level:   slog.LevelError,
			message: "fatal problem",
			want:    "ERROR: fatal problem\n",
		},
		{
			name:    "error message with attrs",
			level:   slog.LevelError,
			message: "operation failed",
			attrs:   []slog.Attr{slog.String("file", "/tmp/x"), slog.Int("code", 42)},
			want:    "ERROR: operation failed file=/tmp/x code=42\n",
		},
		{
			name:    "integer attr value",
			level:   slog.LevelInfo,
			message: "processed",
			attrs:   []slog.Attr{slog.Int("count", 5)},
			want:    "processed count=5\n",
		},
		{
			name:    "indented message preserves spacing",
			level:   slog.LevelDebug,
			message: "    Compressed layer",
			attrs:   []slog.Attr{slog.Int64("bytes", 1024), slog.String("digest", "def456")},
			want:    "    Compressed layer bytes=1024 digest=def456\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldStderr := os.Stderr
			r, w, _ := os.Pipe()
			os.Stderr = w

			h := &messageOnlyHandler{}
			record := slog.NewRecord(time.Time{}, tt.level, tt.message, 0)
			record.AddAttrs(tt.attrs...)

			err := h.Handle(context.Background(), record)
			if err != nil {
				t.Fatalf("Handle returned error: %v", err)
			}

			w.Close()
			os.Stderr = oldStderr

			var buf bytes.Buffer
			buf.ReadFrom(r)
			got := buf.String()

			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMessageOnlyHandlerWithAttrsAndGroup(t *testing.T) {
	h := &messageOnlyHandler{}

	if h.WithAttrs([]slog.Attr{slog.String("k", "v")}) != h {
		t.Error("WithAttrs should return the same handler")
	}

	if h.WithGroup("g") != h {
		t.Error("WithGroup should return the same handler")
	}
}

func TestParseLogLevelErrorMessage(t *testing.T) {
	_, err := parseLogLevel("bogus")
	if err == nil {
		t.Fatal("expected error for invalid level")
	}

	if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("error message should contain the invalid value, got: %v", err)
	}
}
