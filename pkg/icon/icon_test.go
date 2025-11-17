package icon

import (
	"bytes"
	"image/png"
	"testing"
)

func TestBadge(t *testing.T) {
	tests := []struct {
		name     string
		incoming int
		outgoing int
		wantNil  bool
	}{
		{"no PRs", 0, 0, true},
		{"incoming only", 3, 0, false},
		{"outgoing only", 0, 5, false},
		{"both", 2, 1, false},
		{"large numbers", 150, 200, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := Badge(tt.incoming, tt.outgoing)
			if err != nil {
				t.Fatalf("Badge() error = %v", err)
			}

			if tt.wantNil {
				if data != nil {
					t.Error("Badge() should return nil for 0/0")
				}
				return
			}

			if data == nil {
				t.Fatal("Badge() returned nil when badge expected")
			}

			// Verify it's valid PNG
			img, err := png.Decode(bytes.NewReader(data))
			if err != nil {
				t.Fatalf("invalid PNG: %v", err)
			}

			// Verify dimensions
			bounds := img.Bounds()
			if bounds.Dx() != Size || bounds.Dy() != Size {
				t.Errorf("wrong dimensions: got %dx%d, want %dx%d",
					bounds.Dx(), bounds.Dy(), Size, Size)
			}
		})
	}
}

func TestCache(t *testing.T) {
	c := NewCache()

	// Cache miss
	if _, ok := c.Get(1, 2); ok {
		t.Error("expected cache miss")
	}

	// Cache hit
	data := []byte("test")
	c.Put(1, 2, data)
	got, ok := c.Get(1, 2)
	if !ok {
		t.Error("expected cache hit")
	}
	if !bytes.Equal(got, data) {
		t.Error("cached data mismatch")
	}
}

func TestFormat(t *testing.T) {
	tests := []struct {
		input int
		want  string
	}{
		{0, "0"},
		{1, "1"},
		{9, "9"},
		{10, "+"},
		{99, "+"},
		{100, "+"},
	}

	for _, tt := range tests {
		got := format(tt.input)
		if got != tt.want {
			t.Errorf("format(%d) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
