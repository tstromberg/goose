package dedup

import (
	"testing"
	"time"
)

func TestNew(t *testing.T) {
	m := New(5*time.Second, 1*time.Hour, 100)
	if m == nil {
		t.Fatal("New returned nil")
	}
	if m.window != 5*time.Second {
		t.Errorf("window = %v, want %v", m.window, 5*time.Second)
	}
	if m.cleanupAge != 1*time.Hour {
		t.Errorf("cleanupAge = %v, want %v", m.cleanupAge, 1*time.Hour)
	}
	if m.maxSize != 100 {
		t.Errorf("maxSize = %d, want 100", m.maxSize)
	}
	if m.Size() != 0 {
		t.Errorf("initial Size() = %d, want 0", m.Size())
	}
}

func TestManager_ShouldProcess(t *testing.T) {
	m := New(100*time.Millisecond, 1*time.Hour, 100)
	now := time.Now()

	// First event should be processed
	if !m.ShouldProcess("url1", now) {
		t.Error("First event should be processed")
	}
	if m.Size() != 1 {
		t.Errorf("Size after first event = %d, want 1", m.Size())
	}

	// Duplicate within window should not be processed
	if m.ShouldProcess("url1", now.Add(50*time.Millisecond)) {
		t.Error("Duplicate within dedup window should not be processed")
	}

	// After window, should be processed again
	if !m.ShouldProcess("url1", now.Add(150*time.Millisecond)) {
		t.Error("Event after dedup window should be processed")
	}

	// Different URL should be processed
	if !m.ShouldProcess("url2", now) {
		t.Error("Different URL should be processed")
	}
	if m.Size() != 2 {
		t.Errorf("Size after second URL = %d, want 2", m.Size())
	}
}

func TestManager_Cleanup(t *testing.T) {
	m := New(5*time.Second, 1*time.Minute, 10)
	now := time.Now()

	// Add events to fill beyond max
	for i := range 15 {
		url := "url" + string(rune('0'+i))
		m.ShouldProcess(url, now.Add(-2*time.Minute))
	}

	// Add a new event to trigger cleanup
	m.ShouldProcess("trigger", now)

	// Old entries should be cleaned up
	if sz := m.Size(); sz > 11 {
		t.Errorf("Size after cleanup = %d, expected cleanup to reduce it", sz)
	}
}

func TestManager_MultipleEvents(t *testing.T) {
	m := New(200*time.Millisecond, 1*time.Hour, 100)
	now := time.Now()

	urls := []string{"url1", "url2", "url3"}

	// All should be processed initially
	for _, url := range urls {
		if !m.ShouldProcess(url, now) {
			t.Errorf("Initial event for %s should be processed", url)
		}
	}

	if m.Size() != 3 {
		t.Errorf("Size = %d, want 3", m.Size())
	}

	// All duplicates should be rejected
	for _, url := range urls {
		if m.ShouldProcess(url, now.Add(100*time.Millisecond)) {
			t.Errorf("Duplicate event for %s should not be processed", url)
		}
	}

	// After window, all should be processed again
	for _, url := range urls {
		if !m.ShouldProcess(url, now.Add(250*time.Millisecond)) {
			t.Errorf("Event after window for %s should be processed", url)
		}
	}
}

func TestManager_ExactWindowBoundary(t *testing.T) {
	m := New(100*time.Millisecond, 1*time.Hour, 100)
	now := time.Now()

	m.ShouldProcess("url1", now)

	// Exactly at window boundary should not be processed (< not <=)
	if m.ShouldProcess("url1", now.Add(99*time.Millisecond)) {
		t.Error("Event just before window end should not be processed")
	}

	// Just after window should be processed
	if !m.ShouldProcess("url1", now.Add(100*time.Millisecond)) {
		t.Error("Event at window boundary should be processed")
	}
}
