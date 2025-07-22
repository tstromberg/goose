//go:build !darwin
// +build !darwin

// Package main - loginitem_other.go provides stub functions for non-macOS platforms.
package main

// addLoginItemUI is a no-op on non-macOS platforms
func addLoginItemUI(app *App) {
	// Login items are only supported on macOS
}