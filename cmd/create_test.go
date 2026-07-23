package cmd

import (
	"testing"
)

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
