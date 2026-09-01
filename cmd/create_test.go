package cmd

import (
	"strings"
	"testing"

	tardiff "github.com/containers/tar-diff/pkg/tar-diff"
)

func TestParseBinaryDiffMethod(t *testing.T) {
	tests := []struct {
		value   string
		want    tardiff.BinaryDiffMethod
		wantErr string
	}{
		{value: "bsdiff", want: tardiff.BinaryDiffBsdiff},
		{value: "zstd", want: tardiff.BinaryDiffZstd},
		{value: "auto", want: tardiff.BinaryDiffAuto},
		{value: "nope", wantErr: `invalid --binary-diff "nope"`},
	}

	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			got, err := parseBinaryDiffMethod(tt.value)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("parseBinaryDiffMethod(%q) error = %v, want substring %q", tt.value, err, tt.wantErr)
				}

				return
			}

			if err != nil {
				t.Fatalf("parseBinaryDiffMethod(%q) unexpected error: %v", tt.value, err)
			}

			if got != tt.want {
				t.Fatalf("parseBinaryDiffMethod(%q) = %v, want %v", tt.value, got, tt.want)
			}
		})
	}
}

func TestParseCreateDiffFlags(t *testing.T) {
	t.Cleanup(func() {
		createBinaryDiff = "bsdiff"
		createZstdDiffWindowMiB = 0
		createMaxZstdDiffSize = 128
		createMaxBsdiffSize = 192
	})

	createBinaryDiff = "auto"
	createZstdDiffWindowMiB = 0
	createMaxZstdDiffSize = 128
	createMaxBsdiffSize = 192

	got, err := parseCreateDiffFlags()
	if err != nil {
		t.Fatalf("parseCreateDiffFlags() error = %v", err)
	}

	if got != tardiff.BinaryDiffAuto {
		t.Fatalf("parseCreateDiffFlags() = %v, want auto", got)
	}

	createZstdDiffWindowMiB = 4

	got, err = parseCreateDiffFlags()
	if err != nil {
		t.Fatalf("parseCreateDiffFlags() power-of-two window error = %v", err)
	}

	if got != tardiff.BinaryDiffAuto {
		t.Fatalf("parseCreateDiffFlags() = %v, want auto", got)
	}

	tests := []struct {
		name    string
		setup   func()
		wantErr string
	}{
		{
			name: "invalid method",
			setup: func() {
				createBinaryDiff = "gzip"
			},
			wantErr: `invalid --binary-diff "gzip"`,
		},
		{
			name: "negative zstd window",
			setup: func() {
				createBinaryDiff = "zstd"
				createZstdDiffWindowMiB = -1
			},
			wantErr: "invalid --zstd-diff-window -1",
		},
		{
			name: "zstd window over max",
			setup: func() {
				createBinaryDiff = "zstd"
				createZstdDiffWindowMiB = 513
			},
			wantErr: "invalid --zstd-diff-window 513 (max 512)",
		},
		{
			name: "zstd window not power of two",
			setup: func() {
				createBinaryDiff = "zstd"
				createZstdDiffWindowMiB = 3
			},
			wantErr: "invalid --zstd-diff-window 3 (must be 0 or a power of two)",
		},
		{
			name: "negative max zstd size",
			setup: func() {
				createBinaryDiff = "zstd"
				createMaxZstdDiffSize = -2
			},
			wantErr: "invalid --max-zstd-diff-size -2",
		},
		{
			name: "negative max bsdiff size",
			setup: func() {
				createBinaryDiff = "bsdiff"
				createMaxBsdiffSize = -3
			},
			wantErr: "invalid --max-bsdiff-size -3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			createBinaryDiff = "bsdiff"
			createZstdDiffWindowMiB = 0
			createMaxZstdDiffSize = 128
			createMaxBsdiffSize = 192
			tt.setup()

			_, err := parseCreateDiffFlags()
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("parseCreateDiffFlags() error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func TestBytesToMB(t *testing.T) {
	tests := []struct {
		bytes int64
		want  string
	}{
		{0, "0.00 MB"},
		{1_000_000, "1.00 MB"},
		{1_500_000, "1.50 MB"},
		{1_234_000, "1.23 MB"},
		{1_239_000, "1.24 MB"},
	}
	for _, tt := range tests {
		if got := bytesToMB(tt.bytes); got != tt.want {
			t.Errorf("bytesToMB(%d) = %q, want %q", tt.bytes, got, tt.want)
		}
	}
}
