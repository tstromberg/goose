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
	if _, ok := c.Lookup(1, 2); ok {
		t.Error("expected cache miss")
	}

	// Cache hit
	data := []byte("test")
	c.Put(1, 2, data)
	got, ok := c.Lookup(1, 2)
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

func TestScale(t *testing.T) {
	// Create a test icon (red circle with "5")
	originalData, err := Badge(5, 0)
	if err != nil {
		t.Fatalf("Badge() failed: %v", err)
	}
	if originalData == nil {
		t.Fatal("Badge() returned nil")
	}

	// Scale it
	scaled, err := Scale(originalData)
	if err != nil {
		t.Fatalf("Scale() error = %v", err)
	}

	// Verify it's valid PNG
	img, err := png.Decode(bytes.NewReader(scaled))
	if err != nil {
		t.Fatalf("invalid PNG after scaling: %v", err)
	}

	// Verify dimensions match Size constant
	bounds := img.Bounds()
	if bounds.Dx() != Size || bounds.Dy() != Size {
		t.Errorf("wrong dimensions: got %dx%d, want %dx%d",
			bounds.Dx(), bounds.Dy(), Size, Size)
	}

	// Test error case: invalid PNG data
	_, err = Scale([]byte("not a png"))
	if err == nil {
		t.Error("Scale() should fail with invalid PNG data")
	}
}

func TestCacheOverflow(t *testing.T) {
	c := NewCache()

	// Fill cache to exactly 101 entries (exceeds limit of 100)
	for i := range 101 {
		c.Put(i, 0, []byte("test"))
	}

	// At this point we have 101 entries (exceeds limit but not cleared yet)
	// Add one more entry to trigger cache clear
	c.Put(999, 0, []byte("test"))

	// After clearing and adding entry 999, only entry 999 should be present
	if _, ok := c.Lookup(999, 0); !ok {
		t.Error("expected entry 999 after cache overflow")
	}

	// Old entries should be gone after cache was cleared
	found := 0
	for i := range 101 {
		if _, ok := c.Lookup(i, 0); ok {
			found++
		}
	}
	if found > 0 {
		t.Errorf("expected old entries to be cleared after overflow, but found %d", found)
	}
}
