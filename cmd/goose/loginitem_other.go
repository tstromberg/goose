//go:build !darwin

// Package main - loginitem_other.go provides stub functions for non-macOS platforms.
package main

import "context"

// addLoginItemUI is a no-op on non-macOS platforms.
func addLoginItemUI(_ context.Context, _ *App) {
	// Login items are only supported on macOS
}
